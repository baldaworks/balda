package state

import (
	"context"
	"errors"
	"strconv"

	"github.com/dgraph-io/badger/v4"
	"github.com/normahq/balda/sessionmemory"
)

var _ sessionmemory.CanonicalMigrationCheckpointStore = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.CanonicalMigrationReadinessStore = (*BadgerSessionMemoryStore)(nil)

// LoadCanonicalMigrationReadiness reads the durable exact-scope cutover gate.
func (s *BadgerSessionMemoryStore) LoadCanonicalMigrationReadiness(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.CanonicalMigrationReadiness, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.CanonicalMigrationReadiness{}, false, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.CanonicalMigrationReadiness{}, false, err
	}
	if s == nil {
		return sessionmemory.CanonicalMigrationReadiness{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.CanonicalMigrationReadiness{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerMigrationReadyKey(scope)
	if err != nil {
		return sessionmemory.CanonicalMigrationReadiness{}, false, err
	}
	var readiness sessionmemory.CanonicalMigrationReadiness
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordMigrationReady, &readiness)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.CanonicalMigrationReadiness{}, false, nil
	}
	if err != nil {
		return sessionmemory.CanonicalMigrationReadiness{}, false, badgerSessionMemoryError("load canonical migration readiness", err)
	}
	if err := readiness.Validate(); err != nil || readiness.Scope != scope {
		return sessionmemory.CanonicalMigrationReadiness{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical migration readiness is invalid", err)
	}
	return readiness, true, nil
}

// SaveCanonicalMigrationReadiness durably advertises a fully migrated scope.
func (s *BadgerSessionMemoryStore) SaveCanonicalMigrationReadiness(ctx context.Context, readiness sessionmemory.CanonicalMigrationReadiness) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := readiness.Validate(); err != nil {
		return err
	}
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerMigrationReadyKey(readiness.Scope)
	if err != nil {
		return err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var existing sessionmemory.CanonicalMigrationReadiness
		readErr := getBadgerSessionMemoryRecord(txn, key, badgerRecordMigrationReady, &existing)
		if readErr == nil {
			if err := existing.Validate(); err != nil || existing.Scope != readiness.Scope {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical migration readiness is invalid", err)
			}
			if existing.SnapshotVersion > readiness.SnapshotVersion {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical migration readiness moved backwards", nil)
			}
			if existing.SnapshotVersion == readiness.SnapshotVersion {
				return nil
			}
		}
		if readErr != nil && !errors.Is(readErr, badger.ErrKeyNotFound) {
			return readErr
		}
		return putBadgerSessionMemoryRecord(txn, key, badgerRecordMigrationReady, readiness)
	})
	if err != nil {
		return badgerSessionMemoryError("save canonical migration readiness", err)
	}
	return nil
}

// LoadCanonicalMigrationCheckpoint returns the last cursor durably recorded
// for one exact-scope v1 snapshot. A missing cursor is the clean-start state.
func (s *BadgerSessionMemoryStore) LoadCanonicalMigrationCheckpoint(ctx context.Context, scope sessionmemory.Scope, snapshotVersion uint64) (sessionmemory.CanonicalMigrationCheckpoint, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, err
	}
	key, err := badgerMigrationCheckpointKey(scope, strconv.FormatUint(snapshotVersion, 10))
	if err != nil {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, err
	}
	if s == nil {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	var checkpoint sessionmemory.CanonicalMigrationCheckpoint
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordMigrationCheckpoint, &checkpoint)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, nil
	}
	if err != nil {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, badgerSessionMemoryError("load canonical migration checkpoint", err)
	}
	if err := checkpoint.Validate(); err != nil || checkpoint.Scope != scope || checkpoint.SnapshotVersion != snapshotVersion {
		return sessionmemory.CanonicalMigrationCheckpoint{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical migration checkpoint is invalid", err)
	}
	return checkpoint, true, nil
}

// SaveCanonicalMigrationCheckpoint advances a cursor after the corresponding
// canonical mutation has committed. It is monotonic and idempotent, so a
// crash between mutation commit and this write safely replays the operation.
func (s *BadgerSessionMemoryStore) SaveCanonicalMigrationCheckpoint(ctx context.Context, checkpoint sessionmemory.CanonicalMigrationCheckpoint) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerMigrationCheckpointKey(checkpoint.Scope, strconv.FormatUint(checkpoint.SnapshotVersion, 10))
	if err != nil {
		return err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		var existing sessionmemory.CanonicalMigrationCheckpoint
		readErr := getBadgerSessionMemoryRecord(txn, key, badgerRecordMigrationCheckpoint, &existing)
		if readErr == nil {
			if err := existing.Validate(); err != nil {
				return err
			}
			if existing.Scope != checkpoint.Scope || existing.SnapshotVersion != checkpoint.SnapshotVersion || existing.SourceCount != checkpoint.SourceCount || existing.AtomCount != checkpoint.AtomCount || existing.ScenarioCount != checkpoint.ScenarioCount || existing.ProfileCount != checkpoint.ProfileCount || existing.OperationCount != checkpoint.OperationCount {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical migration checkpoint identity was reused", nil)
			}
			if existing.NextSourceOffset > checkpoint.NextSourceOffset || existing.NextAtomOffset > checkpoint.NextAtomOffset || existing.NextScenarioOffset > checkpoint.NextScenarioOffset || existing.NextProfileOffset > checkpoint.NextProfileOffset || existing.NextOperationOffset > checkpoint.NextOperationOffset {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical migration checkpoint moved backwards", nil)
			}
			if existing.NextSourceOffset == checkpoint.NextSourceOffset && existing.NextAtomOffset == checkpoint.NextAtomOffset && existing.NextScenarioOffset == checkpoint.NextScenarioOffset && existing.NextProfileOffset == checkpoint.NextProfileOffset && existing.NextOperationOffset == checkpoint.NextOperationOffset && existing.Completed == checkpoint.Completed {
				return nil
			}
		} else if !errors.Is(readErr, badger.ErrKeyNotFound) {
			return readErr
		}
		return putBadgerSessionMemoryRecord(txn, key, badgerRecordMigrationCheckpoint, checkpoint)
	})
	if err != nil {
		return badgerSessionMemoryError("save canonical migration checkpoint", err)
	}
	return nil
}
