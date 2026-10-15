package state

import (
	"path/filepath"
	"testing"
)

func TestWebhookTurnClaimSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "webhook-route-123"
	if _, err := provider.Jobs().CreateJob(t.Context(), JobRecord{
		ID: jobID, SessionID: "wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Objective: "input", Status: JobStatusCreated, PrivateRunKind: PrivateRunKindWebhook,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := provider.Jobs().ClaimWebhookTurn(t.Context(), jobID)
	if err != nil || !claimed {
		t.Fatalf("first claim = %t, %v", claimed, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	claimed, err = reopened.Jobs().ClaimWebhookTurn(t.Context(), jobID)
	if err != nil || claimed {
		t.Fatalf("replayed claim = %t, %v", claimed, err)
	}
}
