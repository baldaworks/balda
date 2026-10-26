package session

import (
	"context"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/v2/session"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

func TestFinalRuntimeStateReadDoesNotRestore(t *testing.T) {
	f := newArchiveFixture(t)
	original := f.manager()
	first, err := original.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	fresh := f.manager()
	value, found, err := fresh.ActiveRuntimeStateValue(t.Context(), archiveInput().Locator, chatarchivecmd.EpochStateKey)
	if err != nil || found || value != nil {
		t.Fatalf("inactive read=%v %t %v", value, found, err)
	}
	if len(fresh.sessions) != 0 {
		t.Fatal("read restored runtime")
	}
	changed, err := fresh.UpdateRuntimeStateForArchive(t.Context(), archiveInput().Locator, first.GetArchiveID(), map[string]any{"final-format": "old"})
	if err != nil || changed || len(fresh.sessions) != 0 {
		t.Fatalf("inactive update=%t %v", changed, err)
	}
}

func TestFinalRuntimeStateWriteFencesReplacement(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ResetSession(t.Context(), archiveInput().Locator); err != nil {
		t.Fatal(err)
	}
	next, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := m.UpdateRuntimeStateForArchive(t.Context(), archiveInput().Locator, first.GetArchiveID(), map[string]any{"final-format": "old"})
	if err != nil || changed {
		t.Fatalf("old update=%t %v", changed, err)
	}
	if _, found, err := next.RuntimeStateValue(t.Context(), "final-format"); err != nil || found {
		t.Fatalf("replacement polluted=%t %v", found, err)
	}
	changed, err = m.UpdateRuntimeStateForArchive(t.Context(), archiveInput().Locator, next.GetArchiveID(), map[string]any{"final-format": "current"})
	if err != nil || !changed {
		t.Fatalf("current update=%t %v", changed, err)
	}
	if value, found, err := next.RuntimeStateValue(t.Context(), "final-format"); err != nil || !found || value != "current" {
		t.Fatalf("current=%v %t %v", value, found, err)
	}
	if value, found, matched, err := m.ActiveRuntimeStateValueForArchive(t.Context(), archiveInput().Locator, first.GetArchiveID(), "final-format"); err != nil || matched || found || value != nil {
		t.Fatalf("old guarded read=%v found=%t matched=%t error=%v", value, found, matched, err)
	}
	if value, found, matched, err := m.ActiveRuntimeStateValueForArchive(t.Context(), archiveInput().Locator, next.GetArchiveID(), "final-format"); err != nil || !matched || !found || value != "current" {
		t.Fatalf("current guarded read=%v found=%t matched=%t error=%v", value, found, matched, err)
	}
}

func TestFinalReplayResumesExactLifetimeForBookkeeping(t *testing.T) {
	f := newArchiveFixture(t)
	first, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	freshCalls := f.builder.freshCalls
	restarted := f.manager()
	resumed, err := restarted.ResumeArchiveRuntime(t.Context(), archiveInput().Locator, first.GetArchiveID())
	if err != nil || !resumed {
		t.Fatalf("resume=%t %v", resumed, err)
	}
	if f.builder.freshCalls != freshCalls {
		t.Fatal("final recovery created fresh runtime")
	}
	changed, err := restarted.UpdateRuntimeStateForArchive(t.Context(), archiveInput().Locator, first.GetArchiveID(), map[string]any{"final-format": "recovered"})
	if err != nil || !changed {
		t.Fatalf("bookkeeping=%t %v", changed, err)
	}
	value, found, err := restarted.ActiveRuntimeStateValue(t.Context(), archiveInput().Locator, "final-format")
	if err != nil || !found || value != "recovered" {
		t.Fatalf("recovered=%v %t %v", value, found, err)
	}
}

func TestFinalReplayExactRestoreFailureDoesNotCreateFallback(t *testing.T) {
	f := newArchiveFixture(t)
	first, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.provider.RuntimeSessions().Delete(t.Context(), &adksession.DeleteRequest{AppName: f.runtime.AppName, UserID: first.GetUserID(), SessionID: first.GetAgentSessionID()}); err != nil {
		t.Fatal(err)
	}
	freshCalls := f.builder.freshCalls
	restarted := f.manager()
	resumed, err := restarted.ResumeArchiveRuntime(t.Context(), archiveInput().Locator, first.GetArchiveID())
	if err == nil || resumed || f.builder.freshCalls != freshCalls || len(restarted.sessions) != 0 {
		t.Fatalf("resume=%t error=%v creates=%d", resumed, err, f.builder.freshCalls-freshCalls)
	}
	old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || old.Status != chatarchivecmd.StatusActive {
		t.Fatalf("old lifetime changed=%+v %v", old, err)
	}
}

type blockedFinalActivation struct {
	ArchiveLifecycle
	entered, release chan struct{}
}

func (a *blockedFinalActivation) Activate(ctx context.Context, token string, live *baldastate.SessionRecord) (chatarchivecmd.Session, error) {
	close(a.entered)
	<-a.release
	return a.ArchiveLifecycle.Activate(ctx, token, live)
}
func TestFinalReplayRestoreParticipatesInShutdown(t *testing.T) {
	f := newArchiveFixture(t)
	first, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	restarted := f.manager()
	blocked := &blockedFinalActivation{ArchiveLifecycle: f.provider.ChatArchive(), entered: make(chan struct{}), release: make(chan struct{})}
	restarted.archive = blocked
	resumeDone := make(chan error, 1)
	go func() {
		_, err := restarted.ResumeArchiveRuntime(t.Context(), archiveInput().Locator, first.GetArchiveID())
		resumeDone <- err
	}()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not reach activation")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- restarted.Stop(t.Context()) }()
	select {
	case err := <-stopDone:
		t.Errorf("shutdown finished during retained restore: %v", err)
		stopDone <- err
	case <-time.After(30 * time.Millisecond):
	}
	close(blocked.release)
	if err := <-resumeDone; err != nil {
		t.Error(err)
	}
	if err := <-stopDone; err != nil {
		t.Error(err)
	}
	restarted.mu.RLock()
	count := len(restarted.sessions)
	restarted.mu.RUnlock()
	if count != 0 {
		t.Fatalf("runtimes published after shutdown=%d", count)
	}
}
