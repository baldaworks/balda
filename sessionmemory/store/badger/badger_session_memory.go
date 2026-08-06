package badger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// maintenanceMu excludes lifecycle/maintenance operations while allowing
	// independent canonical mutations to use Badger's own concurrency control.
	maintenanceMu                 sync.RWMutex
	beforeCanonicalMutationCommit func() error // test fault-injection seam; nil in production.
}

// DB exposes the owned handle only to composition adapters that need a
// backend-specific maintenance operation. Callers should prefer the
// storage-neutral ports implemented by the store.
func (s *BadgerSessionMemoryStore) DB() *badger.DB {
	if s == nil {
		return nil
	}
	return s.db
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
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if err := s.db.RunValueLogGC(discardRatio); err != nil && !errors.Is(err, badger.ErrNoRewrite) {
		return badgerSessionMemoryError("run canonical badger value-log GC", err)
	}
	return nil
}

var _ sessionmemory.CanonicalStore = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.ProjectionCheckpointStore = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.ProjectionRetentionFloorWriter = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.ScopeCheckpointStore = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.CanonicalOperationReader = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.CanonicalOperationCommitter = (*BadgerSessionMemoryStore)(nil)

type badgerCanonicalOperation struct {
	Fingerprint string                                 `json:"fingerprint"`
	Outcome     sessionmemory.CanonicalMutationOutcome `json:"outcome"`
}

// LoadCanonicalOperation reads one exact-scope v2 mutation replay record.
func (s *BadgerSessionMemoryStore) LoadCanonicalOperation(ctx context.Context, scope sessionmemory.Scope, operationID string) (sessionmemory.CanonicalOperationRecord, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.CanonicalOperationRecord{}, false, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.CanonicalOperationRecord{}, false, err
	}
	if err := (sessionmemory.RevisionRef{ItemID: operationID, RevisionID: operationID}).Validate(); err != nil {
		return sessionmemory.CanonicalOperationRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical operation id is invalid", nil)
	}
	if s == nil {
		return sessionmemory.CanonicalOperationRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.CanonicalOperationRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerOperationKey(scope, operationID)
	if err != nil {
		return sessionmemory.CanonicalOperationRecord{}, false, err
	}
	var stored badgerCanonicalOperation
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordOperation, &stored)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.CanonicalOperationRecord{}, false, nil
	}
	if err != nil {
		return sessionmemory.CanonicalOperationRecord{}, false, badgerSessionMemoryError("load canonical operation", err)
	}
	record := sessionmemory.CanonicalOperationRecord{Fingerprint: stored.Fingerprint, Outcome: stored.Outcome}
	if err := record.Validate(); err != nil {
		return sessionmemory.CanonicalOperationRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical operation outcome is invalid", err)
	}
	return record, true, nil
}

// CommitCanonicalOperation durably advances one scope for an operation that
// has no revision records while preserving replay and CAS semantics.
func (s *BadgerSessionMemoryStore) CommitCanonicalOperation(ctx context.Context, request sessionmemory.CanonicalOperationCommitRequest) (sessionmemory.CanonicalMutationOutcome, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	if s == nil {
		return sessionmemory.CanonicalMutationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.CanonicalMutationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	operationKey, err := badgerOperationKey(request.Scope, request.OperationID)
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	scopeKey, err := badgerScopeKey(request.Scope)
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	var outcome sessionmemory.CanonicalMutationOutcome
	err = s.db.Update(func(txn *badger.Txn) error {
		var stored badgerCanonicalOperation
		lookupErr := getBadgerSessionMemoryRecord(txn, operationKey, badgerRecordOperation, &stored)
		if lookupErr == nil {
			if stored.Fingerprint != request.Fingerprint {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical operation identity was reused", nil)
			}
			if err := stored.Outcome.Validate(); err != nil {
				return err
			}
			outcome = stored.Outcome
			return nil
		}
		if !errors.Is(lookupErr, badger.ErrKeyNotFound) {
			return lookupErr
		}
		state := sessionmemory.ScopeState{SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Scope: request.Scope}
		stateErr := getBadgerSessionMemoryRecord(txn, scopeKey, badgerRecordScope, &state)
		if stateErr != nil && !errors.Is(stateErr, badger.ErrKeyNotFound) {
			return stateErr
		}
		if err := state.Validate(); err != nil || state.Scope != request.Scope {
			return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical scope state is invalid", err)
		}
		if state.Version != request.ExpectedScopeVersion {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical scope version changed", nil)
		}
		state.Version++
		state.ChangeSeq++
		outcome = sessionmemory.CanonicalMutationOutcome{ScopeVersion: state.Version, ChangeSeq: state.ChangeSeq}
		if err := outcome.Validate(); err != nil {
			return err
		}
		changeKey, keyErr := badgerScopeChangeKey(request.Scope, state.ChangeSeq)
		if keyErr != nil {
			return keyErr
		}
		change := sessionmemory.ScopeChange{Sequence: state.ChangeSeq, OperationID: request.OperationID, OccurredAt: request.CommittedAt}
		if err := putBadgerSessionMemoryRecord(txn, changeKey, badgerRecordChange, change); err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, scopeKey, badgerRecordScope, state); err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, operationKey, badgerRecordOperation, badgerCanonicalOperation{Fingerprint: request.Fingerprint, Outcome: outcome}); err != nil {
			return err
		}
		if s.beforeCanonicalMutationCommit != nil {
			return s.beforeCanonicalMutationCommit()
		}
		return nil
	})
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, badgerSessionMemoryError("commit canonical operation", err)
	}
	return outcome, nil
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

