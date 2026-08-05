package state

import (
	"path/filepath"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestBadgerSessionMemoryStoreOwnsOneWritableDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "memory.badger")
	first, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	if _, err := OpenBadgerSessionMemoryStore(directory); err == nil {
		t.Fatal("second writable Badger store opened")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
}

func TestBadgerSessionMemoryRecordCodec(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := []byte("record")
	if err := store.db.Update(func(txn *badger.Txn) error {
		return putBadgerSessionMemoryRecord(txn, key, "scope", map[string]string{"key": "value"})
	}); err != nil {
		t.Fatalf("write record error = %v", err)
	}
	var got map[string]string
	if err := store.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, "scope", &got)
	}); err != nil {
		t.Fatalf("read record error = %v", err)
	}
	if got["key"] != "value" {
		t.Fatalf("record = %#v", got)
	}
	if err := store.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, "operation", &got)
	}); err == nil {
		t.Fatal("wrong record type decoded")
	}
}

func TestBadgerSessionMemoryStoreRequiresDirectory(t *testing.T) {
	if _, err := OpenBadgerSessionMemoryStore(" "); err == nil {
		t.Fatal("empty directory opened")
	}
}
