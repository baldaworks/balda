package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/normahq/balda/sessionmemory"
)

// BadgerSessionMemoryStore owns one canonical Badger directory. Store methods
// are added alongside the v2 canonical mutation contract; construction is kept
// separate so directory locking and durability defaults are testable now.
type BadgerSessionMemoryStore struct {
	db   *badger.DB
	gcMu sync.Mutex
}

var _ sessionmemory.CanonicalSourceForgetStore = (*BadgerSessionMemoryStore)(nil)

// RunValueLogGC performs at most one non-destructive Badger value-log cleanup.
// A skipped/rejected GC is normal when no reclaimable log exists.
func (s *BadgerSessionMemoryStore) RunValueLogGC(discardRatio float64) error {
	if s == nil || s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	if discardRatio <= 0 || discardRatio >= 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "badger GC discard ratio is invalid", nil)
	}
	s.gcMu.Lock()
	defer s.gcMu.Unlock()
	if err := s.db.RunValueLogGC(discardRatio); err != nil && !errors.Is(err, badger.ErrNoRewrite) {
		return badgerSessionMemoryError("run canonical badger value-log GC", err)
	}
	return nil
}

var _ sessionmemory.CanonicalStore = (*BadgerSessionMemoryStore)(nil)

type badgerCanonicalOperation struct {
	Fingerprint string                                 `json:"fingerprint"`
	Outcome     sessionmemory.CanonicalMutationOutcome `json:"outcome"`
}

type badgerProvenanceEdge struct {
	ChildRevisionID  string `json:"child_revision_id"`
	ParentRevisionID string `json:"parent_revision_id"`
}

type badgerDeniedSource struct {
	SourceID string    `json:"source_id"`
	DeniedAt time.Time `json:"denied_at"`
}

type badgerDeniedRevision struct {
	RevisionID string    `json:"revision_id"`
	DeniedAt   time.Time `json:"denied_at"`
}

func putBadgerSessionMemoryRecord(txn *badger.Txn, key []byte, recordType string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s record: %w", recordType, err)
	}
	envelope, err := sessionmemory.MarshalRecordEnvelope(recordType, payload)
	if err != nil {
		return fmt.Errorf("encode %s record: %w", recordType, err)
	}
	if err := txn.Set(key, envelope); err != nil {
		return fmt.Errorf("write %s record: %w", recordType, err)
	}
	return nil
}

func getBadgerSessionMemoryRecord(txn *badger.Txn, key []byte, recordType string, value any) error {
	item, err := txn.Get(key)
	if err != nil {
		return err
	}
	encoded, err := item.ValueCopy(nil)
	if err != nil {
		return fmt.Errorf("read %s record: %w", recordType, err)
	}
	envelope, err := sessionmemory.UnmarshalRecordEnvelope(encoded)
	if err != nil {
		return fmt.Errorf("decode %s record: %w", recordType, err)
	}
	if envelope.RecordType != recordType {
		return fmt.Errorf("unexpected Badger record type %q", envelope.RecordType)
	}
	if err := json.Unmarshal(envelope.Payload, value); err != nil {
		return fmt.Errorf("unmarshal %s record: %w", recordType, err)
	}
	return nil
}

func putBadgerSessionMemoryImmutableRecord(txn *badger.Txn, key []byte, recordType string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal immutable %s record: %w", recordType, err)
	}
	item, err := txn.Get(key)
	if err == nil {
		encoded, err := item.ValueCopy(nil)
		if err != nil {
			return fmt.Errorf("read immutable %s record: %w", recordType, err)
		}
		envelope, err := sessionmemory.UnmarshalRecordEnvelope(encoded)
		if err != nil || envelope.RecordType != recordType || !bytes.Equal(envelope.Payload, payload) {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical immutable record identity was reused", err)
		}
		return nil
	}
	if !errors.Is(err, badger.ErrKeyNotFound) {
		return fmt.Errorf("lookup immutable %s record: %w", recordType, err)
	}
	return putBadgerSessionMemoryRecord(txn, key, recordType, value)
}