type badgerProjectionActive struct {
	GenerationID string `json:"generation_id"`
}

type badgerProjectionRetention struct {
	Floor uint64 `json:"floor"`
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
	if s == nil {
		return sessionmemory.ScopeState{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.ScopeState{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
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
	if s == nil {
		return sessionmemory.CanonicalMutationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.CanonicalMutationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
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
		if err := putBadgerSessionMemoryRecord(txn, operationKey, badgerRecordOperation, badgerCanonicalOperation{Fingerprint: mutation.Operation.Fingerprint, Outcome: outcome}); err != nil {
			return err
		}
		if s.beforeCanonicalMutationCommit != nil {
			return s.beforeCanonicalMutationCommit()
		}
		return nil
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
	for _, payload := range mutation.Payloads {
		key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, payload.Ref.ID)
		if err != nil {
			return err
		}
		if err := putBadgerSessionMemoryImmutableRecord(txn, key, badgerRecordPayload, payload.Data); err != nil {
			return err
		}
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

func (s *BadgerSessionMemoryStore) LoadCanonicalRevisions(ctx context.Context, request sessionmemory.CanonicalRevisionReadRequest) ([]sessionmemory.MemoryRevision, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	revisions := make([]sessionmemory.MemoryRevision, 0, len(request.RevisionIDs))
	err := s.db.View(func(txn *badger.Txn) error {
		for _, revisionID := range request.RevisionIDs {
			key, err := badgerSessionMemoryKey(request.Scope, badgerRecordRevision, revisionID)
			if err != nil {
				return err
			}
			var revision sessionmemory.MemoryRevision
			if err := getBadgerSessionMemoryRecord(txn, key, badgerRecordRevision, &revision); err != nil {
				if errors.Is(err, badger.ErrKeyNotFound) {
					return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "canonical revision is missing", nil)
				}
				return err
			}
			if err := revision.Validate(); err != nil {
				return err
			}
			revisions = append(revisions, revision)
		}
		return nil
	})
	if err != nil {
		return nil, badgerSessionMemoryError("load canonical revisions", err)
	}
	return revisions, nil
}

func (s *BadgerSessionMemoryStore) ScanActiveHeads(ctx context.Context, request sessionmemory.ActiveHeadScanRequest) ([]sessionmemory.ItemHead, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	prefix, err := badgerSessionMemoryPrefix(request.Scope, badgerRecordHead)
	if err != nil {
		return nil, err
	}
	heads := make([]sessionmemory.ItemHead, 0, request.Limit)
	err = s.db.View(func(txn *badger.Txn) error {
		options := badger.DefaultIteratorOptions
		options.Prefix = prefix
		iterator := txn.NewIterator(options)
		defer iterator.Close()
		start := prefix
		if request.AfterItemID != "" {
			start, err = badgerSessionMemoryKey(request.Scope, badgerRecordHead, request.AfterItemID)
			if err != nil {
				return err
			}
		}
		for iterator.Seek(start); iterator.ValidForPrefix(prefix) && uint32(len(heads)) < request.Limit; iterator.Next() {
			var head sessionmemory.ItemHead
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordHead, &head); err != nil {
				return err
			}
			if request.AfterItemID != "" && head.ItemID <= request.AfterItemID {
				continue
			}
			if err := head.Validate(); err != nil {
				return err
			}
			heads = append(heads, head)
		}
		return nil
	})
	if err != nil {
		return nil, badgerSessionMemoryError("scan canonical active heads", err)
	}
	return heads, nil
}

// ScanActiveMemory returns a bounded reconciler view without scanning historic
// revisions. Scope-level CAS on the subsequent mutation handles concurrent
// changes after this read.
func (s *BadgerSessionMemoryStore) ScanActiveMemory(ctx context.Context, request sessionmemory.ActiveMemoryScanRequest) ([]sessionmemory.ActiveCanonicalMemory, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	heads, err := s.ScanActiveHeads(ctx, sessionmemory.ActiveHeadScanRequest(request))
	if err != nil {
		return nil, err
	}
	active := make([]sessionmemory.ActiveCanonicalMemory, 0, len(heads))
	err = s.db.View(func(txn *badger.Txn) error {
		for _, head := range heads {
			itemKey, keyErr := badgerSessionMemoryKey(request.Scope, badgerRecordItem, head.ItemID)
			if keyErr != nil {
				return keyErr
			}
			revisionKey, keyErr := badgerSessionMemoryKey(request.Scope, badgerRecordRevision, head.RevisionID)
			if keyErr != nil {
				return keyErr
			}
			var item sessionmemory.MemoryItem
			if err := getBadgerSessionMemoryRecord(txn, itemKey, badgerRecordItem, &item); err != nil {
				return err
			}
			var revision sessionmemory.MemoryRevision
			if err := getBadgerSessionMemoryRecord(txn, revisionKey, badgerRecordRevision, &revision); err != nil {
				return err
			}
			if err := item.Validate(); err != nil {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored active canonical item is invalid", err)
			}
			if item.Scope != request.Scope || revision.ItemID != item.ItemID {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored active canonical memory is invalid", nil)
			}
			active = append(active, sessionmemory.ActiveCanonicalMemory{
				Item:       item,
				RevisionID: revision.RevisionID,
				Revision:   revision.Revision,
				Evidence:   append([]sessionmemory.EvidenceRef(nil), revision.Evidence...),
			})
		}
		return nil
	})
	if err != nil {
		return nil, badgerSessionMemoryError("scan active canonical memory", err)
	}
	return active, nil
}

func (s *BadgerSessionMemoryStore) LoadProjectionManifest(ctx context.Context, scope sessionmemory.Scope, projectionID, generationID string) (sessionmemory.ProjectionManifest, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ProjectionManifest{}, false, err
	}
	if err := scope.Validate(); err != nil || strings.TrimSpace(projectionID) == "" || strings.TrimSpace(generationID) == "" {
		return sessionmemory.ProjectionManifest{}, false, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "projection manifest lookup is invalid", err)
	}
	key, err := badgerProjectionManifestKey(scope, projectionID, generationID)
	if err != nil {
		return sessionmemory.ProjectionManifest{}, false, err
	}
	var manifest sessionmemory.ProjectionManifest
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionManifest, &manifest)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.ProjectionManifest{}, false, nil
	}
	if err != nil {
		return sessionmemory.ProjectionManifest{}, false, badgerSessionMemoryError("load projection manifest", err)
	}
	if err := manifest.Validate(); err != nil || manifest.Scope != scope || manifest.ProjectionID != projectionID || manifest.GenerationID != generationID {
		return sessionmemory.ProjectionManifest{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored projection manifest is invalid", err)
	}
	return manifest, true, nil
}

