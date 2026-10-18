package sessionturnapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type turnHooks struct {
	events []hookcmd.Event
	deny   hookcmd.EventName
}

func TestPromptHookContextSurvivesFormatComposition(t *testing.T) {
	var input string
	runner, id := newFormatTestRunner(t, func(invocationID, text string) ([]*adksession.Event, error) {
		input = text
		event := adksession.NewEvent(context.Background(), invocationID)
		event.TurnComplete = true
		return []*adksession.Event{event}, nil
	})
	service := NewTurnExecutionServiceWithFormats(nil, nil, &formatTestState{}, zerolog.Nop(), 1, nil, newFormatTestRegistry(t))
	service.SetHooks(&turnHooks{})
	err := service.Execute(t.Context(), ExecutionRequest{Text: "question", Runner: runner, UserID: "telegram-user", SessionID: "session", AgentSessionID: id, WorkspaceDir: t.TempDir(), Locator: baldasession.SessionLocator{ChannelType: deliveryfmt.TransportTelegram, SessionID: "session"}, DeliveryOptions: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"question", "trusted policy context", "name: telegram_rich_markdown"} {
		if !strings.Contains(input, part) {
			t.Fatalf("provider input missing %q: %s", part, input)
		}
	}
}

func (h *turnHooks) Run(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	h.events = append(h.events, event)
	if event.Name == h.deny {
		return hookcmd.Result{}, &hookcmd.DeniedError{Hook: "guard", Reason: "rejected"}
	}
	return hookcmd.Result{AdditionalContext: "trusted policy context"}, nil
}

func TestPromptHookDenialPreventsProviderExecution(t *testing.T) {
	calls := 0
	runner, id := newCaptureTestRunner(t, func(string) []*adksession.Event { calls++; return nil })
	service := NewTurnExecutionServiceWithJobEvents(nil, nil, nil, zerolog.Nop(), 1)
	hooks := &turnHooks{deny: hookcmd.UserPromptSubmit}
	service.SetHooks(hooks)
	err := service.Execute(t.Context(), ExecutionRequest{Text: "question", Runner: runner, UserID: "tg-101", SessionID: "session", AgentSessionID: id, WorkspaceDir: t.TempDir()})
	if !errors.Is(err, hookcmd.ErrDenied) || calls != 0 {
		t.Fatalf("Execute() error = %v, provider calls = %d, want denied and zero", err, calls)
	}
	if len(hooks.events) != 1 || hooks.events[0].Prompt != "question" {
		t.Fatalf("hook events = %+v", hooks.events)
	}
}

func TestStopHookDenialPrecedesFinalDeliveryAndCapture(t *testing.T) {
	runner, id := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		event := adksession.NewEvent(context.Background(), invocationID)
		event.Content = genai.NewContentFromText("candidate answer", genai.RoleModel)
		event.TurnComplete = true
		return []*adksession.Event{event}
	})
	dispatcher, capture := &captureDispatcher{}, &captureHook{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil, zerolog.Nop(), 1, capture)
	hooks := &turnHooks{deny: hookcmd.Stop}
	service.SetHooks(hooks)
	err := service.Execute(t.Context(), ExecutionRequest{Text: "question", Runner: runner, UserID: "tg-101", SessionID: "session", AgentSessionID: id, WorkspaceDir: t.TempDir(), Deliver: true, DedupeKey: "turn"})
	if !errors.Is(err, hookcmd.ErrDenied) {
		t.Fatalf("Execute() error = %v, want denied", err)
	}
	if len(dispatcher.envelopes) != 0 || len(capture.turns) != 0 {
		t.Fatal("rejected terminal answer was delivered or captured")
	}
	if len(hooks.events) != 2 || hooks.events[1].Name != hookcmd.Stop || hooks.events[1].Response != "candidate answer" {
		t.Fatalf("hook events = %+v", hooks.events)
	}
}