// LoadScopeState reads the small mutable cursor for one exact scope.
func (s *BadgerSessionMemoryStore) LoadScopeState(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.ScopeState, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ScopeState{}, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.ScopeState{}, err
	}
	key, err := badgerScopeKey(scope)
	if err != nil {
		return sessionmemory.ScopeState{}, err
	}
	state := sessionmemory.ScopeState{SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Scope: scope}
	err = s.db.View(func(txn *badger.Txn) error {
		err := getBadgerSessionMemoryRecord(txn, key, badgerRecordScope, &state)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		return err
	})
	if err != nil {
		return sessionmemory.ScopeState{}, badgerSessionMemoryError("load canonical scope state", err)
	}
	if err := state.Validate(); err != nil || state.Scope != scope {
		return sessionmemory.ScopeState{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical scope state is invalid", err)
	}
	return state, nil
}

// ApplyCanonicalMutation appends immutable records and advances the exact
// scope cursor in one Badger transaction.
func (s *BadgerSessionMemoryStore) ApplyCanonicalMutation(ctx context.Context, mutation sessionmemory.CanonicalMutation) (sessionmemory.CanonicalMutationOutcome, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	if err := mutation.Validate(); err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	scopeKey, err := badgerScopeKey(mutation.Scope)
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	operationKey, err := badgerOperationKey(mutation.Scope, mutation.Operation.OperationID)
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	var outcome sessionmemory.CanonicalMutationOutcome
	err = s.db.Update(func(txn *badger.Txn) error {
		var operation badgerCanonicalOperation
		err := getBadgerSessionMemoryRecord(txn, operationKey, badgerRecordOperation, &operation)
		if err == nil {
			if operation.Fingerprint != mutation.Operation.Fingerprint {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical operation identity was reused", nil)
			}
			if err := operation.Outcome.Validate(); err != nil {
				return err
			}
			outcome = operation.Outcome
			return nil
		}
		if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		state := sessionmemory.ScopeState{SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Scope: mutation.Scope}
		err = getBadgerSessionMemoryRecord(txn, scopeKey, badgerRecordScope, &state)
		if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		if err := state.Validate(); err != nil || state.Scope != mutation.Scope {
			return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical scope state is invalid", err)
		}
		if state.Version != mutation.ExpectedScopeVersion {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical scope version changed", nil)
		}
		if err := s.putCanonicalMutationRecords(txn, mutation); err != nil {
			return err
		}
		state.Version++
		state.ChangeSeq++
		outcome = sessionmemory.CanonicalMutationOutcome{ScopeVersion: state.Version, ChangeSeq: state.ChangeSeq}
		for _, revision := range mutation.Revisions {
			outcome.RevisionIDs = append(outcome.RevisionIDs, revision.RevisionID)
		}
		if err := outcome.Validate(); err != nil {
			return err
		}
		change := sessionmemory.ScopeChange{Sequence: state.ChangeSeq, OperationID: mutation.Operation.OperationID, OccurredAt: mutation.Operation.CommittedAt, RevisionIDs: outcome.RevisionIDs}
		changeKey, err := badgerScopeChangeKey(mutation.Scope, change.Sequence)
		if err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, changeKey, badgerRecordChange, change); err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, scopeKey, badgerRecordScope, state); err != nil {
			return err
		}
		return putBadgerSessionMemoryRecord(txn, operationKey, badgerRecordOperation, badgerCanonicalOperation{Fingerprint: mutation.Operation.Fingerprint, Outcome: outcome})
	})
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, badgerSessionMemoryError("apply canonical mutation", err)
	}
	return outcome, nil
}

