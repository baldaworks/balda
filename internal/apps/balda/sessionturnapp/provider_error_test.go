package sessionturnapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/go-adk-acpagent/v2/acperror"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestProviderErrorTurnMessageWithoutOriginalUsesCategory(t *testing.T) {
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
			message := providerErrorTurnMessage(&tt.err, now)
			if !strings.Contains(message, tt.want) {
				t.Fatalf("incorrect category guidance %q", message)
			}
		})
	}
}

func TestTypedProviderFailureDeliversPartialWithInterruptionAndFailsTurn(t *testing.T) {
	dispatcher := &captureDispatcher{}
	capture := &captureHook{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns, capture)
	service.structuredRegistry = &testTurnFailureRegistry{}
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
		!strings.Contains(delivery.Text, "credential=SYNTHETIC_SECRET raw provider failure") || delivery.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("incomplete literal delivery: %q", delivery.Text)
	}
	if len(capture.turns) != 1 || capture.turns[0].TerminalStatus != TerminalStatusFailed {
		t.Fatalf("captured turns = %+v, want failed", capture.turns)
	}
}

// testTurnFailureRegistry exercises the consuming seam without transport imports.
type testTurnFailureRegistry struct {
	calls  int
	prefix string
	format deliveryfmt.DeliveryFormat
	err    error
}

func (r *testTurnFailureRegistry) RenderStructured(_ context.Context, _ string, typ deliveryfmt.MessageType, env any) (deliveryfmt.StructuredPresentation, error) {
	r.calls++
	typed, ok := env.(deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure])
	if !ok || typ != deliveryfmt.TurnFailureDescriptor.Type {
		return deliveryfmt.StructuredPresentation{}, errors.New("unexpected failure descriptor")
	}
	if r.err != nil {
		return deliveryfmt.StructuredPresentation{}, r.err
	}
	format := r.format
	if format == "" {
		format = deliveryfmt.DeliveryFormatNone
	}
	return deliveryfmt.StructuredPresentation{Text: r.prefix + typed.Body.Text, DeliveryFormat: format}, nil
}

