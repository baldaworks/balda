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
	provider := &Provider{}
	if err := provider.IngestTurn(context.Background(), turn); err != nil {
		t.Fatalf("IngestTurn() error = %v", err)
	}
	if err := provider.ApplyBoundary(context.Background(), boundary); err != nil {
		t.Fatalf("ApplyBoundary() error = %v", err)
	}
	if len(provider.Turns()) != 1 || len(provider.Boundaries()) != 1 {
		t.Fatalf("recorded calls = turns:%d boundaries:%d", len(provider.Turns()), len(provider.Boundaries()))
	}
	recorded := provider.Turns()
	recorded[0].Messages[0].Text = "mutated snapshot"
	if got := provider.Turns()[0].Messages[0].Text; got != "hello" {
		t.Fatalf("stored turn text = %q after snapshot mutation, want hello", got)
	}
}
