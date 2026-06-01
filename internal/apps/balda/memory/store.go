package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	MemoryFileName = "MEMORY.md"

	MemoryStateKey = "balda_memory"
)

type Store struct {
	stateDir      string
	memoryEnabled bool
	mu            sync.Mutex
}

func NewStore(stateDir string, memoryEnabled bool) *Store {
	return &Store{
		stateDir:      strings.TrimSpace(stateDir),
		memoryEnabled: memoryEnabled,
	}
}

func (s *Store) MemoryEnabled() bool {
	return s != nil && s.memoryEnabled
}

func (s *Store) ReadMemory(ctx context.Context) (string, error) {
	if s == nil || !s.memoryEnabled {
		return "", nil
	}
	return s.readFile(ctx, filepath.Join(s.stateDir, MemoryFileName))
}

func (s *Store) Remember(ctx context.Context, fact string) error {
	if s == nil {
		return fmt.Errorf("memory store is required")
	}
	if !s.memoryEnabled {
		return fmt.Errorf("memory is disabled")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return fmt.Errorf("fact is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
		return fmt.Errorf("create memory state dir: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(s.stateDir, MemoryFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open memory file: %w", err)
	}

	if info, statErr := file.Stat(); statErr == nil && info.Size() > 0 {
		if _, err := file.WriteString("\n"); err != nil {
			_ = file.Close()
			return fmt.Errorf("separate memory entry: %w", err)
		}
	}
	if _, err := file.WriteString(fact + "\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("append memory fact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close memory file: %w", err)
	}
	return nil
}

func (s *Store) readFile(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err == nil {
		return string(content), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read %s: %w", filepath.Base(path), err)
}