func (s *BadgerSessionMemoryStore) LoadActiveProjectionManifest(ctx context.Context, scope sessionmemory.Scope, projectionID string) (sessionmemory.ProjectionManifest, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ProjectionManifest{}, false, err
	}
	if err := scope.Validate(); err != nil || strings.TrimSpace(projectionID) == "" {
		return sessionmemory.ProjectionManifest{}, false, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "active projection lookup is invalid", err)
	}
	activeKey, err := badgerSessionMemoryKey(scope, badgerRecordProjectionActive, projectionID)
	if err != nil {
		return sessionmemory.ProjectionManifest{}, false, err
	}
	var active badgerProjectionActive
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, activeKey, badgerRecordProjectionActive, &active)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.ProjectionManifest{}, false, nil
	}
	if err != nil {
		return sessionmemory.ProjectionManifest{}, false, badgerSessionMemoryError("load active projection", err)
	}
	manifest, found, err := s.LoadProjectionManifest(ctx, scope, projectionID, active.GenerationID)
	if err != nil || !found || manifest.Status != sessionmemory.ProjectionGenerationActive {
		return sessionmemory.ProjectionManifest{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "active projection manifest is invalid", err)
	}
	return manifest, true, nil
}

func (s *BadgerSessionMemoryStore) MarkProjectionDirty(ctx context.Context, manifest sessionmemory.ProjectionManifest) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	manifest.Status = sessionmemory.ProjectionGenerationDirty
	key, err := badgerProjectionManifestKey(manifest.Scope, manifest.ProjectionID, manifest.GenerationID)
	if err != nil {
		return err
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return putBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionManifest, manifest)
	}); err != nil {
		return badgerSessionMemoryError("mark projection generation dirty", err)
	}
	return nil
}

