package session

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

type lifecycleHooks struct {
	events []hookcmd.Event
	deny   bool
}

func TestSessionStartDenialPreservesPersistedRestore(t *testing.T) {
	hooks := &lifecycleHooks{deny: true}
	manager := hookTestManager(t, hooks)
	locator := testTelegramLocator(100, 0)
	record := baldastate.SessionRecord{SessionID: locator.SessionID, UserID: "user", ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "balda", RuntimeSnapshotID: currentSnapshotID, Status: baldastate.SessionStatusActive}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{sessionAddressKey(locator.ChannelType, locator.AddressKey): record}}
	manager.sessionStore = store
	_, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator, UserID: "user"})
	if !errors.Is(err, hookcmd.ErrDenied) {
		t.Fatalf("RestoreSession() error = %v, want denied", err)
	}
	if store.deletedSessionID != "" || len(store.upsertedRecords) != 0 {
		t.Fatal("denied restore modified persisted session metadata")
	}
	if _, err := manager.GetSession(locator); err == nil {
		t.Fatal("denied restore became active")
	}
	if len(hooks.events) != 1 || hooks.events[0].Source != "resume" {
		t.Fatalf("events = %+v, want denied resume", hooks.events)
	}
}

func (h *lifecycleHooks) Run(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	h.events = append(h.events, event)
	if h.deny {
		return hookcmd.Result{}, hookcmd.ErrDenied
	}
	return hookcmd.Result{}, nil
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