func TestTerminalErrorPreservesOriginalAndClassifiesStructuredOverload(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tt := range []struct {
		name, code, message, want string
		typed                     *acperror.ProviderError
	}{
		{name: "extension overload", code: fixtureOverloadCode, message: "  Модель <busy> & **retry**\nnext line  ", want: "Provider error:\n  Модель <busy> & **retry**\nnext line  \n\nThe selected model is overloaded. Try again later or choose another model."},
		{name: "independent event wins", code: "acp_request_error:-32603", message: "independent RPC text", typed: &acperror.ProviderError{Kind: acperror.KindUnavailable, Code: fixtureOverloadCode, Message: "nested text"}, want: "Provider error:\nindependent RPC text\n\nThe selected model is overloaded. Try again later or choose another model."},
		{name: "metadata message wins synthesized text", code: "acp_provider_error:unavailable", message: "synthesized error dump", typed: &acperror.ProviderError{Kind: acperror.KindUnavailable, Message: "original metadata text"}, want: "Provider error:\noriginal metadata text"},
		{name: "typed fallback", typed: &acperror.ProviderError{Kind: acperror.KindUnavailable, Message: "metadata fallback"}, want: "Provider error:\nmetadata fallback"},
		{name: "ordinary original", code: "otherCode", message: "serverOverloaded words are just text", want: "Provider error:\nserverOverloaded words are just text"},
		{name: "empty overload", code: fixtureOverloadCode, want: "The selected model is overloaded. Try again later or choose another model."},
		{name: "empty unknown", code: "unknown", want: "The provider could not complete this turn. Please try again."},
		{name: "empty typed overload", typed: &acperror.ProviderError{Kind: acperror.KindUnavailable, Code: fixtureOverloadCode}, want: "The selected model is overloaded. Try again later or choose another model."},
		{name: "exact code only", code: " serverOverloaded ", message: "original", want: "Provider error:\noriginal"},
		{name: "synthesized no message", code: "acp_provider_error:unavailable", message: "synthesized dump", typed: &acperror.ProviderError{Kind: acperror.KindUnavailable}, want: "The provider is temporarily unavailable. Please try again."},
		{name: "unavailable is not overload", typed: &acperror.ProviderError{Kind: acperror.KindUnavailable}, want: "The provider is temporarily unavailable. Please try again."},
		{name: "no error", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalErrorTurnMessage(tt.code, tt.message, tt.typed, now)
			if got != tt.want {
				t.Fatalf("terminal message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderFailureLiteralFinalWithToolOrVisibleOutput(t *testing.T) {
	for _, output := range []string{"none", "visible", "function", "execution"} {
		t.Run(output, func(t *testing.T) {
			bus := &captureDispatcher{}
			capture := &captureHook{}
			service := NewTurnExecutionServiceWithJobEventsAndCapture(bus, nil, nil, zerolog.Nop(), 1, capture)
			registry := &testTurnFailureRegistry{prefix: "literal:", format: deliveryfmt.DeliveryFormatNone}
			service.structuredRegistry = registry
			runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
				partial := adksession.NewEvent(t.Context(), inv)
				switch output {
				case "visible":
					partial.Content = genai.NewContentFromText("**partial**", genai.RoleModel)
				case "function":
					partial.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "lookup", Response: map[string]any{"result": "tool body stays private"}}}}}
				case "execution":
					partial.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{CodeExecutionResult: &genai.CodeExecutionResult{Output: "tool body stays private"}}}}
				}
				done := adksession.NewEvent(t.Context(), inv)
				done.TurnComplete = true
				done.ErrorCode = fixtureOverloadCode
				done.ErrorMessage = "  raw <busy> & Модель\nnext line  "
				return []*adksession.Event{partial, done}
			})
			err := service.Execute(t.Context(), ExecutionRequest{Text: "input", Runner: runner, UserID: "tg-101", AgentSessionID: id, SessionID: "session", DedupeKey: "turn", Deliver: true, ProgressEmitter: &recordingScheduleProgress{}, TurnSource: turncmd.SourceTelegram, DeliveryOptions: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}, Locator: deliverycmd.Locator{SessionID: "session", ChannelType: "telegram", AddressKey: "101:0"}})
			var accepted *turncmd.FinalOutcomeAcceptedError
			if !errors.Is(err, errProviderTurnFailed) || !errors.As(err, &accepted) {
				t.Fatalf("Execute error=%v", err)
			}
			if len(bus.envelopes) != 1 || registry.calls != 1 {
				t.Fatalf("sends=%d renders=%d", len(bus.envelopes), registry.calls)
			}
			var delivered deliverycmd.Payload
			if err := actorlayer.UnmarshalPayload(bus.envelopes[0].Payload, &delivered); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(delivered.Text, "literal:") || !strings.Contains(delivered.Text, "  raw <busy> & Модель\nnext line  ") || !strings.Contains(delivered.Text, "choose another model") || delivered.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
				t.Fatalf("delivery=%+v", delivered)
			}
			if strings.Contains(delivered.Text, "Response interrupted.") != (output != "none") {
				t.Fatalf("wrong interruption notice: %q", delivered.Text)
			}
			if strings.Contains(delivered.Text, "tool body stays private") {
				t.Fatalf("tool content exposed: %q", delivered.Text)
			}
			if output == "visible" && !strings.Contains(delivered.Text, "**partial**") {
				t.Fatalf("partial reply missing: %q", delivered.Text)
			}
			if len(capture.turns) != 1 || capture.turns[0].TerminalStatus != TerminalStatusFailed || strings.Contains(capture.turns[0].AssistantText, "raw <busy>") {
				t.Fatalf("capture=%+v", capture.turns)
			}
		})
	}
}

func TestProviderFailureRendererErrorPreventsFinalPublication(t *testing.T) {
	bus := &captureDispatcher{}
	service := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	cause := errors.New("renderer unavailable")
	service.structuredRegistry = &testTurnFailureRegistry{err: cause}
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	service.SetFinalArchive(archive)
	runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
		done := adksession.NewEvent(t.Context(), inv)
		done.TurnComplete = true
		done.ErrorCode = fixtureOverloadCode
		done.ErrorMessage = "original"
		return []*adksession.Event{done}
	})
	err := service.Execute(t.Context(), ExecutionRequest{ArchiveID: "epoch", DedupeKey: "turn", Text: "input", Runner: runner, UserID: "tg-101", AgentSessionID: id, SessionID: "session", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: deliverycmd.Locator{SessionID: "session", ChannelType: "telegram", AddressKey: "101:0"}})
	var accepted *turncmd.FinalOutcomeAcceptedError
	if !errors.Is(err, cause) || errors.As(err, &accepted) || len(bus.envelopes) != 0 || archive.calls != 0 {
		t.Fatalf("error=%v sends=%d commits=%d", err, len(bus.envelopes), archive.calls)
	}
}
