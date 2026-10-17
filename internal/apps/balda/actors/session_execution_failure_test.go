package actors

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adkrunner "google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestSessionActorNotifiesUnsupportedModelAfterProviderStarted(t *testing.T) {
	for _, test := range []struct {
		maxAttempts int
		partial     bool
	}{{5, true}, {2, true}, {5, false}} {
		t.Run(fmt.Sprintf("attempts-%d-partial-%t", test.maxAttempts, test.partial), func(t *testing.T) {
			maxAttempts := test.maxAttempts
			cause := errors.New("session model is not supported credential=SYNTHETIC_SECRET")
			var calls atomic.Int32
			actor, bus, payload := executionFailureActor(t, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
				return func(yield func(*adksession.Event, error) bool) {
					calls.Add(1)
					progress := adksession.NewEvent(ctx, ctx.InvocationID())
					progress.Partial = test.partial
					progress.Content = genai.NewContentFromText("working", genai.RoleModel)
					if !yield(progress, nil) {
						return
					}
					yield(nil, cause)
				}
			})
			env, err := turncmd.SessionTurnEnvelope(payload)
			if err != nil {
				t.Fatal(err)
			}
			env.ID, env.MaxAttempts = "telegram:10001:0:5316", maxAttempts
			env.CorrelationID = "telegram-update-5316"
			for attempt := range maxAttempts {
				env.Attempt = attempt
				if err := actor.Handle(t.Context(), env); !errors.Is(err, cause) {
					t.Fatalf("attempt %d error = %v, want iterator cause", attempt+1, err)
				}
				var notices []deliverycmd.Payload
				for _, command := range bus.commands {
					var delivery deliverycmd.Payload
					if err := actorlayer.UnmarshalPayload(command.Payload, &delivery); err != nil {
						t.Fatal(err)
					}
					if delivery.Settlement == deliverycmd.SettlementOutbox {
						notices = append(notices, delivery)
					}
				}
				want := 0
				if attempt == maxAttempts-1 {
					want = 1
				}
				if len(notices) != want {
					t.Fatalf("inbound=%s attempt=%d/%d provider calls=%d terminal notices=%d, want%d", env.ID, attempt+1, maxAttempts, calls.Load(), len(notices), want)
				}
				if want != 0 {
					notice := bus.commands[len(bus.commands)-1]
					if notice.CausationID != env.ID || notice.CorrelationID != env.CorrelationID {
						t.Fatalf("notice causation=%q correlation=%q, want%q/%q", notice.CausationID, notice.CorrelationID, env.ID, env.CorrelationID)
					}
				}
				if want != 0 && (notices[0].Locator != payload.Locator || strings.Contains(notices[0].Text, "SYNTHETIC_SECRET")) {
					t.Fatalf("unsafe/misdirected notice = %+v", notices[0])
				}
			}
			if got := calls.Load(); got != int32(maxAttempts) {
				t.Fatalf("provider calls=%d, want%d", got, maxAttempts)
			}
		})
	}
}

func executionFailureActor(t *testing.T, run func(adkagent.InvocationContext) iter.Seq2[*adksession.Event, error], build ...func(*recordingHandlerCommandBus) *sessionturnapp.TurnExecutionService) (*SessionActorExecutor, *recordingHandlerCommandBus, turncmd.SessionTurnPayload) {
	t.Helper()
	const appName = "execution-failure"
	sessions := adksession.InMemoryService()
	agent, err := adkagent.New(adkagent.Config{Name: appName, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := adkrunner.New(adkrunner.Config{AppName: appName, Agent: agent, SessionService: sessions})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(t.Context(), &adksession.CreateRequest{AppName: appName, UserID: "telegram-user"})
	if err != nil {
		t.Fatal(err)
	}
	active := &failureActiveSession{runner: runner, id: created.Session.ID()}
	bus := &recordingHandlerCommandBus{}
	execution := sessionturnapp.NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)
	if len(build) != 0 {
		execution = build[0](bus)
	}
	queued := sessionturn.New(failureSessionAccessor{active}, sessionturnapp.NewProviderTurnExecutorFromService(execution), nil, zerolog.Nop())
	turns := NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() { _ = turns.Shutdown(context.Background()) })
	actor := NewSessionActor(SessionActorConfig{Turns: turns, Runner: queued, Dispatcher: bus})
	payload := turncmd.SessionTurnPayload{Text: "message", UserID: "telegram-user", MessageID: 5316, Source: turncmd.SourceTelegram, Deliver: true,
		Locator: deliverycmd.Locator{SessionID: "tg-10001-0", ChannelType: "telegram", AddressKey: "10001:0", AddressJSON: `{"chat_id":10001,"topic_id":0}`}}
	return actor, bus, payload
}

type failureActiveSession struct {
	runner *adkrunner.Runner
	id     string
}

