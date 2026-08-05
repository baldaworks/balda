package state

import (
	"encoding/json"
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
