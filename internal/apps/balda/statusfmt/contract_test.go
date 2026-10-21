package statusfmt

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestResponseDescriptorCarriesTypedStatusData(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.October, 21, 12, 30, 0, 0, time.UTC)
	env := deliveryfmt.StructuredEnvelope[Response]{
		Descriptor: ResponseDescriptor,
		Body: Response{
			ProviderID:      "codex",
			ProviderType:    "openai",
			Model:           "gpt-6",
			ReasoningEffort: "high",
			ChannelType:     "telegram",
			SessionID:       "tg-1-2",
			SessionState:    "active",
			TurnActive:      true,
			TurnStartedAt:   startedAt,
			PendingTurns:    2,
			UserText:        "hello",
		},
	}
	if got, want := env.Type(), deliveryfmt.MessageType("balda.status.response.v1"); got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
	if env.Body.ProviderID != "codex" || env.Body.TurnStartedAt != startedAt || env.Body.UserText != "hello" {
		t.Fatalf("Body = %+v", env.Body)
	}
}
