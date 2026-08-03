package sessionmemoryapp

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
)

func TestNativeProviderProcessesAndSearchesDerivedMemory(t *testing.T) {
	invoker := &scriptedStructuredInvoker{output: []byte(`{"output":[{"category":"fact","text":"native memory","relation":"new"}]}`)}
	deriver, err := NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	provider, err := NewNativeProvider(sessionmemorytest.NewStore(), deriver, invoker)
	if err != nil {
		t.Fatalf("NewNativeProvider() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "native:personal:topic:1", Kind: sessionmemory.ScopeKindPersonal}
	turn, err := sessionmemory.NewTurn(scope, sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC), "remember", "native memory")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	if err := provider.SyncTurn(context.Background(), turn); err != nil {
		t.Fatalf("SyncTurn() error = %v", err)
	}
	response, err := provider.Search(context.Background(), sessionmemory.SearchRequest{Scope: scope, Session: turn.Session, Query: "native", Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Text != "native memory" || response.Results[0].ScopeKey != scope.Key {
		t.Fatalf("Search() = %#v", response)
	}
	if err := provider.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
