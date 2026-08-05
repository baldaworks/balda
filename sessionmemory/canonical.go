package sessionmemory

import (
	"context"
	"time"
)

// CanonicalSchemaVersionV1 identifies the storage-neutral incremental
// canonical-store contract. It is intentionally separate from the v1
// ScopeSnapshot store, whose full-scope representation cannot meet v2 write
// cost requirements.
const CanonicalSchemaVersionV1 = "session-memory-canonical/v1"

const maxCanonicalMutationRecords = 512

// ScopeState is the small mutable record for one exact memory scope.
// All memory content is stored in immutable records referenced by this state.
type ScopeState struct {
	SchemaVersion string `json:"schema_version"`
	Scope         Scope  `json:"scope"`
	Version       uint64 `json:"version"`
	ChangeSeq     uint64 `json:"change_seq"`
}

// ItemHead is the mutable active revision pointer for one logical item.
type ItemHead struct {
	ItemID     string `json:"item_id"`
	RevisionID string `json:"revision_id"`
}

// OperationRecord preserves exact replay identity and its committed outcome.
// Fingerprint is application-owned and includes the derivation version.
type OperationRecord struct {
	OperationID string    `json:"operation_id"`
	Fingerprint string    `json:"fingerprint"`
	Outcome     []string  `json:"outcome"`
	CommittedAt time.Time `json:"committed_at"`
}

