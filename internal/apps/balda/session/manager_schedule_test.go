package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adksession "google.golang.org/adk/v2/session"
)

type cleanupWorkspaceFixture struct {
	root    string
	cleaned string
	deleted string
}

func (w *cleanupWorkspaceFixture) CanonicalWorkspaceDir(key string) string {
	return filepath.Join(w.root, key)
}

func (*cleanupWorkspaceFixture) ForceRemountCanonicalWorkspace(context.Context, string, string) (EnsureWorkspaceResult, error) {
	return EnsureWorkspaceResult{}, nil
}

func (*cleanupWorkspaceFixture) EnsureWorkspace(context.Context, string, string, string) (EnsureWorkspaceResult, error) {
	return EnsureWorkspaceResult{}, nil
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
