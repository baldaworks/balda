package sessionturnapp

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/automode"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type formatTestFormatter struct {
	name deliveryfmt.Name
}

func (f formatTestFormatter) Name() deliveryfmt.Name { return f.name }

func (f formatTestFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{Name: f.name, Text: text, PlainFallback: text}, nil
}

type formatTestState struct {
	values  map[string]any
	updates int
}

func (s *formatTestState) RuntimeStateValue(_ context.Context, _ baldasession.SessionLocator, key string) (any, bool, error) {
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *formatTestState) UpdateRuntimeState(_ context.Context, _ baldasession.SessionLocator, state map[string]any) error {
	if s.values == nil {
		s.values = make(map[string]any)
	}
	for key, value := range state {
		s.values[key] = value
	}
	s.updates++
	return nil
}

func TestFormatPromptComposerTracksRegisteredNameChanges(t *testing.T) {
	t.Parallel()

	state := &formatTestState{values: make(map[string]any)}
	composer := NewFormatPromptComposer(newFormatTestRegistry(t), state)
	locator := baldasession.SessionLocator{ChannelType: deliveryfmt.TransportTelegram, SessionID: "tg-1-0"}

	tests := []struct {
		name       string
		format     deliveryfmt.DeliveryFormat
		wantMarker string
		wantChange bool
	}{
		{name: "first rich format", format: deliveryfmt.DeliveryFormatRichMarkdown, wantMarker: "name: telegram_rich_markdown", wantChange: true},
		{name: "same registered format", format: deliveryfmt.DeliveryFormatRichMarkdown},
		{name: "changed rich format", format: deliveryfmt.DeliveryFormatRichHTML, wantMarker: "name: telegram_rich_html", wantChange: true},
		{name: "explicit plain format", format: deliveryfmt.DeliveryFormatNone, wantMarker: "name: plain_text", wantChange: true},
		{name: "format disabled", wantMarker: "Previous application message-format rules no longer apply.", wantChange: true},
		{name: "format remains disabled"},
		{name: "format re-enabled", format: deliveryfmt.DeliveryFormatRichMarkdown, wantMarker: "name: telegram_rich_markdown", wantChange: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, change, err := composer.Compose(context.Background(), locator, test.format, "hello")
			if err != nil {
				t.Fatalf("Compose() error = %v", err)
			}
			if test.wantMarker == "" {
				if got != "hello" {
					t.Fatalf("Compose() = %q, want unchanged user text", got)
				}
			} else if !strings.Contains(got, test.wantMarker) || !strings.Contains(got, "[user-request]\nhello") {
				t.Fatalf("Compose() missing current format or user boundary:\n%s", got)
			}
			if (change != nil) != test.wantChange {
				t.Fatalf("Compose() change = %#v, wantChange %t", change, test.wantChange)
			}
			if change != nil {
				if err := composer.Commit(context.Background(), locator, *change); err != nil {
					t.Fatalf("Commit() error = %v", err)
				}
			}
		})
	}
}

func TestFormatPromptComposerRejectsUnknownRoute(t *testing.T) {
	t.Parallel()

	composer := NewFormatPromptComposer(newFormatTestRegistry(t), &formatTestState{values: make(map[string]any)})
	_, _, err := composer.Compose(
		context.Background(),
		baldasession.SessionLocator{ChannelType: deliveryfmt.TransportTelegram, SessionID: "tg-1-0"},
		"unknown",
		"hello",
	)
	if !errors.Is(err, deliveryfmt.ErrRouteNotFound) {
		t.Fatalf("Compose() error = %v, want ErrRouteNotFound", err)
	}
}