// ScopeChange is an ordered, immutable projection-replay entry.
type ScopeChange struct {
	Sequence    uint64    `json:"sequence"`
	OperationID string    `json:"operation_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	RevisionIDs []string  `json:"revision_ids,omitempty"`
}

// DeliveryOutboxRecord is the canonical post-mutation side-effect record.
// Its payload is an opaque application-owned reference; side-effect delivery
// state remains outside immutable memory records.
type DeliveryOutboxRecord struct {
	DeliveryID  string     `json:"delivery_id"`
	OperationID string     `json:"operation_id"`
	PayloadRef  PayloadRef `json:"payload_ref"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CanonicalMutation is the bounded atomic v2 persistence unit. Records are
// append-only except Heads and ScopeState, which are the only mutable indexes.
type CanonicalMutation struct {
	SchemaVersion        string                 `json:"schema_version"`
	Scope                Scope                  `json:"scope"`
	ExpectedScopeVersion uint64                 `json:"expected_scope_version"`
	Operation            OperationRecord        `json:"operation"`
	Sources              []SourceRecordV2       `json:"sources,omitempty"`
	Messages             []MessageRecord        `json:"messages,omitempty"`
	Items                []MemoryItem           `json:"items,omitempty"`
	Revisions            []MemoryRevision       `json:"revisions,omitempty"`
	Lifecycle            []LifecycleEvent       `json:"lifecycle,omitempty"`
	Heads                []ItemHead             `json:"heads,omitempty"`
	Delivery             []DeliveryOutboxRecord `json:"delivery,omitempty"`
}

// CanonicalMutationOutcome is returned for both a new commit and an exact
// operation replay.
type CanonicalMutationOutcome struct {
	ScopeVersion uint64   `json:"scope_version"`
	ChangeSeq    uint64   `json:"change_seq"`
	RevisionIDs  []string `json:"revision_ids,omitempty"`
}

// CanonicalStore is the storage-neutral incremental v2 persistence port.
// Implementations must atomically enforce ScopeState CAS and operation
// fingerprint replay, and must not read or rewrite full scope history to
// commit one mutation.
type CanonicalStore interface {
	LoadScopeState(ctx context.Context, scope Scope) (ScopeState, error)
	ApplyCanonicalMutation(ctx context.Context, mutation CanonicalMutation) (CanonicalMutationOutcome, error)
	ScanScopeChanges(ctx context.Context, scope Scope, after uint64, limit uint32) ([]ScopeChange, error)
}

func (s ScopeState) Validate() error {
	if s.SchemaVersion != CanonicalSchemaVersionV1 {
		return invalidDerived("canonical scope state schema version is invalid")
	}
	return s.Scope.Validate()
}

func (m CanonicalMutation) Validate() error {
	if m.SchemaVersion != CanonicalSchemaVersionV1 {
		return invalidDerived("canonical mutation schema version is invalid")
	}
	if err := m.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(m.Operation.OperationID) || !isCanonicalID(m.Operation.Fingerprint) || m.Operation.CommittedAt.IsZero() {
		return invalidDerived("canonical mutation operation is invalid")
	}
	if err := validateUniqueCanonicalIDs(m.Operation.Outcome, "canonical operation outcome"); err != nil {
		return err
	}
	count := len(m.Sources) + len(m.Messages) + len(m.Items) + len(m.Revisions) + len(m.Lifecycle) + len(m.Heads) + len(m.Delivery)
	if count == 0 || count > maxCanonicalMutationRecords {
		return invalidDerived("canonical mutation record count is invalid")
	}
	sourceIDs := make([]string, 0, len(m.Sources))
	for _, source := range m.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Scope != m.Scope {
			return invalidDerived("canonical source scope does not match mutation")
		}
		sourceIDs = append(sourceIDs, source.SourceID)
	}
	if err := validateUniqueCanonicalIDs(sourceIDs, "canonical source"); err != nil {
		return err
	}
	messageIDs := make([]string, 0, len(m.Messages))
	for _, message := range m.Messages {
		if err := message.Validate(); err != nil {
			return err
		}
		messageIDs = append(messageIDs, message.MessageID)
	}
	if err := validateUniqueCanonicalIDs(messageIDs, "canonical message"); err != nil {
		return err
	}
	itemIDs := make([]string, 0, len(m.Items))
	for _, item := range m.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if item.Scope != m.Scope {
			return invalidDerived("canonical item scope does not match mutation")
		}
		itemIDs = append(itemIDs, item.ItemID)
	}
	if err := validateUniqueCanonicalIDs(itemIDs, "canonical item"); err != nil {
		return err
	}
	revisionIDs := make([]string, 0, len(m.Revisions))
	for _, revision := range m.Revisions {
		if err := revision.Validate(); err != nil {
			return err
		}
		revisionIDs = append(revisionIDs, revision.RevisionID)
	}
	if err := validateUniqueCanonicalIDs(revisionIDs, "canonical revision"); err != nil {
		return err
	}
	lifecycleIDs := make([]string, 0, len(m.Lifecycle))
	for _, lifecycle := range m.Lifecycle {
		if err := lifecycle.Validate(); err != nil {
			return err
		}
		lifecycleIDs = append(lifecycleIDs, lifecycle.EventID)
	}
	if err := validateUniqueCanonicalIDs(lifecycleIDs, "canonical lifecycle event"); err != nil {
		return err
	}
	headItemIDs := make([]string, 0, len(m.Heads))
	for _, head := range m.Heads {
		if !isCanonicalID(head.ItemID) || !isCanonicalID(head.RevisionID) {
			return invalidDerived("canonical item head is invalid")
		}
		headItemIDs = append(headItemIDs, head.ItemID)
	}
	if err := validateUniqueCanonicalIDs(headItemIDs, "canonical item head"); err != nil {
		return err
	}
	deliveryIDs := make([]string, 0, len(m.Delivery))
	for _, delivery := range m.Delivery {
		if err := delivery.Validate(); err != nil {
			return err
		}
		deliveryIDs = append(deliveryIDs, delivery.DeliveryID)
	}
	return validateUniqueCanonicalIDs(deliveryIDs, "canonical delivery outbox")
}

func validateUniqueCanonicalIDs(ids []string, name string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !isCanonicalID(id) {
			return invalidDerived(name + " identity is invalid")
		}
		if _, exists := seen[id]; exists {
			return invalidDerived(name + " identity is duplicated")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (o CanonicalMutationOutcome) Validate() error {
	if o.ScopeVersion == 0 || o.ChangeSeq == 0 {
		return invalidDerived("canonical mutation outcome is invalid")
	}
	for _, revisionID := range o.RevisionIDs {
		if !isCanonicalID(revisionID) {
			return invalidDerived("canonical mutation outcome revision is invalid")
		}
	}
	return nil
}

func (c ScopeChange) Validate() error {
	if c.Sequence == 0 || !isCanonicalID(c.OperationID) || c.OccurredAt.IsZero() {
		return invalidDerived("scope change is invalid")
	}
	for _, revisionID := range c.RevisionIDs {
		if !isCanonicalID(revisionID) {
			return invalidDerived("scope change revision is invalid")
		}
	}
	return nil
}

func (d DeliveryOutboxRecord) Validate() error {
	if !isCanonicalID(d.DeliveryID) || !isCanonicalID(d.OperationID) || d.CreatedAt.IsZero() {
		return invalidDerived("delivery outbox record is invalid")
	}
	return d.PayloadRef.Validate()
}
