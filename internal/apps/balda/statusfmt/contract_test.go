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
			ProviderID:        "codex",
			ProviderType:      "openai",
			RequestedModel:    "gpt-6",
			HasRequestedModel: true,
			ActiveModel:       "gpt-6",
			ActiveModelKind:   "provider_observed",
			ReasoningEffort:   "high",
			ChannelType:       "telegram",
			SessionID:         "tg-1-2",
			SessionState:      "active",
			TurnActive:        true,
			TurnStartedAt:     startedAt,
			PendingTurns:      2,
			UserText:          "hello",
		},
	}
	if got, want := env.Type(), deliveryfmt.MessageType("balda.status.response.v3"); got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
	if env.Body.ProviderID != "codex" || env.Body.TurnStartedAt != startedAt || env.Body.UserText != "hello" {
		t.Fatalf("Body = %+v", env.Body)
	}
	legacy := deliveryfmt.StructuredEnvelope[Response]{Descriptor: LegacyResponseDescriptor}
	if got, want := legacy.Type(), deliveryfmt.MessageType("balda.status.response.v1"); got != want {
		t.Fatalf("legacy Type() = %q, want %q", got, want)
	}
	legacyV2 := deliveryfmt.StructuredEnvelope[Response]{Descriptor: LegacyV2ResponseDescriptor}
	if got, want := legacyV2.Type(), deliveryfmt.MessageType("balda.status.response.v2"); got != want {
		t.Fatalf("legacy v2 Type() = %q, want %q", got, want)
	}
}