func (s *BadgerSessionMemoryStore) putCanonicalMutationRecords(txn *badger.Txn, mutation sessionmemory.CanonicalMutation) error {
	if err := validateBadgerMutationReferences(txn, mutation); err != nil {
		return err
	}
	if err := validateBadgerMutationProvenance(txn, mutation); err != nil {
		return err
	}
	for _, source := range mutation.Sources {
		if err := putBadgerCanonicalRecord(txn, mutation.Scope, badgerRecordSource, source.SourceID, source); err != nil {
			return err
		}
	}
	for _, message := range mutation.Messages {
		if err := putBadgerCanonicalRecord(txn, mutation.Scope, badgerRecordMessage, message.MessageID, message); err != nil {
			return err
		}
	}
	for _, item := range mutation.Items {
		if err := putBadgerCanonicalRecord(txn, mutation.Scope, badgerRecordItem, item.ItemID, item); err != nil {
			return err
		}
	}
	for _, revision := range mutation.Revisions {
		if err := putBadgerCanonicalRecord(txn, mutation.Scope, badgerRecordRevision, revision.RevisionID, revision); err != nil {
			return err
		}
		for _, parentID := range revision.Parents {
			edge := badgerProvenanceEdge{ChildRevisionID: revision.RevisionID, ParentRevisionID: parentID}
			forwardKey, err := badgerProvenanceKey(mutation.Scope, badgerRecordProvenanceForward, revision.RevisionID, parentID)
			if err != nil {
				return err
			}
			if err := putBadgerSessionMemoryImmutableRecord(txn, forwardKey, badgerRecordProvenanceForward, edge); err != nil {
				return err
			}
			reverseKey, err := badgerProvenanceKey(mutation.Scope, badgerRecordProvenanceReverse, parentID, revision.RevisionID)
			if err != nil {
				return err
			}
			if err := putBadgerSessionMemoryImmutableRecord(txn, reverseKey, badgerRecordProvenanceReverse, edge); err != nil {
				return err
			}
		}
		for _, evidence := range revision.Evidence {
			key, err := badgerProvenanceKey(mutation.Scope, badgerRecordSourceRevision, evidence.SourceID, revision.RevisionID)
			if err != nil {
				return err
			}
			edge := badgerProvenanceEdge{ChildRevisionID: revision.RevisionID, ParentRevisionID: evidence.SourceID}
			if err := putBadgerSessionMemoryImmutableRecord(txn, key, badgerRecordSourceRevision, edge); err != nil {
				return err
			}
		}
	}
	for _, lifecycle := range mutation.Lifecycle {
		if err := putBadgerCanonicalRecord(txn, mutation.Scope, badgerRecordLifecycle, lifecycle.EventID, lifecycle); err != nil {
			return err
		}
	}
	for _, head := range mutation.Heads {
		key, err := badgerSessionMemoryKey(mutation.Scope, badgerRecordHead, head.ItemID)
		if err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, key, badgerRecordHead, head); err != nil {
			return err
		}
	}
	for _, delivery := range mutation.Delivery {
		if err := putBadgerCanonicalRecord(txn, mutation.Scope, badgerRecordDelivery, delivery.DeliveryID, delivery); err != nil {
			return err
		}
	}
	return nil
}

func validateBadgerMutationReferences(txn *badger.Txn, mutation sessionmemory.CanonicalMutation) error {
	items := make(map[string]struct{}, len(mutation.Items))
	for _, item := range mutation.Items {
		items[item.ItemID] = struct{}{}
	}
	revisions := make(map[string]struct{}, len(mutation.Revisions))
	for _, revision := range mutation.Revisions {
		revisions[revision.RevisionID] = struct{}{}
		if _, exists := items[revision.ItemID]; !exists {
			if err := badgerCanonicalRecordExists(txn, mutation.Scope, badgerRecordItem, revision.ItemID); err != nil {
				return fmt.Errorf("revision item reference: %w", err)
			}
		}
	}
	for _, lifecycle := range mutation.Lifecycle {
		if _, exists := revisions[lifecycle.RevisionID]; !exists {
			if err := badgerCanonicalRecordExists(txn, mutation.Scope, badgerRecordRevision, lifecycle.RevisionID); err != nil {
				return fmt.Errorf("lifecycle revision reference: %w", err)
			}
		}
	}
	for _, head := range mutation.Heads {
		if _, exists := revisions[head.RevisionID]; !exists {
			if err := badgerCanonicalRecordExists(txn, mutation.Scope, badgerRecordRevision, head.RevisionID); err != nil {
				return fmt.Errorf("head revision reference: %w", err)
			}
		}
	}
	return nil
}

