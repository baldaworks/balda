package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
)

func TestSQLiteSessionMemoryStoreContract(t *testing.T) {
	sessionmemorytest.RunStoreContract(t, func() sessionmemory.Store {
		provider, err := NewSQLiteProvider(context.Background(), filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("NewSQLiteProvider() error = %v", err)
		}
		t.Cleanup(func() { _ = provider.Close() })
		return provider.SessionMemoryStore()
	})
}

func TestSQLiteSessionMemoryStorePersistsAcrossProviderRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	store := provider.SessionMemoryStore()
	scope := sessionmemory.Scope{Key: "restart:personal:topic:1", Kind: sessionmemory.ScopeKindPersonal}
	models := sessionmemorytest.NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{Category: sessionmemory.AtomCategoryFact, Text: "restart durable", Relation: sessionmemory.CandidateRelationNew}}, nil)
	engine, err := sessionmemory.NewEngine(store, models, models, models, sessionmemory.Config{})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	turn, err := sessionmemory.NewTurn(scope, sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", sessionmemoryTestTime(), "remember", "restart durable")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	if _, err := engine.ProcessTurn(ctx, turn); err != nil {
		t.Fatalf("ProcessTurn() error = %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	provider, err = NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()
	reopened := provider.SessionMemoryStore()
	snapshot, err := reopened.LoadScope(ctx, scope)
	if err != nil || len(snapshot.Sources) != 1 || len(snapshot.Atoms) != 1 {
		t.Fatalf("reopened snapshot = %#v, error = %v", snapshot, err)
	}
	search, err := reopened.Search(ctx, sessionmemory.DerivedSearchRequest{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Scope: scope, Query: "restart", Limit: 10})
	if err != nil || len(search) != 1 {
		t.Fatalf("reopened search = %#v, error = %v", search, err)
	}
}

func sessionmemoryTestTime() (result time.Time) {
	return time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
}
