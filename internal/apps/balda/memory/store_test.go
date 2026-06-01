package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreReadsStateDirFiles(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	store := NewStore(stateDir, true)
	gotMemory, err := store.ReadMemory(context.Background())
	if err != nil {
		t.Fatalf("ReadMemory() error = %v", err)
	}
	if strings.TrimSpace(gotMemory) != "fact" {
		t.Fatalf("ReadMemory() = %q, want fact", gotMemory)
	}
}

func TestStoreRememberAppendsMemory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	store := NewStore(stateDir, true)
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

func TestStoreMemoryDisabledIsEmpty(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	store := NewStore(stateDir, true)
	gotMemory, err := store.ReadMemory(context.Background())
	if err != nil {
		t.Fatalf("ReadMemory() error = %v", err)
	}
	if strings.TrimSpace(gotMemory) != "fact" {
		t.Fatalf("ReadMemory() = %q, want fact", gotMemory)
	}
}

func TestStoreMemoryDisabledDoesNotReadMemory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	store := NewStore(stateDir, false)
	gotMemory, err := store.ReadMemory(context.Background())
	if err != nil {
		t.Fatalf("ReadMemory() error = %v", err)
	}
	if strings.TrimSpace(gotMemory) != "" {
		t.Fatalf("ReadMemory() = %q, want empty when disabled", gotMemory)
	}
	if err := store.Remember(context.Background(), "new fact"); err == nil {
		t.Fatal("Remember() error = nil, want disabled error")
	}
}
