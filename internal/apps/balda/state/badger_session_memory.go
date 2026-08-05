package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/normahq/balda/sessionmemory"
)

// BadgerSessionMemoryStore owns one canonical Badger directory. Store methods
// are added alongside the v2 canonical mutation contract; construction is kept
// separate so directory locking and durability defaults are testable now.
type BadgerSessionMemoryStore struct {
	db *badger.DB
}

var _ sessionmemory.CanonicalStore = (*BadgerSessionMemoryStore)(nil)

type badgerCanonicalOperation struct {
	Fingerprint string                                 `json:"fingerprint"`
	Outcome     sessionmemory.CanonicalMutationOutcome `json:"outcome"`
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
