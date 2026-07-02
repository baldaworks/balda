package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MemoryFileName = "MEMORY.md"

	MemoryStateKey        = "balda_memory"
	MemoryVersionStateKey = "balda_memory_version"

	kvMemoryKey = "memory/global"
)

type KVStore interface {
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
	SetJSON(ctx context.Context, key string, value any) error
}

type Snapshot struct {
	Content string
	Version int64
	Found   bool
}

type Update struct {
	Content string
	Version int64
	Found   bool
}

type Store struct {
	kv             KVStore
	legacyStateDir string
	memoryEnabled  bool
	mu             sync.Mutex
}

type record struct {
	Version   int64   `json:"version"`
	UpdatedAt string  `json:"updated_at"`
	Entries   []entry `json:"entries"`
}

type entry struct {
	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at"`
	Fact      string `json:"fact"`
}

func NewStore(kv KVStore, legacyStateDir string, memoryEnabled bool) *Store {
	return &Store{
		kv:             kv,
		legacyStateDir: strings.TrimSpace(legacyStateDir),
		memoryEnabled:  memoryEnabled,
	}
}

func (s *Store) MemoryEnabled() bool {
	return s != nil && s.memoryEnabled
}

func (s *Store) ReadMemory(ctx context.Context) (string, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return snapshot.Content, nil
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	if s == nil || !s.memoryEnabled {
		return Snapshot{}, nil
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.loadRecordLocked(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotFromRecord(rec), nil
}

func (s *Store) UpdatesSince(ctx context.Context, seenVersion int64) (Update, error) {
	if s == nil || !s.memoryEnabled {
		return Update{}, nil
	}
	if err := ctx.Err(); err != nil {
		return Update{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.loadRecordLocked(ctx)
	if err != nil {
		return Update{}, err
	}
	if rec.Version <= seenVersion {
		return Update{Version: rec.Version}, nil
	}

	facts := make([]string, 0, len(rec.Entries))
	for _, item := range rec.Entries {
		if item.Version > seenVersion {
			if fact := strings.TrimSpace(item.Fact); fact != "" {
				facts = append(facts, fact)
			}
		}
	}
	return Update{
		Content: strings.Join(facts, "\n\n"),
		Version: rec.Version,
		Found:   len(facts) > 0,
	}, nil
}

func (s *Store) Remember(ctx context.Context, fact string) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, fmt.Errorf("memory store is required")
	}
	if !s.memoryEnabled {
		return Snapshot{}, fmt.Errorf("memory is disabled")
	}
	if s.kv == nil {
		return Snapshot{}, fmt.Errorf("memory kv store is required")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return Snapshot{}, fmt.Errorf("fact is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.loadRecordLocked(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	nextVersion := rec.Version + 1
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec.Version = nextVersion
	rec.UpdatedAt = now
	rec.Entries = append(rec.Entries, entry{
		Version:   nextVersion,
		CreatedAt: now,
		Fact:      fact,
	})
	if err := s.saveRecordLocked(ctx, rec); err != nil {
		return Snapshot{}, err
	}
	return snapshotFromRecord(rec), nil
}

func VersionStateValue(version int64) string {
	if version <= 0 {
		return ""
	}
	return strconv.FormatInt(version, 10)
}

func VersionFromState(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		version, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return version
	default:
		return 0
	}
}

func (s *Store) loadRecordLocked(ctx context.Context) (record, error) {
	if s.kv == nil {
		return record{}, nil
	}
	value, ok, err := s.kv.GetJSON(ctx, kvMemoryKey)
	if err != nil {
		return record{}, fmt.Errorf("read memory kv: %w", err)
	}
	if ok {
		rec, err := normalizeRecord(value)
		if err != nil {
			return record{}, err
		}
		return rec, nil
	}
	rec, imported, err := s.importLegacyRecord(ctx)
	if err != nil {
		return record{}, err
	}
	if imported {
		if err := s.saveRecordLocked(ctx, rec); err != nil {
			return record{}, err
		}
	}
	return rec, nil
}

func (s *Store) saveRecordLocked(ctx context.Context, rec record) error {
	if s.kv == nil {
		return fmt.Errorf("memory kv store is required")
	}
	if err := s.kv.SetJSON(ctx, kvMemoryKey, rec); err != nil {
		return fmt.Errorf("write memory kv: %w", err)
	}
	return nil
}

func (s *Store) importLegacyRecord(ctx context.Context) (record, bool, error) {
	if strings.TrimSpace(s.legacyStateDir) == "" {
		return record{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return record{}, false, err
	}
	content, err := os.ReadFile(filepath.Join(s.legacyStateDir, MemoryFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return record{}, false, nil
		}
		return record{}, false, fmt.Errorf("read legacy %s: %w", MemoryFileName, err)
	}
	fact := strings.TrimSpace(string(content))
	if fact == "" {
		return record{}, false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return record{
		Version:   1,
		UpdatedAt: now,
		Entries: []entry{{
			Version:   1,
			CreatedAt: now,
			Fact:      fact,
		}},
	}, true, nil
}

func normalizeRecord(value any) (record, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return record{}, fmt.Errorf("marshal memory record: %w", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return record{}, fmt.Errorf("decode memory record: %w", err)
	}
	if rec.Version < 0 {
		rec.Version = 0
	}
	return rec, nil
}

func snapshotFromRecord(rec record) Snapshot {
	facts := make([]string, 0, len(rec.Entries))
	for _, item := range rec.Entries {
		if fact := strings.TrimSpace(item.Fact); fact != "" {
			facts = append(facts, fact)
		}
	}
	content := strings.Join(facts, "\n\n")
	return Snapshot{
		Content: content,
		Version: rec.Version,
		Found:   content != "",
	}
}