func badgerCanonicalRecordExists(txn *badger.Txn, scope sessionmemory.Scope, recordType, id string) error {
	key, err := badgerSessionMemoryKey(scope, recordType, id)
	if err != nil {
		return err
	}
	if _, err := txn.Get(key); errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical record reference does not exist", nil)
	} else if err != nil {
		return err
	}
	return nil
}

func validateBadgerMutationProvenance(txn *badger.Txn, mutation sessionmemory.CanonicalMutation) error {
	revisions := make(map[string]sessionmemory.MemoryRevision, len(mutation.Revisions))
	for _, revision := range mutation.Revisions {
		revisions[revision.RevisionID] = revision
	}
	for _, revision := range mutation.Revisions {
		for _, parentID := range revision.Parents {
			if _, exists := revisions[parentID]; exists {
				continue
			}
			parentKey, err := badgerSessionMemoryKey(mutation.Scope, badgerRecordRevision, parentID)
			if err != nil {
				return err
			}
			if _, err := txn.Get(parentKey); errors.Is(err, badger.ErrKeyNotFound) {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical provenance parent does not exist", nil)
			} else if err != nil {
				return err
			}
		}
	}
	colors := make(map[string]uint8, len(revisions))
	var visit func(string) error
	visit = func(revisionID string) error {
		switch colors[revisionID] {
		case 1:
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical provenance cycle", nil)
		case 2:
			return nil
		}
		revision, exists := revisions[revisionID]
		if !exists {
			return nil
		}
		colors[revisionID] = 1
		for _, parentID := range revision.Parents {
			if err := visit(parentID); err != nil {
				return err
			}
		}
		colors[revisionID] = 2
		return nil
	}
	for revisionID := range revisions {
		if err := visit(revisionID); err != nil {
			return err
		}
	}
	return nil
}

func putBadgerCanonicalRecord(txn *badger.Txn, scope sessionmemory.Scope, recordType, id string, value any) error {
	key, err := badgerSessionMemoryKey(scope, recordType, id)
	if err != nil {
		return err
	}
	return putBadgerSessionMemoryImmutableRecord(txn, key, recordType, value)
}

// ScanScopeChanges returns a bounded ordered tail for projection recovery.
func (s *BadgerSessionMemoryStore) ScanScopeChanges(ctx context.Context, scope sessionmemory.Scope, after uint64, limit uint32) ([]sessionmemory.ScopeChange, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if limit == 0 || limit > 512 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical change scan limit is invalid", nil)
	}
	prefix, err := badgerScopeChangePrefix(scope)
	if err != nil {
		return nil, err
	}
	changes := make([]sessionmemory.ScopeChange, 0, limit)
	err = s.db.View(func(txn *badger.Txn) error {
		options := badger.DefaultIteratorOptions
		options.Prefix = prefix
		iterator := txn.NewIterator(options)
		defer iterator.Close()
		start, err := badgerScopeChangeKey(scope, after)
		if err != nil {
			return err
		}
		for iterator.Seek(start); iterator.ValidForPrefix(prefix) && uint32(len(changes)) < limit; iterator.Next() {
			var change sessionmemory.ScopeChange
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordChange, &change); err != nil {
				return err
			}
			if err := change.Validate(); err != nil {
				return err
			}
			if change.Sequence <= after {
				continue
			}
			changes = append(changes, change)
		}
		return nil
	})
	if err != nil {
		return nil, badgerSessionMemoryError("scan canonical scope changes", err)
	}
	return changes, nil
}

