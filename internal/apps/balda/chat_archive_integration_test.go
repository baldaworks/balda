package balda

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const archiveIntegrationApp = "norma-balda"

type archiveIntegrationRuntime struct {
	runtime *session.BuiltRuntime
	calls   atomic.Int32
}

func (f *archiveIntegrationRuntime) Runtime(context.Context) (*session.BuiltRuntime, error) {
	return f.runtime, nil
}
func (*archiveIntegrationRuntime) ProviderID() string { return "deterministic-archive" }
func (*archiveIntegrationRuntime) GetAgentMetadata(string) session.AgentMetadata {
	return session.AgentMetadata{}
}
func (f *archiveIntegrationRuntime) CreateRuntimeSession(ctx context.Context, r *session.BuiltRuntime, a, u, id, d string, c session.RuntimeSessionContext) (adksession.Session, error) {
	return f.CreateFreshRuntimeSession(ctx, r, a, u, id, d, c)
}
func (*archiveIntegrationRuntime) CreateFreshRuntimeSession(ctx context.Context, r *session.BuiltRuntime, _, u, id, _ string, c session.RuntimeSessionContext) (adksession.Session, error) {
	v, e := r.SessionSvc.Create(ctx, &adksession.CreateRequest{AppName: r.AppName, UserID: u, SessionID: id, State: map[string]any{chatarchivecmd.EpochStateKey: c.ArchiveID}})
	if e != nil {
		return nil, e
	}
	return v.Session, nil
}
func (*archiveIntegrationRuntime) OpenExistingRuntimeSession(ctx context.Context, r *session.BuiltRuntime, _, u, id, _ string, c session.RuntimeSessionContext) (adksession.Session, error) {
	v, e := r.SessionSvc.Get(ctx, &adksession.GetRequest{AppName: r.AppName, UserID: u, SessionID: id})
	if e != nil {
		return nil, e
	}
	epoch, e := v.Session.State().Get(chatarchivecmd.EpochStateKey)
	if c.ArchiveID != "" && (e != nil || epoch != c.ArchiveID) {
		return nil, errors.New("archive epoch mismatch")
	}
	return v.Session, nil
}
func newArchiveIntegrationRuntime(t *testing.T, p state.Provider, persistent bool) *archiveIntegrationRuntime {
	t.Helper()
	f := &archiveIntegrationRuntime{}
	svc := p.RuntimeSessions()
	if !persistent {
		svc = adksession.InMemoryService()
	}
	ag, e := adkagent.New(adkagent.Config{Name: "archive-deterministic", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			if err := ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			f.calls.Add(1)
			thought := adksession.NewEvent(ctx, ctx.InvocationID())
			thought.Author = "archive-deterministic"
			thought.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "hidden reasoning marker", Thought: true}}}
			if !yield(thought, nil) {
				return
			}
			final := adksession.NewEvent(ctx, ctx.InvocationID())
			final.Author = "archive-deterministic"
			final.TurnComplete = true
			final.Content = genai.NewContentFromText("Synthetic selected final\n<script>harmless chat</script>", genai.RoleModel)
			yield(final, nil)
		}
	}})
	if e != nil {
		t.Fatal(e)
	}
	run, e := runner.New(runner.Config{AppName: archiveIntegrationApp, Agent: ag, SessionService: svc})
	if e != nil {
		t.Fatal(e)
	}
	f.runtime = &session.BuiltRuntime{AppName: archiveIntegrationApp, Agent: ag, Runner: run, SessionSvc: svc, RuntimeSnapshotID: "archive-integration-snapshot"}
	return f
}
func (f *archiveIntegrationRuntime) manager(t *testing.T, p state.Provider, persistent bool) *session.Manager {
	t.Helper()
	m, e := session.NewManager(session.ManagerParams{Archive: p.ChatArchive(), AgentBuilder: f, RuntimeManager: f, BaldaProviderID: f.ProviderID(), WorkingDir: t.TempDir(), SessionsPersistent: persistent, SessionStore: p.Sessions(), Logger: zerolog.Nop()})
	if e != nil {
		t.Fatal(e)
	}
	return m
}

type archiveIntegrationBus struct {
	fail  bool
	sends int
}

func (b *archiveIntegrationBus) Dispatch(_ context.Context, _ actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if b.fail {
		return nil, errors.New("synthetic publication outage")
	}
	b.sends++
	return &actortransport.DispatchReceipt{}, nil
}

func archiveIntegrationLocator() deliverycmd.Locator {
	return deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:7", AddressJSON: `{"chat_id":101,"topic_id":7}`, SessionID: "tg-101-7"}
}
func archiveIntegrationInput() chatarchivecmd.Input {
	return chatarchivecmd.Input{Source: "telegram:101:7:1", Text: "Synthetic original user\n<script>user</script>", Speaker: chatarchivecmd.Speaker{Principal: "human-one", Name: "Synthetic speaker one"}, ReceivedAt: time.Now().UTC(), Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "101:7", Locator: "telegram:101:7", Title: "Synthetic personal topic", Audience: "personal", Subdivision: "topic", SubdivisionLabel: "Planning"}}
}

