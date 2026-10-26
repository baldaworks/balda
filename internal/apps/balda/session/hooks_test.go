package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type lifecycleHooks struct {
	events                 []hookcmd.Event
	deny                   bool
	waitForEndCancellation bool
}

func TestSessionStartDenialPreservesPersistedRestore(t *testing.T) {
	hooks := &lifecycleHooks{deny: true}
	manager := hookTestManager(t, hooks)
	locator := testTelegramLocator(100, 0)
	manager.sessionsPersistent = true
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{AppName: baldaRuntimeAppName, UserID: "user", SessionID: locator.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "saved-turn")
	event.Content = genai.NewContentFromText("saved history", genai.RoleUser)
	if err := service.AppendEvent(t.Context(), created.Session, event); err != nil {
		t.Fatal(err)
	}
	manager.runtimeManager.(*fakeScopedRuntimeManager).sessionRuntime.SessionSvc = service
	record := baldastate.SessionRecord{SessionID: locator.SessionID, UserID: "user", ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "balda", RuntimeSnapshotID: currentSnapshotID, Status: baldastate.SessionStatusActive}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{sessionAddressKey(locator.ChannelType, locator.AddressKey): record}}
	manager.sessionStore = store
	_, err = manager.RestoreSession(t.Context(), SessionContext{Locator: locator, UserID: "user"})
	if !errors.Is(err, hookcmd.ErrDenied) {
		t.Fatalf("RestoreSession() error = %v, want denied", err)
	}
	if store.deletedSessionID != "" || len(store.upsertedRecords) != 0 {
		t.Fatal("denied restore modified persisted session metadata")
	}
	retained, err := service.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: "user", SessionID: locator.SessionID})
	if err != nil || retained.Session.Events().Len() != 1 || retained.Session.Events().At(0).Content.Parts[0].Text != "saved history" {
		t.Fatalf("denied restore changed runtime history: %v", err)
	}
	if _, err := manager.GetSession(locator); err == nil {
		t.Fatal("denied restore became active")
	}
	if len(hooks.events) != 1 || hooks.events[0].Source != "resume" {
		t.Fatalf("events = %+v, want denied resume", hooks.events)
	}
}

func (h *lifecycleHooks) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	h.events = append(h.events, event)
	if h.waitForEndCancellation && event.Name == hookcmd.SessionEnd {
		<-ctx.Done()
		return hookcmd.Result{}, ctx.Err()
	}
	if h.deny {
		return hookcmd.Result{}, hookcmd.ErrDenied
	}
	return hookcmd.Result{}, nil
}

func TestSessionEndHooksRespectRemainingCleanupBudget(t *testing.T) {
	hooks := &lifecycleHooks{waitForEndCancellation: true}
	manager := hookTestManager(t, hooks)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	manager.endSessionHooks(ctx, &TopicSession{sessionID: "session", workspaceDir: t.TempDir(), hooksStarted: true})
	if time.Since(started) > time.Second || len(hooks.events) != 1 {
		t.Fatalf("session-end hook exceeded cleanup budget: %v, events=%+v", time.Since(started), hooks.events)
	}
}

func hookTestManager(t *testing.T, hooks *lifecycleHooks) *Manager {
	t.Helper()
	return &Manager{baldaProviderName: "provider", runtimeManager: &fakeScopedRuntimeManager{fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "provider"}, sessionRuntime: &BuiltRuntime{RuntimeSnapshotID: currentSnapshotID}}, agentBuilder: &fakeAgentBuilder{}, workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: &fakeSessionStore{}, hooks: hooks}
}

func TestSessionLifecycleHooksObserveCreateAndCloseOnce(t *testing.T) {
	hooks := &lifecycleHooks{}
	manager := hookTestManager(t, hooks)
	locator := testTelegramLocator(100, 0)
	if err := manager.CreateSession(t.Context(), SessionContext{Locator: locator, UserID: "user"}, "balda"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(hooks.events) != 2 || hooks.events[0].Name != hookcmd.SessionStart || hooks.events[1].Name != hookcmd.SessionEnd || hooks.events[0].SessionID != locator.SessionID || hooks.events[0].Provider != "provider" || hooks.events[0].CWD != manager.workingDir {
		t.Fatalf("lifecycle events = %+v", hooks.events)
	}
}

func TestSessionHooksUsePreparedADKIdentity(t *testing.T) {
	hooks := &lifecycleHooks{}
	manager := hookTestManager(t, hooks)
	service := adksession.InMemoryService()
	prepared, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName:   baldaRuntimeAppName,
		UserID:    "user",
		SessionID: "prepared-adk-identity",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.runtimeManager.(*fakeScopedRuntimeManager).sessionRuntime.SessionSvc = service
	manager.agentBuilder.(*fakeAgentBuilder).runtimeSession = prepared.Session
	locator := testTelegramLocator(101, 0)
	if err := manager.CreateSession(t.Context(), SessionContext{Locator: locator, UserID: "user"}, "balda"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(hooks.events) != 2 {
		t.Fatalf("events = %+v, want start and end", hooks.events)
	}
	for _, event := range hooks.events {
		if event.AgentSessionID != prepared.Session.ID() {
			t.Fatalf("%s hook ADK identity = %q, want %q", event.Name, event.AgentSessionID, prepared.Session.ID())
		}
	}
}

func TestSessionStartHookDenialPreventsActivation(t *testing.T) {
	hooks := &lifecycleHooks{deny: true}
	manager := hookTestManager(t, hooks)
	locator := testTelegramLocator(100, 0)
	err := manager.CreateSession(t.Context(), SessionContext{Locator: locator, UserID: "user"}, "balda")
	if !errors.Is(err, hookcmd.ErrDenied) {
		t.Fatalf("CreateSession() error = %v, want denied", err)
	}
	if _, err := manager.GetSession(locator); err == nil {
		t.Fatal("denied session became active")
	}
	if len(hooks.events) != 1 {
		t.Fatalf("events = %+v, want one denied start", hooks.events)
	}
}
