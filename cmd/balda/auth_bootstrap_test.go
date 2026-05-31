package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/paths"
)

const testOwnerTokenPersisted = "owner-token-persisted"

func TestLoadOrCreateBaldaOwnerToken_GeneratesAndReuses(t *testing.T) {
	dbPath := paths.StateDBPath(t.TempDir())

	previousGenerator := baldaGenerateOwnerToken
	defer func() { baldaGenerateOwnerToken = previousGenerator }()

	generateCalls := 0
	baldaGenerateOwnerToken = func() (string, error) {
		generateCalls++
		return testOwnerTokenPersisted, nil
	}

	first, err := loadOrCreateBaldaOwnerToken(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("loadOrCreateBaldaOwnerToken(first): %v", err)
	}
	if first != testOwnerTokenPersisted {
		t.Fatalf("first token = %q, want %q", first, testOwnerTokenPersisted)
	}
	if generateCalls != 1 {
		t.Fatalf("generate calls after first = %d, want 1", generateCalls)
	}

	second, err := loadOrCreateBaldaOwnerToken(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("loadOrCreateBaldaOwnerToken(second): %v", err)
	}
	if second != testOwnerTokenPersisted {
		t.Fatalf("second token = %q, want %q", second, testOwnerTokenPersisted)
	}
	if generateCalls != 1 {
		t.Fatalf("generate calls after second = %d, want 1", generateCalls)
	}
}

func TestResolveStateDir(t *testing.T) {
	workingDir := t.TempDir()

	resolved, err := paths.ResolveStateDir(workingDir, ".config/balda")
	if err != nil {
		t.Fatalf("paths.ResolveStateDir: %v", err)
	}

	want := filepath.Join(workingDir, ".config", "balda")
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}