func (s *failureActiveSession) GetRunner() *adkrunner.Runner { return s.runner }
func (*failureActiveSession) GetRuntimeSnapshotID() string   { return "" }
func (*failureActiveSession) GetSessionID() string           { return "tg-10001-0" }
func (s *failureActiveSession) GetAgentSessionID() string    { return s.id }
func (*failureActiveSession) GetUserID() string              { return "telegram-user" }
func (*failureActiveSession) RuntimeStateValue(context.Context, string) (any, bool, error) {
	return nil, false, nil
}

type failureSessionAccessor struct{ active sessionturn.ActiveSession }

func (s failureSessionAccessor) GetSession(sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	return s.active, nil
}
func (s failureSessionAccessor) RestoreSession(context.Context, sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	return s.active, nil
}
func (s failureSessionAccessor) EnsureSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return s.active, nil
}
func (s failureSessionAccessor) EnsureTransientSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return s.active, nil
}

func TestSessionActorSettlesAcceptedFinalWithoutReplay(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal-%t", terminal), func(t *testing.T) {
			var calls atomic.Int32
			cause := errors.New("post-publication bookkeeping failed")
			actor, bus, payload := executionFailureActor(t, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
				return func(yield func(*adksession.Event, error) bool) {
					calls.Add(1)
					done := adksession.NewEvent(ctx, ctx.InvocationID())
					done.TurnComplete = true
					if terminal {
						done.ErrorMessage = "provider unavailable"
					} else {
						done.Content = genai.NewContentFromText("answer", genai.RoleModel)
					}
					yield(done, nil)
				}
			}, func(bus *recordingHandlerCommandBus) *sessionturnapp.TurnExecutionService {
				return sessionturnapp.NewTurnExecutionServiceWithJobEvents(bus, failureFinalAppender{cause}, nil, zerolog.Nop(), automode.DefaultMaxTurns)
			})
			payload.JobID = "chat-correlation"
			env, err := turncmd.SessionTurnEnvelope(payload)
			if err != nil {
				t.Fatal(err)
			}
			env.Attempt, env.MaxAttempts = 4, 5
			if err := actor.Handle(t.Context(), env); err != nil {
				t.Fatalf("accepted final should settle, got%v", err)
			}
			if calls.Load() != 1 || len(bus.commands) != 1 {
				t.Fatalf("provider calls=%d deliveries=%d, want1/1", calls.Load(), len(bus.commands))
			}
			var delivery deliverycmd.Payload
			if err := actorlayer.UnmarshalPayload(bus.commands[0].Payload, &delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.Text == terminalFailureMessage || delivery.Locator != payload.Locator {
				t.Fatalf("wrong final=%+v", delivery)
			}
		})
	}
}

type failureFinalAppender struct{ err error }

func (a failureFinalAppender) AppendEvent(context.Context, string, string, string, string, any) error {
	return a.err
}

func TestSessionActorFailedThenSuccessfulExecutionHasNoFailureNotice(t *testing.T) {
	calls := 0
	cause := errors.New("temporary iterator failure")
	actor, bus, payload := executionFailureActor(t, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			calls++
			if calls == 1 {
				yield(nil, cause)
				return
			}
			done := adksession.NewEvent(ctx, ctx.InvocationID())
			done.TurnComplete = true
			done.Content = genai.NewContentFromText("answer", genai.RoleModel)
			yield(done, nil)
		}
	})
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.MaxAttempts = 5
	if err := actor.Handle(t.Context(), env); !errors.Is(err, cause) {
		t.Fatalf("first attempt=%v", err)
	}
	env.Attempt++
	if err := actor.Handle(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(bus.commands) != 1 {
		t.Fatalf("provider calls=%d deliveries=%d, want2/1", calls, len(bus.commands))
	}
	var delivery deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(bus.commands[0].Payload, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Text != "answer" {
		t.Fatalf("delivery=%+v", delivery)
	}
}

func TestSessionActorFailedFinalPublicationRemainsUnhandled(t *testing.T) {
	actor, bus, payload := executionFailureActor(t, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			done := adksession.NewEvent(ctx, ctx.InvocationID())
			done.TurnComplete = true
			done.Content = genai.NewContentFromText("answer", genai.RoleModel)
			yield(done, nil)
		}
	})
	cause := errors.New("final publication unavailable")
	bus.commandErrs = []error{cause}
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.Attempt, env.MaxAttempts = 4, 5
	if err := actor.Handle(t.Context(), env); !errors.Is(err, cause) {
		t.Fatalf("error=%v,want failed publication", err)
	}
	if len(bus.commands) != 1 {
		t.Fatalf("deliveries=%d,want terminalnotice only", len(bus.commands))
	}
	var delivery deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(bus.commands[0].Payload, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Text != terminalFailureMessage || delivery.Locator != payload.Locator {
		t.Fatalf("wrong notice=%+v", delivery)
	}
}