// One provider owns lifecycle metadata, ADK history, inputs and final replay.
func TestChatArchiveSharedProviderLifecycleAndTurns(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		t.Run(map[bool]string{true: "durable", false: "memory"}[persistent], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			p, e := state.NewSQLiteProvider(t.Context(), path)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = p.Close() }()
			f := newArchiveIntegrationRuntime(t, p, persistent)
			m := f.manager(t, p, persistent)
			loc := archiveIntegrationLocator()
			sc := session.SessionContext{Locator: loc, UserID: "human-one"}
			first, e := m.EnsureSession(t.Context(), sc, "Synthetic first lifetime")
			if e != nil {
				t.Fatal(e)
			}
			input := archiveIntegrationInput()
			admission := chatarchiveapp.New(p.ChatArchive())
			if _, e = admission.RecordInput(t.Context(), first.GetArchiveID(), input); e != nil {
				t.Fatal(e)
			}
			bus := &archiveIntegrationBus{fail: true}
			execute := sessionturnapp.NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
			execute.SetFinalArchive(admission)
			req := sessionturnapp.ExecutionRequest{ArchiveID: first.GetArchiveID(), Runner: first.GetRunner(), AgentSessionID: first.GetAgentSessionID(), UserID: first.GetUserID(), SessionID: first.GetSessionID(), Text: "enriched private prompt marker", DedupeKey: "execution-one", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: loc}
			if e = execute.Execute(t.Context(), req); e == nil {
				t.Fatal("publication outage was ignored")
			}
			if f.calls.Load() != 1 || bus.sends != 0 {
				t.Fatal("unexpected model or delivery count")
			}
			canceled := input
			canceled.Source = "telegram:101:7:2"
			canceled.Text = "Synthetic canceled input"
			canceled.Speaker = chatarchivecmd.Speaker{Principal: "human-two", Name: "Synthetic speaker two"}
			if _, e = admission.RecordInput(t.Context(), first.GetArchiveID(), canceled); e != nil {
				t.Fatal(e)
			}
			cancelCtx, cancel := context.WithCancel(t.Context())
			cancel()
			canceledRequest := req
			canceledRequest.DedupeKey = "execution-canceled"
			canceledRequest.Text = canceled.Text
			if err := execute.Execute(cancelCtx, canceledRequest); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled admitted turn = %v", err)
			}
			if f.calls.Load() != 1 {
				t.Fatal("canceled turn invoked the model")
			}
			if e = m.ResetSession(t.Context(), loc); e != nil {
				t.Fatal(e)
			}
			second, e := m.EnsureSession(t.Context(), sc, "Synthetic replacement")
			if e != nil {
				t.Fatal(e)
			}
			if second.GetArchiveID() == first.GetArchiveID() || (persistent && second.GetAgentSessionID() != first.GetAgentSessionID()) {
				t.Fatal("reset epoch/routing mismatch")
			}
			// Immutable replay executes after reset without any session accessor.
			bus.fail = false
			queued := sessionturn.New(nil, sessionturnapp.NewProviderTurnExecutorFromService(execute), nil, zerolog.Nop())
			if e = queued.RunSessionTurnPayload(t.Context(), turncmd.SessionTurnPayload{ArchiveID: first.GetArchiveID(), DedupeKey: "execution-one", Deliver: true, Source: turncmd.SourceTelegram, Locator: loc}); e != nil {
				t.Fatal(e)
			}
			if f.calls.Load() != 1 || bus.sends != 1 {
				t.Fatal("replay invoked model or lost final")
			}
			if e = m.ResetSessionWithReason(t.Context(), loc, session.BoundaryReasonClose); e != nil {
				t.Fatal(e)
			}
			third, e := m.EnsureSession(t.Context(), sc, "Synthetic after close")
			if e != nil {
				t.Fatal(e)
			}
			if third.GetArchiveID() == second.GetArchiveID() {
				t.Fatal("close reused epoch")
			}
			if persistent {
				if e = m.Stop(t.Context()); e != nil {
					t.Fatal(e)
				}
			}
			if e = p.Close(); e != nil {
				t.Fatal(e)
			}
			p, e = state.NewSQLiteProvider(t.Context(), path)
			if e != nil {
				t.Fatal(e)
			}
			f2 := newArchiveIntegrationRuntime(t, p, persistent)
			m2 := f2.manager(t, p, persistent)
			restored, e := m2.EnsureSession(t.Context(), sc, "Synthetic reopen")
			if e != nil {
				t.Fatal(e)
			}
			if persistent && restored.GetArchiveID() != third.GetArchiveID() {
				t.Fatal("exact reopen split lifetime")
			}
			if !persistent && restored.GetArchiveID() == third.GetArchiveID() {
				t.Fatal("new memory boot reused lifetime")
			}
			if persistent {
				if e = m2.Stop(t.Context()); e != nil {
					t.Fatal(e)
				}
				if e = p.RuntimeSessions().Delete(t.Context(), &adksession.DeleteRequest{AppName: archiveIntegrationApp, UserID: restored.GetUserID(), SessionID: restored.GetAgentSessionID()}); e != nil {
					t.Fatal(e)
				}
				fallback, e := f2.manager(t, p, true).EnsureSession(t.Context(), sc, "Synthetic fallback")
				if e != nil {
					t.Fatal(e)
				}
				if fallback.GetArchiveID() == restored.GetArchiveID() {
					t.Fatal("fresh fallback reused lifetime")
				}
			}
			messages, e := p.ChatArchive().Messages(t.Context(), first.GetArchiveID(), 0, 100)
			if e != nil {
				t.Fatal(e)
			}
			if len(messages) != 3 || messages[0].Text != input.Text || messages[1].Role != "agent" || messages[2].Speaker.Name != "Synthetic speaker two" {
				t.Fatalf("retained transcript=%+v", messages)
			}
			next, e := p.ChatArchive().Messages(t.Context(), second.GetArchiveID(), 0, 100)
			if e != nil || len(next) != 0 {
				t.Fatal("late final escaped original lifetime", e)
			}
			if f2.calls.Load() != 0 {
				t.Fatal("passive read/restoration invoked model")
			}
		})
	}
}