func (s *BadgerSessionMemoryStore) AdvanceProjectionWatermark(ctx context.Context, manifest sessionmemory.ProjectionManifest, watermark uint64, updatedAt time.Time) error {
	return s.updateProjectionManifest(ctx, manifest, updatedAt, func(stored *sessionmemory.ProjectionManifest) error {
		if stored.Status != sessionmemory.ProjectionGenerationDirty || watermark < stored.Watermark {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "projection watermark cannot advance", nil)
		}
		stored.Watermark = watermark
		return nil
	})
}

func (s *BadgerSessionMemoryStore) ActivateProjectionGeneration(ctx context.Context, manifest sessionmemory.ProjectionManifest, updatedAt time.Time) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil || updatedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "projection activation is invalid", err)
	}
	manifestKey, err := badgerProjectionManifestKey(manifest.Scope, manifest.ProjectionID, manifest.GenerationID)
	if err != nil {
		return err
	}
	activeKey, err := badgerSessionMemoryKey(manifest.Scope, badgerRecordProjectionActive, manifest.ProjectionID)
	if err != nil {
		return err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var stored sessionmemory.ProjectionManifest
		if err := getBadgerSessionMemoryRecord(txn, manifestKey, badgerRecordProjectionManifest, &stored); err != nil {
			return err
		}
		if stored.Status != sessionmemory.ProjectionGenerationDirty {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "projection generation is not dirty", nil)
		}
		var previous badgerProjectionActive
		if err := getBadgerSessionMemoryRecord(txn, activeKey, badgerRecordProjectionActive, &previous); err == nil && previous.GenerationID != stored.GenerationID {
			previousKey, keyErr := badgerProjectionManifestKey(manifest.Scope, manifest.ProjectionID, previous.GenerationID)
			if keyErr != nil {
				return keyErr
			}
			var old sessionmemory.ProjectionManifest
			if err := getBadgerSessionMemoryRecord(txn, previousKey, badgerRecordProjectionManifest, &old); err != nil {
				return err
			}
			old.Status = sessionmemory.ProjectionGenerationSuperseded
			old.UpdatedAt = updatedAt.UTC()
			if err := putBadgerSessionMemoryRecord(txn, previousKey, badgerRecordProjectionManifest, old); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		stored.Status = sessionmemory.ProjectionGenerationActive
		stored.UpdatedAt = updatedAt.UTC()
		if err := putBadgerSessionMemoryRecord(txn, manifestKey, badgerRecordProjectionManifest, stored); err != nil {
			return err
		}
		return putBadgerSessionMemoryRecord(txn, activeKey, badgerRecordProjectionActive, badgerProjectionActive{GenerationID: stored.GenerationID})
	})
	if err != nil {
		return badgerSessionMemoryError("activate projection generation", err)
	}
	return nil
}

