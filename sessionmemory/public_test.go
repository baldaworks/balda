package sessionmemory_test

import (
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestPublicPackageBuildsPortableTurn(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{
		Key:  "telegram:1:0",
		Kind: sessionmemory.ScopeKindPersonal,
	}
	session := sessionmemory.SessionRef{
		SessionID:      "session-1",
		AgentSessionID: "agent-session-1",
	}

	turn, err := sessionmemory.NewTurn(
		scope,
		session,
		"turn-1",
		time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC),
		"user message",
		"assistant message",
	)
	if err != nil {
		t.Fatalf("sessionmemory.NewTurn() error = %v", err)
	}
	if err := turn.Validate(); err != nil {
		t.Fatalf("Turn.Validate() error = %v", err)
	}
}
