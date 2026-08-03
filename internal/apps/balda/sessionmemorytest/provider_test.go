package sessionmemorytest

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestProviderRecordsCallsAndUsesCallbacks(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "tg-1-0", AgentSessionID: "tg-1-0"}
	turn, err := sessionmemory.NewTurn(scope, session, "turn-1", time.Now().UTC(), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	boundary, err := sessionmemory.NewBoundary(scope, session, "close-1", sessionmemory.BoundaryReasonClose, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	req, err := sessionmemory.NormalizeSearchRequest(sessionmemory.SearchRequest{Scope: scope, Session: session, Query: "hello"})
	if err != nil {
		t.Fatalf("NormalizeSearchRequest() error = %v", err)
	}
	provider := &Provider{SearchFunc: func(_ context.Context, got sessionmemory.SearchRequest) (sessionmemory.SearchResponse, error) {
		return sessionmemory.SearchResponse{SchemaVersion: sessionmemory.SchemaVersionV1, Scope: got.Scope}, nil
	}}
	if err := provider.SyncTurn(context.Background(), turn); err != nil {
		t.Fatalf("SyncTurn() error = %v", err)
	}
	if err := provider.OnSessionBoundary(context.Background(), boundary); err != nil {
		t.Fatalf("OnSessionBoundary() error = %v", err)
	}
	if _, err := provider.Search(context.Background(), req); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if err := provider.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(provider.Turns()) != 1 || len(provider.Boundaries()) != 1 || len(provider.Searches()) != 1 || provider.CloseCalls() != 1 {
		t.Fatalf("recorded calls = turns:%d boundaries:%d searches:%d close:%d", len(provider.Turns()), len(provider.Boundaries()), len(provider.Searches()), provider.CloseCalls())
	}
	recorded := provider.Turns()
	recorded[0].Messages[0].Text = "mutated snapshot"
	if got := provider.Turns()[0].Messages[0].Text; got != "hello" {
		t.Fatalf("stored turn text = %q after snapshot mutation, want hello", got)
	}
}

var _ sessionmemory.Provider = (*Provider)(nil)
