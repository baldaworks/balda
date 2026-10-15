package handlersfx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/chatfx"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

type storedIngressBuilder struct{ fakeAgentBuilder }

func (b *storedIngressBuilder) CreateFreshRuntimeSession(ctx context.Context, runtime *baldasession.BuiltRuntime, _, userID, sessionID, _ string, _ baldasession.RuntimeSessionContext) (adksession.Session, error) {
	created, err := runtime.SessionSvc.Create(ctx, &adksession.CreateRequest{AppName: runtime.AppName, UserID: userID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return created.Session, nil
}

func (b *storedIngressBuilder) OpenExistingRuntimeSession(ctx context.Context, runtime *baldasession.BuiltRuntime, _, userID, sessionID, _ string, _ baldasession.RuntimeSessionContext) (adksession.Session, error) {
	opened, err := runtime.SessionSvc.Get(ctx, &adksession.GetRequest{AppName: runtime.AppName, UserID: userID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return opened.Session, nil
}

type storedIngressRuntime struct {
	runtime     *baldasession.BuiltRuntime
	rejectSaved bool
}

func (r *storedIngressRuntime) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return r.runtime, nil
}
func (*storedIngressRuntime) ProviderID() string { return "alpha" }
func (r *storedIngressRuntime) RuntimeForSession(_ context.Context, req baldasession.SessionRuntimeRequest) (*baldasession.BuiltRuntime, error) {
	if r.rejectSaved && req.RuntimeSnapshotID != "" {
		return nil, errors.New("saved MCP configuration unavailable")
	}
	return r.runtime, nil
}

func TestCollaboratorDMRestoresSQLiteSessionAfterRestart(t *testing.T) {
	for _, recoverFresh := range []bool{false, true} {
		name := "preserve saved history"
		if recoverFresh {
			name = "recover failed restore"
		}
		t.Run(name, func(t *testing.T) {
			provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			runtime := &baldasession.BuiltRuntime{AppName: "norma-balda", SessionSvc: provider.RuntimeSessions(), RuntimeSnapshotID: "current-snapshot"}
			workingDir := t.TempDir()
			newManager := func(rejectSaved bool) *baldasession.Manager {
				manager, err := baldasession.NewManager(baldasession.ManagerParams{
					AgentBuilder: &storedIngressBuilder{}, RuntimeManager: &storedIngressRuntime{runtime: runtime, rejectSaved: rejectSaved},
					BaldaProviderID: "alpha", WorkingDir: workingDir, SessionsPersistent: true,
					SessionStore: provider.Sessions(), Logger: zerolog.Nop(),
				})
				if err != nil {
					t.Fatal(err)
				}
				return manager
			}
			locator := baldatelegram.NewLocator(9303, 0)
			if _, err := newManager(false).EnsureSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "tg-303"}, "saved collaborator"); err != nil {
				t.Fatal(err)
			}
			old, err := runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: runtime.AppName, UserID: "tg-303", SessionID: locator.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			event := adksession.NewEvent(t.Context(), "saved-turn")
			event.Author = "user"
			event.Actions.StateDelta = map[string]any{"saved_value": "keep"}
			if err := runtime.SessionSvc.AppendEvent(t.Context(), old.Session, event); err != nil {
				t.Fatal(err)
			}
			restarted := newManager(recoverFresh)
			harness := newTestHarness(t)
			harness.handler.sessionManager = restarted
			harness.handler.sessionPreparer = chatfx.NewIngressSessionAdapter(restarted)
			harness.handler.collaboratorStore, _ = newTestCollaboratorStore(auth.TelegramSubject(303))
			if err := harness.handler.HandleMessage(t.Context(), baldatelegram.MessageContext{Locator: locator, ChatID: 9303, MessageID: 10, UserID: 303, Text: "continue", IsDM: true}); err != nil {
				t.Fatal(err)
			}
			turns := 0
			for _, envelope := range harness.dispatcher.commands {
				if envelope.Namespace == "human.inbound" {
					turns++
					if envelope.To.Key != locator.SessionID {
						t.Fatalf("session lane = %q", envelope.To.Key)
					}
				}
			}
			if turns != 1 {
				t.Fatalf("published turns = %d, want 1", turns)
			}
			opened, err := runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: runtime.AppName, UserID: "tg-303", SessionID: locator.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			wantEvents := 1
			if recoverFresh {
				wantEvents = 0
			}
			if got := opened.Session.Events().Len(); got != wantEvents {
				t.Fatalf("history events = %d, want %d", got, wantEvents)
			}
			if !recoverFresh {
				if got, err := opened.Session.State().Get("saved_value"); err != nil || got != "keep" {
					t.Fatalf("saved state = %v, %v", got, err)
				}
			}
		})
	}
}

func TestCollaboratorDMPreparationFailureRemainsRetryable(t *testing.T) {
	harness := newTestHarness(t)
	harness.handler.collaboratorStore, _ = newTestCollaboratorStore(auth.TelegramSubject(303))
	dependencyErr := errors.New("storage temporarily unavailable")
	harness.handler.sessionPreparer = ingressapp.SessionPreparerFunc(func(context.Context, ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
		return ingressapp.SessionPreparation{}, dependencyErr
	})
	service, err := harness.handler.telegramIngressService()
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Process(t.Context(), baldatelegram.NormalizeInbound(baldatelegram.MessageContext{Locator: baldatelegram.NewLocator(9303, 0), ChatID: 9303, MessageID: 10, UserID: 303, Text: "continue", IsDM: true}, "continue", time.Now()))
	settlement := result.Settlement
	if !errors.Is(err, dependencyErr) || settlement.Outcome != turncmd.InboundRetry {
		t.Fatalf("settlement = %+v, error = %v", settlement, err)
	}
	for _, envelope := range harness.dispatcher.commands {
		if envelope.Namespace == "human.inbound" {
			t.Fatal("published turn despite failed preparation")
		}
	}
}

func TestConversationalPresentationConsumesPreparedSession(t *testing.T) {
	expected := ingressapp.SessionPreparation{Ready: true, SessionID: "saved-session", UserID: "saved-user", RuntimeSnapshotID: "saved-snapshot"}
	calls := 0
	preparer := ingressapp.SessionPreparerFunc(func(_ context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
		calls++
		if inbound.UserID != "requester" {
			t.Fatalf("requester = %q", inbound.UserID)
		}
		return expected, nil
	})
	tests := []struct {
		name    string
		prepare func(context.Context, ingressapp.InboundContext) (ingressapp.SessionPreparation, error)
	}{
		{name: "telegram", prepare: (&telegramInboundHandler{sessionPreparer: preparer}).prepareTelegramSession},
		{name: "zulip", prepare: (&zulipInboundHandler{sessionPreparer: preparer}).prepareZulipSession},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.prepare(t.Context(), ingressapp.InboundContext{UserID: "requester"})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Ready || result.SessionID != expected.SessionID || result.UserID != expected.UserID || result.RuntimeSnapshotID != expected.RuntimeSnapshotID {
				t.Fatalf("preparation = %+v", result)
			}
		})
	}
	if calls != len(tests) {
		t.Fatalf("preparation calls = %d", calls)
	}
}
