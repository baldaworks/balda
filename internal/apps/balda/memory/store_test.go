package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	firstUpdatedAt := requireSnapshotUpdatedAt(t, first)
	second, err := store.Remember(context.Background(), "second fact")
	if err != nil {
		t.Fatalf("Remember(second) error = %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d, want 2", second.Version)
	}
	secondUpdatedAt := requireSnapshotUpdatedAt(t, second)
	if !secondUpdatedAt.After(firstUpdatedAt) {
		t.Fatalf("second updated_at = %v, want after %v", secondUpdatedAt, firstUpdatedAt)
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
	requireSnapshotUpdatedAt(t, snapshot)
}

func TestStoreDoesNotImportLegacyFileWhenKVExists(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, MemoryFileName), []byte("legacy fact\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	kv := newTestKV()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := kv.SetJSON(context.Background(), kvMemoryKey, record{
		Version:   1,
		UpdatedAt: now,
		Entries: []entry{{
			Version:   1,
			CreatedAt: now,
			Fact:      "kv fact",
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
	if snapshot.UpdatedAt != now {
		t.Fatalf("Snapshot() updated_at = %q, want %q", snapshot.UpdatedAt, now)
	}
}

func TestStoreSnapshotEmptyMemoryHasNoTimestamp(t *testing.T) {
	t.Parallel()

	snapshot, err := NewStore(newTestKV(), "", true).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Found || snapshot.Content != "" || snapshot.UpdatedAt != "" {
		t.Fatalf("Snapshot() = %+v, want empty snapshot without timestamp", snapshot)
	}
}

func TestStoreSnapshotRejectsInvalidTimestampWithoutExposingFact(t *testing.T) {
	t.Parallel()

	kv := newTestKV()
	const fact = "private fact"
	if err := kv.SetJSON(context.Background(), kvMemoryKey, record{
		Version:   1,
		UpdatedAt: "not-a-timestamp",
		Entries:   []entry{{Version: 1, Fact: fact}},
	}); err != nil {
		t.Fatalf("prepopulate kv: %v", err)
	}

	_, err := NewStore(kv, "", true).Snapshot(context.Background())
	if err == nil {
		t.Fatal("Snapshot() error = nil, want invalid timestamp error")
	}
	if strings.Contains(err.Error(), fact) {
		t.Fatalf("Snapshot() error = %q, want fact content omitted", err)
	}
}

func TestStoreSnapshotRemainsConsistentDuringRemember(t *testing.T) {
	t.Parallel()

	store := NewStore(newTestKV(), "", true)
	const writes = 20
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range writes {
			if _, err := store.Remember(context.Background(), "fact"); err != nil {
				t.Errorf("Remember() error = %v", err)
				return
			}
		}
	}()

	for {
		snapshot, err := store.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if snapshot.Found {
			requireSnapshotUpdatedAt(t, snapshot)
			if got := strings.Count(snapshot.Content, "fact"); int64(got) != snapshot.Version {
				t.Fatalf("Snapshot() fact count = %d, version = %d", got, snapshot.Version)
			}
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func requireSnapshotUpdatedAt(t *testing.T, snapshot Snapshot) time.Time {
	t.Helper()
	if snapshot.UpdatedAt == "" {
		t.Fatal("Snapshot() updated_at is empty")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, snapshot.UpdatedAt)
	if err != nil {
		t.Fatalf("Snapshot() updated_at = %q: %v", snapshot.UpdatedAt, err)
	}
	if updatedAt.Location() != time.UTC {
		t.Fatalf("Snapshot() updated_at location = %v, want UTC", updatedAt.Location())
	}
	return updatedAt
}