func TestTurnExecutionCommitsFormatOnlyAfterSuccessfulTerminalCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		events     func(string) ([]*adksession.Event, error)
		wantError  bool
		wantCommit bool
	}{
		{
			name: "successful terminal turn",
			events: func(invocationID string) ([]*adksession.Event, error) {
				answer := adksession.NewEvent(context.Background(), invocationID)
				answer.Content = genai.NewContentFromText("done", genai.RoleModel)
				terminal := adksession.NewEvent(context.Background(), invocationID)
				terminal.TurnComplete = true
				return []*adksession.Event{answer, terminal}, nil
			},
			wantCommit: true,
		},
		{
			name: "terminal provider failure",
			events: func(invocationID string) ([]*adksession.Event, error) {
				terminal := adksession.NewEvent(context.Background(), invocationID)
				terminal.TurnComplete = true
				terminal.ErrorMessage = "provider failed"
				return []*adksession.Event{terminal}, nil
			},
		},
		{
			name: "interrupted terminal turn",
			events: func(invocationID string) ([]*adksession.Event, error) {
				terminal := adksession.NewEvent(context.Background(), invocationID)
				terminal.TurnComplete = true
				terminal.Interrupted = true
				return []*adksession.Event{terminal}, nil
			},
		},
		{
			name: "retrying stream without terminal completion",
			events: func(invocationID string) ([]*adksession.Event, error) {
				retry := adksession.NewEvent(context.Background(), invocationID)
				retry.ErrorMessage = "Reconnecting... 1/5"
				return []*adksession.Event{retry}, nil
			},
		},
		{
			name: "canceled provider run",
			events: func(string) ([]*adksession.Event, error) {
				return nil, context.Canceled
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &formatTestState{values: make(map[string]any)}
			var providerInput string
			adkRunner, agentSessionID := newFormatTestRunner(t, func(invocationID, input string) ([]*adksession.Event, error) {
				providerInput = input
				return test.events(invocationID)
			})
			service := NewTurnExecutionServiceWithFormats(
				nil,
				nil,
				state,
				zerolog.Nop(),
				automode.DefaultMaxTurns,
				nil,
				newFormatTestRegistry(t),
			)
			err := service.Execute(context.Background(), ExecutionRequest{
				Text:           "hello",
				Runner:         adkRunner,
				UserID:         "telegram-user",
				SessionID:      "tg-1-0",
				AgentSessionID: agentSessionID,
				Locator:        baldasession.SessionLocator{ChannelType: deliveryfmt.TransportTelegram, SessionID: "tg-1-0"},
				DeliveryOptions: deliveryfmt.Options{
					DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
				},
				Deliver: false,
			})
			if (err != nil) != test.wantError {
				t.Fatalf("Execute() error = %v, wantError %t", err, test.wantError)
			}
			if !strings.Contains(providerInput, "name: telegram_rich_markdown") {
				t.Fatalf("provider input missing format guidance:\n%s", providerInput)
			}
			if got := state.updates > 0; got != test.wantCommit {
				t.Fatalf("format state committed = %t, want %t", got, test.wantCommit)
			}
		})
	}
}

func newFormatTestRegistry(t *testing.T) *deliveryfmt.Registry {
	t.Helper()
	formats := []deliveryfmt.Format{
		{Name: deliveryfmt.NameTelegramRichMarkdown, Instructions: "Use rich Markdown.", Example: "**Hello**"},
		{Name: deliveryfmt.NameTelegramRichHTML, Instructions: "Use rich HTML.", Example: "<b>Hello</b>"},
		{Name: deliveryfmt.NamePlainText, Instructions: "Use plain text only.", Example: "Hello"},
	}
	formatters := make([]deliveryfmt.FormatterRegistration, 0, len(formats))
	for _, format := range formats {
		formatters = append(formatters, deliveryfmt.FormatterRegistration{
			Name:      format.Name,
			Formatter: formatTestFormatter{name: format.Name},
		})
	}
	registry, err := deliveryfmt.NewRegistry(formats, formatters, []deliveryfmt.Route{
		{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown, RegisteredName: deliveryfmt.NameTelegramRichMarkdown},
		{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichHTML, RegisteredName: deliveryfmt.NameTelegramRichHTML},
		{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatNone, RegisteredName: deliveryfmt.NamePlainText},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

func newFormatTestRunner(
	t *testing.T,
	events func(invocationID, input string) ([]*adksession.Event, error),
) (*runner.Runner, string) {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{
		Name:        "format-prompt-test",
		Description: "scripted format prompt test agent",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				items, runErr := events(ctx.InvocationID(), visibleFormatTestText(ctx.UserContent()))
				for _, event := range items {
					if !yield(event, nil) {
						return
					}
				}
				if runErr != nil {
					yield(nil, runErr)
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	sessions := adksession.InMemoryService()
	adkRunner, err := runner.New(runner.Config{AppName: "format-prompt-test", Agent: agent, SessionService: sessions})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessions.Create(context.Background(), &adksession.CreateRequest{AppName: "format-prompt-test", UserID: "telegram-user"})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	return adkRunner, created.Session.ID()
}

func visibleFormatTestText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var text strings.Builder
	for _, part := range content.Parts {
		if part != nil && !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}