// ClaimDeliveryOutbox atomically leases pending or expired exact-scope
// deliveries. Immutable records are never modified while delivery state moves.
func (s *BadgerSessionMemoryStore) ClaimDeliveryOutbox(ctx context.Context, request sessionmemory.DeliveryClaimRequest) ([]sessionmemory.ClaimedDelivery, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	prefix, err := badgerSessionMemoryPrefix(request.Scope, badgerRecordDelivery)
	if err != nil {
		return nil, err
	}
	claimed := make([]sessionmemory.ClaimedDelivery, 0, request.Limit)
	err = s.db.Update(func(txn *badger.Txn) error {
		options := badger.DefaultIteratorOptions
		options.Prefix = prefix
		iterator := txn.NewIterator(options)
		defer iterator.Close()
		for iterator.Rewind(); iterator.ValidForPrefix(prefix) && uint32(len(claimed)) < request.Limit; iterator.Next() {
			var record sessionmemory.DeliveryOutboxRecord
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordDelivery, &record); err != nil {
				return err
			}
			claimKey, err := badgerSessionMemoryKey(request.Scope, badgerRecordDeliveryClaim, record.DeliveryID)
			if err != nil {
				return err
			}
			claim := sessionmemory.DeliveryClaim{DeliveryID: record.DeliveryID, Status: sessionmemory.DeliveryStatusPending}
			err = getBadgerSessionMemoryRecord(txn, claimKey, badgerRecordDeliveryClaim, &claim)
			if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			if claim.Status == sessionmemory.DeliveryStatusDelivered || claim.Status == sessionmemory.DeliveryStatusTerminal ||
				(claim.Status == sessionmemory.DeliveryStatusLeased && claim.LeaseUntil != nil && claim.LeaseUntil.After(request.Now)) {
				continue
			}
			leaseUntil := request.LeaseUntil
			claim = sessionmemory.DeliveryClaim{DeliveryID: record.DeliveryID, Status: sessionmemory.DeliveryStatusLeased, LeaseOwner: request.LeaseOwner, LeaseUntil: &leaseUntil, Attempts: claim.Attempts + 1}
			if err := putBadgerSessionMemoryRecord(txn, claimKey, badgerRecordDeliveryClaim, claim); err != nil {
				return err
			}
			claimed = append(claimed, sessionmemory.ClaimedDelivery{Record: record, Claim: claim})
		}
		return nil
	})
	if err != nil {
		return nil, badgerSessionMemoryError("claim canonical delivery outbox", err)
	}
	return claimed, nil
}

// SettleDeliveryOutbox marks an active exact-worker lease as delivered or
// terminal. A stale or foreign worker cannot settle another worker's lease.
func (s *BadgerSessionMemoryStore) SettleDeliveryOutbox(ctx context.Context, request sessionmemory.DeliverySettlementRequest) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	claimKey, err := badgerSessionMemoryKey(request.Scope, badgerRecordDeliveryClaim, request.DeliveryID)
	if err != nil {
		return err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var claim sessionmemory.DeliveryClaim
		if err := getBadgerSessionMemoryRecord(txn, claimKey, badgerRecordDeliveryClaim, &claim); err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "canonical delivery claim does not exist", nil)
			}
			return err
		}
		if claim.Status != sessionmemory.DeliveryStatusLeased || claim.LeaseOwner != request.LeaseOwner {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical delivery lease is not owned by this worker", nil)
		}
		claim.Status = request.Status
		claim.LeaseOwner = ""
		claim.LeaseUntil = nil
		return putBadgerSessionMemoryRecord(txn, claimKey, badgerRecordDeliveryClaim, claim)
	})
	if err != nil {
		return badgerSessionMemoryError("settle canonical delivery outbox", err)
	}
	return nil
}

// PutEncryptedPayload persists an encrypted blob outside structural canonical
// records. The caller must have produced the payload with SealPayload.
func (s *BadgerSessionMemoryStore) PutEncryptedPayload(ctx context.Context, encrypted sessionmemory.EncryptedPayload, ref sessionmemory.PayloadRef) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if !isBadgerPayloadValid(encrypted, ref) {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encrypted payload is invalid", nil)
	}
	key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, ref.ID)
	if err != nil {
		return err
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return putBadgerSessionMemoryImmutableRecord(txn, key, badgerRecordPayload, encrypted)
	}); err != nil {
		return badgerSessionMemoryError("store encrypted payload", err)
	}
	return nil
}

