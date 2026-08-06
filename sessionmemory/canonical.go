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

const maxCanonicalDeliveryClaims = 128

const maxCanonicalReadRecords = 256

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

// Validate verifies an engine-owned active revision pointer.
func (h ItemHead) Validate() error {
	if !isCanonicalID(h.ItemID) || !isCanonicalID(h.RevisionID) {
		return invalidDerived("canonical item head is invalid")
	}
	return nil
}

// OperationRecord preserves exact replay identity and its committed outcome.
// Fingerprint is application-owned and includes the derivation version.
type OperationRecord struct {
	OperationID string    `json:"operation_id"`
	Fingerprint string    `json:"fingerprint"`
	Outcome     []string  `json:"outcome"`
	CommittedAt time.Time `json:"committed_at"`
}

// CanonicalImportedOperationSchemaVersion identifies the portable imported
// operation record format.
const CanonicalImportedOperationSchemaVersion = "session-memory-canonical-imported-operation/v1"

// CanonicalImportedOperation preserves a v1 idempotent operation outcome
// while the canonical records are being migrated. The nested legacy outcome
// is intentionally retained verbatim: aggregate v1 stages may reference
// rebuildable projections that do not yet have a v2 revision mapping.
type CanonicalImportedOperation struct {
	SchemaVersion string           `json:"schema_version"`
	Outcome       OperationOutcome `json:"outcome"`
}