func (s *BadgerSessionMemoryStore) updateProjectionManifest(ctx context.Context, manifest sessionmemory.ProjectionManifest, updatedAt time.Time, update func(*sessionmemory.ProjectionManifest) error) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil || updatedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "projection manifest update is invalid", err)
	}
	key, err := badgerProjectionManifestKey(manifest.Scope, manifest.ProjectionID, manifest.GenerationID)
	if err != nil {
		return err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var stored sessionmemory.ProjectionManifest
		if err := getBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionManifest, &stored); err != nil {
			return err
		}
		if stored.Scope != manifest.Scope || stored.ProjectionID != manifest.ProjectionID || stored.GenerationID != manifest.GenerationID {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "projection generation changed", nil)
		}
		if err := update(&stored); err != nil {
			return err
		}
		stored.UpdatedAt = updatedAt.UTC()
		return putBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionManifest, stored)
	})
	if err != nil {
		return badgerSessionMemoryError("update projection manifest", err)
	}
	return nil
}

// LoadProjectionRetentionFloor returns the minimum watermark that remains safe
// for incremental replay after retention removed older change records. A
// missing floor means that all canonical changes are still available and is
// represented as zero.
func (s *BadgerSessionMemoryStore) LoadProjectionRetentionFloor(ctx context.Context, scope sessionmemory.Scope, projectionID string) (uint64, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return 0, err
	}
	if err := scope.Validate(); err != nil || strings.TrimSpace(projectionID) == "" {
		return 0, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "projection retention lookup is invalid", err)
	}
	key, err := badgerProjectionRetentionKey(scope, projectionID)
	if err != nil {
		return 0, err
	}
	var retention badgerProjectionRetention
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionRetention, &retention)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, badgerSessionMemoryError("load projection retention floor", err)
	}
	return retention.Floor, nil
}

// SetProjectionRetentionFloor records a verified maintenance cursor. The
// floor is monotonic and is intentionally separate from disposable index
// files, so a projector can detect when incremental replay is no longer safe.
func (s *BadgerSessionMemoryStore) SetProjectionRetentionFloor(ctx context.Context, scope sessionmemory.Scope, projectionID string, floor uint64) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil || strings.TrimSpace(projectionID) == "" {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "projection retention update is invalid", err)
	}
	key, err := badgerProjectionRetentionKey(scope, projectionID)
	if err != nil {
		return err
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var current badgerProjectionRetention
		err := getBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionRetention, &current)
		if err == nil && floor < current.Floor {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "projection retention floor moved backwards", nil)
		}
		if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		return putBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionRetention, badgerProjectionRetention{Floor: floor})
	})
	if err != nil {
		return badgerSessionMemoryError("set projection retention floor", err)
	}
	return nil
}

// LoadScopeCheckpoint loads the latest cursor for one trigger kind.
func (s *BadgerSessionMemoryStore) LoadScopeCheckpoint(ctx context.Context, scope sessionmemory.Scope, kind sessionmemory.ScopeCheckpointKind) (sessionmemory.ScopeCheckpoint, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ScopeCheckpoint{}, false, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.ScopeCheckpoint{}, false, err
	}
	if err := kind.Validate(); err != nil {
		return sessionmemory.ScopeCheckpoint{}, false, err
	}
	key, err := badgerProjectionCheckpointKey(scope, kind)
	if err != nil {
		return sessionmemory.ScopeCheckpoint{}, false, err
	}
	var checkpoint sessionmemory.ScopeCheckpoint
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionCheckpoint, &checkpoint)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.ScopeCheckpoint{}, false, nil
	}
	if err != nil {
		return sessionmemory.ScopeCheckpoint{}, false, badgerSessionMemoryError("load scope checkpoint", err)
	}
	if err := checkpoint.Validate(); err != nil || checkpoint.Scope != scope || checkpoint.Kind != kind {
		return sessionmemory.ScopeCheckpoint{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored scope checkpoint is invalid", err)
	}
	return checkpoint, true, nil
}