// LoadEncryptedPayload reads a content-free reference's encrypted blob.
func (s *BadgerSessionMemoryStore) LoadEncryptedPayload(ctx context.Context, ref sessionmemory.PayloadRef) (sessionmemory.EncryptedPayload, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.EncryptedPayload{}, err
	}
	if err := ref.Validate(); err != nil {
		return sessionmemory.EncryptedPayload{}, err
	}
	key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, ref.ID)
	if err != nil {
		return sessionmemory.EncryptedPayload{}, err
	}
	var encrypted sessionmemory.EncryptedPayload
	if err := s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordPayload, &encrypted)
	}); err != nil {
		return sessionmemory.EncryptedPayload{}, badgerSessionMemoryError("load encrypted payload", err)
	}
	if !isBadgerPayloadValid(encrypted, ref) {
		return sessionmemory.EncryptedPayload{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored encrypted payload is invalid", nil)
	}
	return encrypted, nil
}

// DeleteEncryptedPayload irreversibly removes an encrypted payload blob after
// its logical forget state has committed. Structural tombstones remain intact.
func (s *BadgerSessionMemoryStore) DeleteEncryptedPayload(ctx context.Context, ref sessionmemory.PayloadRef) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, ref.ID)
	if err != nil {
		return err
	}
	if err := s.db.Update(func(txn *badger.Txn) error { return txn.Delete(key) }); err != nil {
		return badgerSessionMemoryError("delete encrypted payload", err)
	}
	return nil
}

// DenySource commits the fail-closed logical forget marker before any
// asynchronous provenance traversal or payload scrubbing begins.
func (s *BadgerSessionMemoryStore) DenySource(ctx context.Context, scope sessionmemory.Scope, sourceID string, deniedAt time.Time) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(sourceID) != sourceID || sourceID == "" || deniedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "source deny request is invalid", nil)
	}
	key, err := badgerSessionMemoryKey(scope, badgerRecordDeniedSource, sourceID)
	if err != nil {
		return err
	}
	denied := badgerDeniedSource{SourceID: sourceID, DeniedAt: deniedAt.UTC()}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return putBadgerDenyRecord(txn, key, badgerRecordDeniedSource, denied)
	}); err != nil {
		return badgerSessionMemoryError("deny canonical source", err)
	}
	return nil
}

// IsSourceDenied lets recall and traversal fail closed before physical scrub.
func (s *BadgerSessionMemoryStore) IsSourceDenied(ctx context.Context, scope sessionmemory.Scope, sourceID string) (bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return false, err
	}
	key, err := badgerSessionMemoryKey(scope, badgerRecordDeniedSource, sourceID)
	if err != nil {
		return false, err
	}
	err = s.db.View(func(txn *badger.Txn) error {
		var denied badgerDeniedSource
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordDeniedSource, &denied)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, badgerSessionMemoryError("read canonical source deny", err)
	}
	return true, nil
}

// DenyRevision commits a fail-closed cascade result for one revision.
func (s *BadgerSessionMemoryStore) DenyRevision(ctx context.Context, scope sessionmemory.Scope, revisionID string, deniedAt time.Time) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if revisionID == "" || deniedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "revision deny request is invalid", nil)
	}
	key, err := badgerSessionMemoryKey(scope, badgerRecordDeniedRevision, revisionID)
	if err != nil {
		return err
	}
	denied := badgerDeniedRevision{RevisionID: revisionID, DeniedAt: deniedAt.UTC()}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return putBadgerDenyRecord(txn, key, badgerRecordDeniedRevision, denied)
	}); err != nil {
		return badgerSessionMemoryError("deny canonical revision", err)
	}
	return nil
}

func putBadgerDenyRecord(txn *badger.Txn, key []byte, recordType string, value any) error {
	if _, err := txn.Get(key); err == nil {
		return nil
	} else if !errors.Is(err, badger.ErrKeyNotFound) {
		return err
	}
	return putBadgerSessionMemoryRecord(txn, key, recordType, value)
}

