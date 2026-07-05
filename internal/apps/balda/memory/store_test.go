package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type testKV struct {
	mu     sync.Mutex
	values map[string]any
}

func newTestKV() *testKV {
	return &testKV{values: make(map[string]any)}
}

func (s *testKV) GetJSON(_ context.Context, key string) (any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[strings.TrimSpace(key)]
	return value, ok, nil
}

func (s *testKV) SetJSON(_ context.Context, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[strings.TrimSpace(key)] = value
	return nil
}

func TestStoreRememberAppendsMemoryInKV(t *testing.T) {
	t.Parallel()

	store := NewStore(newTestKV(), "", true)
	first, err := store.Remember(context.Background(), "first fact")
	if err != nil {
		t.Fatalf("Remember(first) error = %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}
	second, err := store.Remember(context.Background(), "second fact")
	if err != nil {
		t.Fatalf("Remember(second) error = %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d, want 2", second.Version)
	}

	got, err := store.ReadMemory(context.Background())
	if err != nil {
		t.Fatalf("ReadMemory() error = %v", err)
	}
	want := "first fact\n\nsecond fact"
	if got != want {
		t.Fatalf("ReadMemory() = %q, want %q", got, want)
	}
}

func TestStoreMemoryDisabledDoesNotReadMemory(t *testing.T) {
	t.Parallel()

	store := NewStore(newTestKV(), "", false)
	gotMemory, err := store.ReadMemory(context.Background())
	if err != nil {
		t.Fatalf("ReadMemory() error = %v", err)
	}
	if strings.TrimSpace(gotMemory) != "" {
		t.Fatalf("ReadMemory() = %q, want empty when disabled", gotMemory)
	}
	if _, err := store.Remember(context.Background(), "new fact"); err == nil {
		t.Fatal("Remember() error = nil, want disabled error")
	}
}

func TestStoreImportsLegacyMemoryFileWhenKVEmpty(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("legacy fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	store := NewStore(newTestKV(), stateDir, true)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Content != "legacy fact" {
		t.Fatalf("Snapshot() content = %q, want legacy fact", snapshot.Content)
	}
	if snapshot.Version != 1 {
		t.Fatalf("Snapshot() version = %d, want 1", snapshot.Version)
	}
}

func TestStoreDoesNotImportLegacyFileWhenKVExists(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("legacy fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	kv := newTestKV()
	if err := kv.SetJSON(context.Background(), kvMemoryKey, record{
		Version: 1,
		Entries: []entry{{
			Version: 1,
			Fact:    "kv fact",
		}},
	}); err != nil {
		t.Fatalf("prepopulate kv: %v", err)
	}
	store := NewStore(kv, stateDir, true)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Content != "kv fact" {
		t.Fatalf("Snapshot() content = %q, want kv fact", snapshot.Content)
	}
}
