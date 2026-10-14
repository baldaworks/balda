package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

type cleanupWorkspaceFixture struct {
	root         string
	ensureResult EnsureWorkspaceResult
	cleaned      string
	deleted      string
}

func (w *cleanupWorkspaceFixture) CanonicalWorkspaceDir(key string) string {
	return filepath.Join(w.root, key)
}

func (w *cleanupWorkspaceFixture) ForceRemountCanonicalWorkspace(context.Context, string, string) (EnsureWorkspaceResult, error) {
	return w.ensureResult, nil
}

func (w *cleanupWorkspaceFixture) EnsureWorkspace(context.Context, string, string, string) (EnsureWorkspaceResult, error) {
	return w.ensureResult, nil
}

func (*cleanupWorkspaceFixture) Import(context.Context, string) error                 { return nil }
func (*cleanupWorkspaceFixture) Export(context.Context, string, string, string) error { return nil }

func (w *cleanupWorkspaceFixture) CleanupWorkspace(_ context.Context, path string) error {
	w.cleaned = path
	return os.RemoveAll(path)
}

func (w *cleanupWorkspaceFixture) DeleteBranch(_ context.Context, branchName string) error {
	w.deleted = branchName
	return nil
}

func TestFailedSessionCreationPreservesReusedWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaces := &cleanupWorkspaceFixture{
		root:         filepath.Dir(path),
		ensureResult: EnsureWorkspaceResult{Dir: path, Created: false},
	}
	manager := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    &fakeBaldaRuntimeManager{runtimeErr: context.Canceled},
		agentBuilder:      &fakeAgentBuilder{},
		workingDir:        t.TempDir(),
		workspaces:        workspaces,
		workspaceEnabled:  true,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      &fakeSessionStore{},
	}

	if _, err := manager.EnsureSession(t.Context(), SessionContext{
		Locator: testTelegramLocator(10, 42), UserID: "tg-101",
	}, "topic"); err == nil {
		t.Fatal("EnsureSession() error = nil, want runtime failure")
	}
	if workspaces.cleaned != "" {
		t.Fatalf("reused workspace was cleaned: %q", workspaces.cleaned)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reused workspace stat error = %v", err)
	}
}

func TestCloseRunSessionAfterRestartCleansWorkspace(t *testing.T) {
	const sessionID = "sch-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	workspaces := &cleanupWorkspaceFixture{root: t.TempDir()}
	path := workspaces.CanonicalWorkspaceDir(sessionID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	service := adksession.InMemoryService()
	if _, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "schedule-user", SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{runtimeManager: &fakeBaldaRuntimeManager{
		runtime: &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service},
	}, sessions: make(map[string]*TopicSession), workspaceEnabled: true, workspaces: workspaces}
	if err := manager.CloseRunSession(t.Context(), sessionID, "schedule-user"); err != nil {
		t.Fatal(err)
	}
	if workspaces.cleaned != path {
		t.Fatalf("cleaned workspace = %q, want %q", workspaces.cleaned, path)
	}
	if workspaces.deleted != "norma/balda/"+sessionID {
		t.Fatalf("deleted branch = %q", workspaces.deleted)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workspace remains after cleanup: %v", err)
	}
	workspaces.deleted = ""
	if err := manager.CloseRunSession(t.Context(), sessionID, "schedule-user"); err != nil {
		t.Fatal(err)
	}
	if workspaces.deleted != "norma/balda/"+sessionID {
		t.Fatalf("retry did not clean branch: %q", workspaces.deleted)
	}
}

func TestCloseRunSessionCleansOldWorkspaceAfterModeDisabled(t *testing.T) {
	const sessionID = "sch-cccccccccccccccccccccccccccccccc"
	workingDir := t.TempDir()
	initGitRepo(t, t.Context(), workingDir)
	workspaces := &cleanupWorkspaceFixture{root: t.TempDir()}
	path := workspaces.CanonicalWorkspaceDir(sessionID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	service := adksession.InMemoryService()
	if _, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "schedule-user", SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{runtimeManager: &fakeBaldaRuntimeManager{
		runtime: &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service},
	}, sessions: make(map[string]*TopicSession), workspaceEnabled: false,
		workspaces: workspaces, workingDir: workingDir}
	if err := manager.CloseRunSession(t.Context(), sessionID, "schedule-user"); err != nil {
		t.Fatal(err)
	}
	if workspaces.deleted != "norma/balda/"+sessionID {
		t.Fatalf("old workspace branch was not deleted: %q", workspaces.deleted)
	}
	if workspaces.cleaned != path {
		t.Fatalf("old workspace was not cleaned: %q", workspaces.cleaned)
	}
}

func TestCloseActiveWebhookRunPublishesPrivateBoundaryAndDeletesRuntime(t *testing.T) {
	const sessionID = "wh-dddddddddddddddddddddddddddddddd"
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: sessionID, SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	locator := SessionLocator{ChannelType: "webhook", AddressKey: sessionID,
		AddressJSON: "{}", SessionID: sessionID}
	observer := &fakeBoundaryObserver{}
	manager := &Manager{
		sessions: map[string]*TopicSession{sessionID: {
			sessionID: sessionID, userID: sessionID, locator: locator,
			sessionSvc: service, sess: created.Session,
		}},
		boundaryObserver: observer,
	}
	if err := manager.CloseRunSession(t.Context(), sessionID, sessionID); err != nil {
		t.Fatal(err)
	}
	if len(observer.boundaries) != 1 || observer.boundaries[0].Locator.ChannelType != "webhook" ||
		observer.boundaries[0].Reason != BoundaryReasonClose {
		t.Fatalf("close boundary = %+v", observer.boundaries)
	}
	if _, err := service.Get(t.Context(), &adksession.GetRequest{
		AppName: baldaRuntimeAppName, UserID: sessionID, SessionID: sessionID,
	}); err == nil {
		t.Fatal("active webhook runtime session survived close")
	}
}
