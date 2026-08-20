package badger

import (
	"context"
	"io"

	"github.com/dgraph-io/badger/v4"
	"github.com/baldaworks/balda/sessionmemory"
)

const defaultBadgerRestorePendingWrites = 128

// CanonicalBackupWriter is the storage-neutral backup seam used by
// composition-root maintenance jobs. The bytes are Badger's application
// independent backup stream; callers must store them as an opaque artifact.
type CanonicalBackupWriter interface {
	WriteCanonicalBackup(ctx context.Context, destination io.Writer, since uint64) (uint64, error)
}

// CanonicalBackupReader restores one previously produced backup stream into an
// already opened, quiesced canonical store. Badger's transaction loader keeps
// the restore atomic per loaded batch and preserves its own record checksums.
type CanonicalBackupReader interface {
	RestoreCanonicalBackup(ctx context.Context, source io.Reader, maxPendingWrites int) error
}

// CanonicalIntegrityVerifier validates every stored canonical envelope without
// hydrating payload plaintext. It is intended for startup/restore diagnostics;
// malformed records fail closed and are never repaired automatically.
type CanonicalIntegrityVerifier interface {
	VerifyCanonicalIntegrity(ctx context.Context) error
}

var _ CanonicalBackupWriter = (*BadgerSessionMemoryStore)(nil)
var _ CanonicalBackupReader = (*BadgerSessionMemoryStore)(nil)
var _ CanonicalIntegrityVerifier = (*BadgerSessionMemoryStore)(nil)

// WriteCanonicalBackup writes a full or incremental Badger backup. The store
// lock serializes backup/restore operations with each other; application
// lifecycle code must quiesce mutation workers before restore as required by
// Badger's DB.Load contract.
func (s *BadgerSessionMemoryStore) WriteCanonicalBackup(ctx context.Context, destination io.Writer, since uint64) (uint64, error) {
	if err := validateCanonicalMaintenance(ctx, destination); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return 0, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	version, err := s.db.Backup(destination, since)
	if err != nil {
		return 0, badgerSessionMemoryError("write canonical backup", err)
	}
	return version, nil
}

// RestoreCanonicalBackup loads a Badger backup into the current store. A
// caller must stop canonical writers before invoking this method and should
// run VerifyCanonicalIntegrity before making the restored store available.
func (s *BadgerSessionMemoryStore) RestoreCanonicalBackup(ctx context.Context, source io.Reader, maxPendingWrites int) error {
	if err := validateCanonicalMaintenance(ctx, source); err != nil {
		return err
	}
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	if maxPendingWrites <= 0 {
		maxPendingWrites = defaultBadgerRestorePendingWrites
	}
	if maxPendingWrites > 4096 {
		return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical restore pending-write bound is too large", nil)
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	if err := s.db.Load(source, maxPendingWrites); err != nil {
		return badgerSessionMemoryError("restore canonical backup", err)
	}
	if err := checkCanonicalMaintenanceContext(ctx); err != nil {
		return err
	}
	return nil
}

// VerifyCanonicalIntegrity checks all application-owned record envelopes and
// their checksums. It does not attempt destructive repair or decrypt payloads.
func (s *BadgerSessionMemoryStore) VerifyCanonicalIntegrity(ctx context.Context) error {
	if err := checkCanonicalMaintenanceContext(ctx); err != nil {
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
	err := s.db.View(func(txn *badger.Txn) error {
		iterator := txn.NewIterator(badger.DefaultIteratorOptions)
		defer iterator.Close()
		for iterator.Rewind(); iterator.Valid(); iterator.Next() {
			if err := checkCanonicalMaintenanceContext(ctx); err != nil {
				return err
			}
			item := iterator.Item()
			encoded, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			if _, err := sessionmemory.UnmarshalRecordEnvelope(encoded); err != nil {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical record integrity check failed", err)
			}
		}
		return nil
	})
	if err != nil {
		if _, _, ok := sessionmemory.ClassifyError(err); ok {
			return err
		}
		return badgerSessionMemoryError("verify canonical integrity", err)
	}
	return nil
}

func validateCanonicalMaintenance(ctx context.Context, value any) error {
	if err := checkCanonicalMaintenanceContext(ctx); err != nil {
		return err
	}
	if value == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance stream is required", nil)
	}
	switch stream := value.(type) {
	case io.Reader:
		if stream == nil {
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance reader is required", nil)
		}
	case io.Writer:
		if stream == nil {
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance writer is required", nil)
		}
	default:
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance stream is invalid", nil)
	}
	return nil
}

func checkCanonicalMaintenanceContext(ctx context.Context) error {
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance context is required", nil)
	}
	select {
	case <-ctx.Done():
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "canonical maintenance context ended", ctx.Err())
	default:
		return nil
	}
}
