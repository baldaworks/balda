package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSnapshotReadsStateDirFiles(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, SoulFileName), []byte("instruction\n"), 0o600); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	got, err := NewStore(stateDir, true, true).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got.Memory != "fact" {
		t.Fatalf("Snapshot().Memory = %q, want fact", got.Memory)
	}
	if got.Soul != "instruction" {
		t.Fatalf("Snapshot().Soul = %q, want instruction", got.Soul)
	}
}

func TestStoreRememberAppendsMemory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	store := NewStore(stateDir, true, true)
	if err := store.Remember(context.Background(), "first fact"); err != nil {
		t.Fatalf("Remember(first) error = %v", err)
	}
	if err := store.Remember(context.Background(), "second fact"); err != nil {
		t.Fatalf("Remember(second) error = %v", err)
	}

	got, err := store.ReadMemory(context.Background())
	if err != nil {
		t.Fatalf("ReadMemory() error = %v", err)
	}
	want := "first fact\n\nsecond fact\n"
	if got != want {
		t.Fatalf("ReadMemory() = %q, want %q", got, want)
	}
}

func TestStoreDisabledIsEmpty(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	got, err := NewStore(stateDir, false, true).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got.Memory != "" || got.Soul != "" {
		t.Fatalf("Snapshot() = %#v, want empty", got)
	}
}
