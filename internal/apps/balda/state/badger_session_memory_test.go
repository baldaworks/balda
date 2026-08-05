package state

import (
	"path/filepath"
	"testing"
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

func TestBadgerSessionMemoryStoreRequiresDirectory(t *testing.T) {
	if _, err := OpenBadgerSessionMemoryStore(" "); err == nil {
		t.Fatal("empty directory opened")
	}
}
