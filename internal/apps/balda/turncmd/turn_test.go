package turncmd

import (
	"testing"

	"github.com/baldaworks/go-actorlayer"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

func TestSessionTurnEnvelopeCarriesGeneratedDedupeKeyInPayload(t *testing.T) {
	t.Parallel()

	env, err := SessionTurnEnvelope(SessionTurnPayload{
		Text: "test",
		Locator: baldasession.SessionLocator{
			SessionID: "tg-1-0",
		},
	})
	if err != nil {
		t.Fatalf("SessionTurnEnvelope() error = %v", err)
	}
	if env.DedupeKey == "" {
		t.Fatal("envelope dedupe key is empty")
	}
	if env.DedupeKey != env.ID {
		t.Fatalf("generated dedupe key = %q, want envelope ID %q", env.DedupeKey, env.ID)
	}

	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.DedupeKey != env.DedupeKey {
		t.Fatalf("payload dedupe key = %q, want %q", payload.DedupeKey, env.DedupeKey)
	}
}

func TestSessionTurnEnvelopePreservesExplicitDedupeKey(t *testing.T) {
	t.Parallel()

	env, err := SessionTurnEnvelope(SessionTurnPayload{
		Text:      "test",
		DedupeKey: "transport-message-1",
		Locator: baldasession.SessionLocator{
			SessionID: "tg-1-0",
		},
	})
	if err != nil {
		t.Fatalf("SessionTurnEnvelope() error = %v", err)
	}
	if env.DedupeKey != "transport-message-1" {
		t.Fatalf("dedupe key = %q, want transport-message-1", env.DedupeKey)
	}
}