// SaveScopeCheckpoint persists one latest-per-kind cursor and rejects stale
// writes. The maintenance mutex serializes this update with canonical
// lifecycle work in the same Badger owner.
func (s *BadgerSessionMemoryStore) SaveScopeCheckpoint(ctx context.Context, checkpoint sessionmemory.ScopeCheckpoint) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	key, err := badgerProjectionCheckpointKey(checkpoint.Scope, checkpoint.Kind)
	if err != nil {
		return err
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var current sessionmemory.ScopeCheckpoint
		err := getBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionCheckpoint, &current)
		if err == nil {
			if current.ScopeVersion > checkpoint.ScopeVersion || current.ChangeSeq > checkpoint.ChangeSeq || current.OccurredAt.After(checkpoint.OccurredAt) {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "scope checkpoint moved backwards", nil)
			}
			if current.ScopeVersion == checkpoint.ScopeVersion && current.ChangeSeq == checkpoint.ChangeSeq && current.CheckpointID == checkpoint.CheckpointID {
				return nil
			}
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		return putBadgerSessionMemoryRecord(txn, key, badgerRecordProjectionCheckpoint, checkpoint)
	})
	if err != nil {
		return badgerSessionMemoryError("save scope checkpoint", err)
	}
	return nil
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
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix) && uint32(len(claimed)) < request.Limit; iterator.Next() {
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

// PutPayload persists payload bytes outside structural canonical records.
func (s *BadgerSessionMemoryStore) PutPayload(ctx context.Context, data []byte, ref sessionmemory.PayloadRef) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if !isBadgerPayloadValid(data, ref) {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "payload is invalid", nil)
	}
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, ref.ID)
	if err != nil {
		return err
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return putBadgerSessionMemoryImmutableRecord(txn, key, badgerRecordPayload, data)
	}); err != nil {
		return badgerSessionMemoryError("store payload", err)
	}
	return nil
}

// LoadPayload reads a content-free reference's payload bytes.
func (s *BadgerSessionMemoryStore) LoadPayload(ctx context.Context, ref sessionmemory.PayloadRef) ([]byte, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, ref.ID)
	if err != nil {
		return nil, err
	}
	var data []byte
	if err := s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordPayload, &data)
	}); err != nil {
		return nil, badgerSessionMemoryError("load payload", err)
	}
	if !isBadgerPayloadValid(data, ref) {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored payload is invalid", nil)
	}
	return data, nil
}

// DeletePayload removes a payload blob after its logical forget state has
// committed. Structural tombstones remain intact.
func (s *BadgerSessionMemoryStore) DeletePayload(ctx context.Context, ref sessionmemory.PayloadRef) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerSessionMemoryKey(sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}, badgerRecordPayload, ref.ID)
	if err != nil {
		return err
	}
	if err := s.db.Update(func(txn *badger.Txn) error { return txn.Delete(key) }); err != nil {
		return badgerSessionMemoryError("delete payload", err)
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
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
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
	var denied badgerDeniedSource
	return s.isDenied(ctx, scope, sourceID, badgerRecordDeniedSource, "read canonical source deny", &denied)
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
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
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
	var denied badgerDeniedRevision
	return s.isDenied(ctx, scope, revisionID, badgerRecordDeniedRevision, "read canonical revision deny", &denied)
}

func (s *BadgerSessionMemoryStore) isDenied(ctx context.Context, scope sessionmemory.Scope, id, recordType, operation string, denied any) (bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return false, err
	}
	if s == nil {
		return false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerSessionMemoryKey(scope, recordType, id)
	if err != nil {
		return false, err
	}
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, recordType, denied)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, badgerSessionMemoryError(operation, err)
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
	if s == nil {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
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
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix) && uint32(len(results)) < limit; iterator.Next() {
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

func isBadgerPayloadValid(data []byte, ref sessionmemory.PayloadRef) bool {
	if ref.Validate() != nil || len(data) == 0 || len(data) != int(ref.ByteSize) {
		return false
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) == ref.Digest
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
	return openWithConfig(directory, DefaultConfig())
}

func openWithConfig(directory string, config Config) (*BadgerSessionMemoryStore, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("badger session-memory directory is required")
	}
	if !config.SyncWrites {
		config.SyncWrites = true
	}
	if !config.DetectConflicts {
		config.DetectConflicts = true
	}
	if config.NumVersionsToKeep <= 0 {
		config.NumVersionsToKeep = 1
	}
	options := badger.DefaultOptions(directory).
		WithSyncWrites(config.SyncWrites).
		WithDetectConflicts(config.DetectConflicts).
		WithNumVersionsToKeep(config.NumVersionsToKeep)
	db, err := badger.Open(options)
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
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if err := s.db.Sync(); err != nil {
		return fmt.Errorf("sync canonical badger session-memory store: %w", err)
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close canonical badger session-memory store: %w", err)
	}
	s.db = nil
	return nil
}