// IsRevisionDenied lets recall fail closed while physical scrub is pending.
func (s *BadgerSessionMemoryStore) IsRevisionDenied(ctx context.Context, scope sessionmemory.Scope, revisionID string) (bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return false, err
	}
	key, err := badgerSessionMemoryKey(scope, badgerRecordDeniedRevision, revisionID)
	if err != nil {
		return false, err
	}
	err = s.db.View(func(txn *badger.Txn) error {
		var denied badgerDeniedRevision
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordDeniedRevision, &denied)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, badgerSessionMemoryError("read canonical revision deny", err)
	}
	return true, nil
}

// SourceRevisionBatch returns a bounded, resumable source-provenance batch.
// The next cursor is the final revision ID returned; an empty cursor means
// the source has no further indexed descendants in this batch direction.
func (s *BadgerSessionMemoryStore) SourceRevisionBatch(ctx context.Context, scope sessionmemory.Scope, sourceID, afterRevisionID string, limit uint32) ([]string, string, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, "", err
	}
	if err := scope.Validate(); err != nil {
		return nil, "", err
	}
	if sourceID == "" || limit == 0 || limit > 512 {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "source provenance batch limit is invalid", nil)
	}
	prefix, err := badgerProvenancePrefix(scope, badgerRecordSourceRevision, sourceID)
	if err != nil {
		return nil, "", err
	}
	results := make([]string, 0, limit)
	err = s.db.View(func(txn *badger.Txn) error {
		options := badger.DefaultIteratorOptions
		options.Prefix = prefix
		iterator := txn.NewIterator(options)
		defer iterator.Close()
		for iterator.Rewind(); iterator.ValidForPrefix(prefix) && uint32(len(results)) < limit; iterator.Next() {
			var edge badgerProvenanceEdge
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordSourceRevision, &edge); err != nil {
				return err
			}
			if edge.ChildRevisionID > afterRevisionID {
				results = append(results, edge.ChildRevisionID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", badgerSessionMemoryError("scan source provenance", err)
	}
	if len(results) == 0 {
		return results, "", nil
	}
	return results, results[len(results)-1], nil
}

func isBadgerPayloadValid(encrypted sessionmemory.EncryptedPayload, ref sessionmemory.PayloadRef) bool {
	return ref.Validate() == nil && encrypted.KeyID == ref.KeyID && encrypted.PayloadHash == ref.Digest && len(encrypted.Nonce) > 0 && len(encrypted.Ciphertext) > 0 && len(encrypted.DEKNonce) > 0 && len(encrypted.WrappedDEK) > 0
}

func badgerSessionMemoryError(operation string, err error) error {
	if errors.Is(err, badger.ErrTxnTooBig) {
		return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, operation, err)
	}
	if errors.Is(err, badger.ErrConflict) {
		return sessionmemory.RetryableError(sessionmemory.CodeConflict, operation, err)
	}
	var memoryError *sessionmemory.Error
	if errors.As(err, &memoryError) {
		return err
	}
	return sessionmemory.RetryableError(sessionmemory.CodeStoreFailure, operation, err)
}

// OpenBadgerSessionMemoryStore opens the sole writable canonical directory.
func OpenBadgerSessionMemoryStore(directory string) (*BadgerSessionMemoryStore, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("badger session-memory directory is required")
	}
	db, err := badger.Open(badger.DefaultOptions(directory).
		WithSyncWrites(true).
		WithDetectConflicts(true).
		WithNumVersionsToKeep(1))
	if err != nil {
		return nil, fmt.Errorf("open canonical badger session-memory store: %w", err)
	}
	return &BadgerSessionMemoryStore{db: db}, nil
}

// Close syncs and releases the canonical directory lock.
func (s *BadgerSessionMemoryStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Sync(); err != nil {
		return fmt.Errorf("sync canonical badger session-memory store: %w", err)
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close canonical badger session-memory store: %w", err)
	}
	s.db = nil
	return nil
}
