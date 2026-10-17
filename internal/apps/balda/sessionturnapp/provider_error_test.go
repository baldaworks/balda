package sessionturnapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/go-adk-acpagent/v2/acperror"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestProviderErrorTurnMessageIsBoundedAndSecretSafe(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	retryAt := now.Add(time.Hour).Unix()
	tests := []struct {
		name string
		err  acperror.ProviderError
		want string
	}{
		{name: "quota", err: acperror.ProviderError{Kind: acperror.KindQuotaExceeded}, want: "quota is exhausted"},
		{name: "rate reset", err: acperror.ProviderError{Kind: acperror.KindRateLimited, RetryAt: &retryAt}, want: "Try again after"},
		{name: "authentication", err: acperror.ProviderError{Kind: acperror.KindAuthenticationRequired}, want: "needs authentication"},
		{name: "payment", err: acperror.ProviderError{Kind: acperror.KindPaymentRequired}, want: "billing limit"},
		{name: "unavailable", err: acperror.ProviderError{Kind: acperror.KindUnavailable}, want: "temporarily unavailable"},
		{name: "session", err: acperror.ProviderError{Kind: acperror.KindSessionExhausted}, want: "session reached its limit"},
		{name: "context", err: acperror.ProviderError{Kind: acperror.KindInvalidRequest, Code: "contextWindowExceeded"}, want: "request is too large"},
		{name: "sandbox", err: acperror.ProviderError{Kind: acperror.KindInvalidRequest, Code: "sandboxError"}, want: "sandbox"},
		{name: "invalid", err: acperror.ProviderError{Kind: acperror.KindInvalidRequest, Code: "futureCode"}, want: "rejected this request"},
		{name: "unknown", err: acperror.ProviderError{Kind: acperror.KindUnknown}, want: "could not complete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.err.Message = "credential=SYNTHETIC_SECRET request body"
			message := providerErrorTurnMessage(&tt.err, now)
			if !strings.Contains(message, tt.want) || strings.Contains(message, "SYNTHETIC_SECRET") || strings.Contains(message, tt.err.Message) {
				t.Fatalf("unsafe or incorrect message %q", message)
			}
		})
	}
	if got := terminalErrorTurnMessage("credential=SYNTHETIC_SECRET"); strings.Contains(got, "SYNTHETIC_SECRET") {
		t.Fatalf("legacy provider details leaked: %q", got)
	}
}

func TestTypedProviderFailureDeliversPartialWithInterruptionAndFailsTurn(t *testing.T) {
	dispatcher := &captureDispatcher{}
	capture := &captureHook{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns, capture)
	service.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	retryAt := service.now().Add(time.Hour).Unix()
	runner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		done := adksession.NewEvent(context.Background(), invocationID)
		done.TurnComplete = true
		done.Content = genai.NewContentFromText("partial answer", genai.RoleModel)
		done.ErrorCode = "acp_request_error:-32603"
		done.ErrorMessage = "credential=SYNTHETIC_SECRET raw provider failure"
		done.CustomMetadata = map[string]any{
			acperror.ProviderErrorMetadataKey: (&acperror.ProviderError{
				Kind: acperror.KindRateLimited, Scope: acperror.ScopeAccount,
				Code: "rateLimitExceeded", RetryAt: &retryAt,
				Message: "credential=SYNTHETIC_SECRET raw provider failure",
			}).Metadata(),
		}
		return []*adksession.Event{done}
	})

	err := service.Execute(t.Context(), ExecutionRequest{
		Text: "question", Runner: runner, UserID: "tg-101", AgentSessionID: agentSessionID,
		SessionID: "session", Deliver: true, TurnSource: turncmd.SourceTelegram,
		DedupeKey: "telegram:message:provider-error",
		Locator:   deliverycmd.Locator{SessionID: "session", ChannelType: "telegram", AddressKey: "101:0"},
	})
	var accepted *turncmd.FinalOutcomeAcceptedError
	if !errors.Is(err, errProviderTurnFailed) || !errors.As(err, &accepted) {
		t.Fatalf("Execute() error = %v, want accepted provider failure", err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(dispatcher.envelopes))
	}
	var delivery deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &delivery); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivery.Text, "partial answer") || !strings.Contains(delivery.Text, "Response interrupted") ||
		!strings.Contains(delivery.Text, "rate limit") || strings.Contains(delivery.Text, "SYNTHETIC_SECRET") {
		t.Fatalf("unsafe or incomplete delivery: %q", delivery.Text)
	}
	if len(capture.turns) != 1 || capture.turns[0].TerminalStatus != TerminalStatusFailed {
		t.Fatalf("captured turns = %+v, want failed", capture.turns)
	}
}