// Validate verifies one imported operation belongs to the enclosing scope.
func (o CanonicalImportedOperation) Validate(scope Scope) error {
	if o.SchemaVersion != CanonicalImportedOperationSchemaVersion {
		return invalidDerived("unsupported imported operation schema version")
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := o.Outcome.Validate(); err != nil {
		return err
	}
	if o.Outcome.Scope != scope {
		return PermanentError(CodeScopeViolation, "imported operation scope does not match mutation", nil)
	}
	return nil
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

// DeliveryStatus captures the mutable delivery lifecycle for an immutable
// outbox record.
type DeliveryStatus string

const (
	DeliveryStatusPending   DeliveryStatus = "pending"
	DeliveryStatusLeased    DeliveryStatus = "leased"
	DeliveryStatusDelivered DeliveryStatus = "delivered"
	DeliveryStatusTerminal  DeliveryStatus = "terminal"
)

// DeliveryClaim is the mutable lease state attached to one immutable outbox
// record. Lease expiry permits deterministic recovery after worker failure.
type DeliveryClaim struct {
	DeliveryID string         `json:"delivery_id"`
	Status     DeliveryStatus `json:"status"`
	LeaseOwner string         `json:"lease_owner,omitempty"`
	LeaseUntil *time.Time     `json:"lease_until,omitempty"`
	Attempts   uint32         `json:"attempts"`
}

// DeliveryClaimRequest requests a bounded exact-scope outbox lease batch.
type DeliveryClaimRequest struct {
	Scope      Scope     `json:"scope"`
	LeaseOwner string    `json:"lease_owner"`
	Now        time.Time `json:"now"`
	LeaseUntil time.Time `json:"lease_until"`
	Limit      uint32    `json:"limit"`
}

// DeliverySettlementRequest finishes a lease after an external delivery
// attempt. Only the current lease owner may settle an active lease.
type DeliverySettlementRequest struct {
	Scope       Scope          `json:"scope"`
	DeliveryID  string         `json:"delivery_id"`
	LeaseOwner  string         `json:"lease_owner"`
	Status      DeliveryStatus `json:"status"`
	CompletedAt time.Time      `json:"completed_at"`
}

// ClaimedDelivery joins immutable delivery data to its newly acquired lease.
type ClaimedDelivery struct {
	Record DeliveryOutboxRecord `json:"record"`
	Claim  DeliveryClaim        `json:"claim"`
}

// CanonicalMutation is the bounded atomic v2 persistence unit. Records are
// append-only except Heads and ScopeState, which are the only mutable indexes.
type CanonicalMutation struct {
	SchemaVersion        string                       `json:"schema_version"`
	Scope                Scope                        `json:"scope"`
	ExpectedScopeVersion uint64                       `json:"expected_scope_version"`
	Operation            OperationRecord              `json:"operation"`
	ImportedOperations   []CanonicalImportedOperation `json:"imported_operations,omitempty"`
	Sources              []SourceRecordV2             `json:"sources,omitempty"`
	Messages             []MessageRecord              `json:"messages,omitempty"`
	Items                []MemoryItem                 `json:"items,omitempty"`
	Revisions            []MemoryRevision             `json:"revisions,omitempty"`
	Lifecycle            []LifecycleEvent             `json:"lifecycle,omitempty"`
	Heads                []ItemHead                   `json:"heads,omitempty"`
	Delivery             []DeliveryOutboxRecord       `json:"delivery,omitempty"`
	Payloads             []CanonicalPayload           `json:"payloads,omitempty"`
}

// CanonicalPayload couples an encrypted blob to the content-free structural
// reference that names it. A canonical store must persist this pair in the
// same transaction as the mutation that first references it.
type CanonicalPayload struct {
	Ref       PayloadRef       `json:"ref"`
	Encrypted EncryptedPayload `json:"encrypted"`
}

// CanonicalMutationOutcome is returned for both a new commit and an exact
// operation replay.
type CanonicalMutationOutcome struct {
	ScopeVersion uint64   `json:"scope_version"`
	ChangeSeq    uint64   `json:"change_seq"`
	RevisionIDs  []string `json:"revision_ids,omitempty"`
}

// CanonicalRevisionReadRequest hydrates a bounded, exact-scope set of
// immutable revisions. Callers provide IDs from a change log or a bounded
// projection candidate set; implementations must not scan a whole scope.
type CanonicalRevisionReadRequest struct {
	Scope       Scope
	RevisionIDs []string
}

// ActiveHeadScanRequest pages the mutable active-head index in deterministic
// item-ID order. It is the bounded rebuild primitive for projections.
type ActiveHeadScanRequest struct {
	Scope       Scope
	AfterItemID string
	Limit       uint32
}

// ActiveMemoryScanRequest pages active item metadata and the evidence of each
// active revision in deterministic item-ID order. It is the bounded context
// read used by the reconciler; implementations must not load full history.
type ActiveMemoryScanRequest struct {
	Scope       Scope
	AfterItemID string
	Limit       uint32
}

// ActiveCanonicalMemory joins an active item to its current revision evidence.
// It intentionally excludes payload content and other historic revisions.
type ActiveCanonicalMemory struct {
	Item       MemoryItem
	RevisionID string
	Revision   uint64
	Evidence   []EvidenceRef
}

// CanonicalStore is the storage-neutral incremental v2 persistence port.
// Implementations must atomically enforce ScopeState CAS and operation
// fingerprint replay, and must not read or rewrite full scope history to
// commit one mutation.
type CanonicalStore interface {
	LoadScopeState(ctx context.Context, scope Scope) (ScopeState, error)
	ApplyCanonicalMutation(ctx context.Context, mutation CanonicalMutation) (CanonicalMutationOutcome, error)
	ScanScopeChanges(ctx context.Context, scope Scope, after uint64, limit uint32) ([]ScopeChange, error)
	LoadCanonicalRevisions(ctx context.Context, request CanonicalRevisionReadRequest) ([]MemoryRevision, error)
	ScanActiveHeads(ctx context.Context, request ActiveHeadScanRequest) ([]ItemHead, error)
	ScanActiveMemory(ctx context.Context, request ActiveMemoryScanRequest) ([]ActiveCanonicalMemory, error)
	ClaimDeliveryOutbox(ctx context.Context, request DeliveryClaimRequest) ([]ClaimedDelivery, error)
	SettleDeliveryOutbox(ctx context.Context, request DeliverySettlementRequest) error
}

// CanonicalImportedOperationStore is an optional read port for migration
// diagnostics and replay tooling. Regular v2 processing does not depend on
// legacy operation records.
type CanonicalImportedOperationStore interface {
	LoadCanonicalImportedOperation(ctx context.Context, scope Scope, operationID string) (CanonicalImportedOperation, bool, error)
}

func (r CanonicalRevisionReadRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if len(r.RevisionIDs) == 0 || len(r.RevisionIDs) > maxCanonicalReadRecords {
		return limitExceeded("canonical revision read size is invalid")
	}
	seen := make(map[string]struct{}, len(r.RevisionIDs))
	for _, id := range r.RevisionIDs {
		if !isCanonicalID(id) {
			return invalidDerived("canonical revision id is invalid")
		}
		if _, ok := seen[id]; ok {
			return invalidDerived("canonical revision read contains duplicates")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (r ActiveHeadScanRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if r.AfterItemID != "" && !isCanonicalID(r.AfterItemID) {
		return invalidDerived("canonical active-head cursor is invalid")
	}
	if r.Limit == 0 || r.Limit > maxCanonicalReadRecords {
		return limitExceeded("canonical active-head scan limit is invalid")
	}
	return nil
}

func (r ActiveMemoryScanRequest) Validate() error {
	return ActiveHeadScanRequest(r).Validate()
}

func (c DeliveryClaim) Validate() error {
	if !isCanonicalID(c.DeliveryID) {
		return invalidDerived("delivery claim is invalid")
	}
	switch c.Status {
	case DeliveryStatusPending, DeliveryStatusDelivered, DeliveryStatusTerminal:
		if c.LeaseOwner != "" || c.LeaseUntil != nil {
			return invalidDerived("settled delivery claim has a lease")
		}
	case DeliveryStatusLeased:
		if !isCanonicalID(c.LeaseOwner) || c.LeaseUntil == nil || c.LeaseUntil.IsZero() {
			return invalidDerived("leased delivery claim is invalid")
		}
	default:
		return invalidDerived("delivery claim status is invalid")
	}
	return nil
}

func (r DeliveryClaimRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(r.LeaseOwner) || r.Now.IsZero() || !r.LeaseUntil.After(r.Now) || r.Limit == 0 || r.Limit > maxCanonicalDeliveryClaims {
		return invalidDerived("delivery claim request is invalid")
	}
	return nil
}

func (r DeliverySettlementRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(r.DeliveryID) || !isCanonicalID(r.LeaseOwner) || r.CompletedAt.IsZero() {
		return invalidDerived("delivery settlement request is invalid")
	}
	if r.Status != DeliveryStatusDelivered && r.Status != DeliveryStatusTerminal {
		return invalidDerived("delivery settlement status is invalid")
	}
	return nil
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
	count := len(m.ImportedOperations) + len(m.Sources) + len(m.Messages) + len(m.Items) + len(m.Revisions) + len(m.Lifecycle) + len(m.Heads) + len(m.Delivery) + len(m.Payloads)
	if count == 0 || count > maxCanonicalMutationRecords {
		return invalidDerived("canonical mutation record count is invalid")
	}
	importedOperationIDs := make([]string, 0, len(m.ImportedOperations))
	for _, imported := range m.ImportedOperations {
		if err := imported.Validate(m.Scope); err != nil {
			return err
		}
		importedOperationIDs = append(importedOperationIDs, imported.Outcome.OperationID)
	}
	if err := validateUniqueCanonicalIDs(importedOperationIDs, "canonical imported operation"); err != nil {
		return err
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
	if err := validateUniqueCanonicalIDs(deliveryIDs, "canonical delivery outbox"); err != nil {
		return err
	}
	payloadIDs := make([]string, 0, len(m.Payloads))
	for _, payload := range m.Payloads {
		if err := payload.Validate(); err != nil {
			return err
		}
		payloadIDs = append(payloadIDs, payload.Ref.ID)
	}
	return validateUniqueCanonicalIDs(payloadIDs, "canonical payload")
}

// Validate verifies a payload blob can safely be persisted atomically with a
// canonical mutation. Cryptographic opening remains the key-provider's job.
func (p CanonicalPayload) Validate() error {
	if err := p.Ref.Validate(); err != nil {
		return err
	}
	if p.Encrypted.KeyID != p.Ref.KeyID || p.Encrypted.PayloadHash != p.Ref.Digest || len(p.Encrypted.Nonce) == 0 || len(p.Encrypted.Ciphertext) == 0 || len(p.Encrypted.DEKNonce) == 0 || len(p.Encrypted.WrappedDEK) == 0 {
		return invalidDerived("canonical encrypted payload is invalid")
	}
	return nil
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
