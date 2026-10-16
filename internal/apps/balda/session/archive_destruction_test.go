package session

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	adksession "google.golang.org/adk/v2/session"
)

type destructionFault struct {
	ArchiveLifecycle
	completeErr error
}

func (a *destructionFault) Complete(ctx context.Context, token, outcome string) error {
	if a.completeErr != nil {
		return a.completeErr
	}
	return a.ArchiveLifecycle.Complete(ctx, token, outcome)
}
func TestArchiveResetEndsOnlyDeletedLifetime(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ResetSession(t.Context(), archiveInput().Locator); err != nil {
		t.Fatal(err)
	}
	old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || old.Status != chatarchivecmd.StatusEnded || old.EndReason != "reset" {
		t.Fatalf("old archive=%#v, %v", old, err)
	}
	next, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if next.GetArchiveID() == first.GetArchiveID() || next.GetAgentSessionID() != first.GetAgentSessionID() {
		t.Fatal("reset did not split archive while preserving routing")
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), next.GetArchiveID())
	if err != nil || row.PredecessorID != first.GetArchiveID() {
		t.Fatalf("replacement=%#v, %v", row, err)
	}
}
func TestArchiveDestructionIntentFailurePreservesRuntime(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	m.archive = &archiveFault{ArchiveLifecycle: m.archive, beginErr: chatarchivecmd.ErrUnavailable}
	if err := m.ResetSession(t.Context(), archiveInput().Locator); !errors.Is(err, chatarchivecmd.ErrUnavailable) {
		t.Fatalf("reset=%v", err)
	}
	if _, err := f.runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: ensureTestUserID, SessionID: first.GetAgentSessionID()}); err != nil {
		t.Fatal("runtime deleted without intent", err)
	}
}
func TestArchiveDestructionCompletionRecovery(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	fault := &destructionFault{ArchiveLifecycle: m.archive, completeErr: chatarchivecmd.ErrUnavailable}
	m.archive = fault
	if err := m.ResetSession(t.Context(), archiveInput().Locator); !errors.Is(err, chatarchivecmd.ErrUnavailable) {
		t.Fatalf("reset=%v", err)
	}
	pending, err := f.provider.ChatArchive().Pending(t.Context(), "telegram", "10:0")
	if err != nil || pending.Kind != chatarchivecmd.KindDestroy {
		t.Fatalf("pending=%#v, %v", pending, err)
	}
	next, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if next.GetArchiveID() == first.GetArchiveID() {
		t.Fatal("deleted lifetime reused")
	}
	old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || old.Status != chatarchivecmd.StatusEnded {
		t.Fatalf("old=%#v, %v", old, err)
	}
}

func TestArchiveFailedDeleteRetainsLifetime(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	first.sessionSvc = &trackingSessionService{Service: first.sessionSvc, deleteErr: errors.New("delete failed")}
	if err := m.ResetSession(t.Context(), archiveInput().Locator); err == nil {
		t.Fatal("failed delete accepted")
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || row.Status != chatarchivecmd.StatusActive {
		t.Fatalf("retained=%#v, %v", row, err)
	}
	got, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil || got != first {
		t.Fatalf("retained runtime=%p, %v", got, err)
	}
}
func TestArchiveFallbackEndsOldBeforeFailedReplacement(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	// A supported failed exact open forces the normal fallback, independently of archive recovery.
	if err := f.runtime.SessionSvc.Delete(t.Context(), &adksession.DeleteRequest{AppName: baldaRuntimeAppName, UserID: ensureTestUserID, SessionID: first.GetAgentSessionID()}); err != nil {
		t.Fatal(err)
	}
	f.builder.fail = true
	if _, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
		t.Fatal("failed replacement accepted")
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || row.Status != chatarchivecmd.StatusEnded || row.EndReason != "rotation" {
		t.Fatalf("fallback old=%#v, %v", row, err)
	}
	rows, err := f.provider.ChatArchive().List(t.Context(), chatarchivecmd.Filter{Status: chatarchivecmd.StatusActive, Limit: 50})
	if err != nil || len(rows) != 0 {
		t.Fatalf("active=%#v, %v", rows, err)
	}
}
func TestArchiveCloseRetainsTranscriptAndNextLifetime(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.provider.ChatArchive().Record(t.Context(), chatarchivecmd.Message{SessionID: first.GetArchiveID(), Source: "original", Role: "user", Text: "retain me", ObservedAt: m.currentTime()}); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetSessionWithReason(t.Context(), archiveInput().Locator, BoundaryReasonClose); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved"); err != nil {
		t.Fatal(err)
	}
	msgs, err := f.provider.ChatArchive().Messages(t.Context(), first.GetArchiveID(), 0, 100)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "retain me" {
		t.Fatalf("retained messages=%#v, %v", msgs, err)
	}
}

func TestArchiveMemoryBootSeparatesReusedRuntimeID(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	m.sessionsPersistent = false
	m.runtimeManager = &fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: adksession.InMemoryService(), RuntimeSnapshotID: currentSnapshotID}}
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	// Process-local runtime state is gone; only the selected SQL provider survives.
	nextRuntime := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: adksession.InMemoryService(), RuntimeSnapshotID: currentSnapshotID}
	next, err := NewManager(ManagerParams{Archive: f.provider.ChatArchive(), AgentBuilder: f.builder, RuntimeManager: &fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: nextRuntime}, SessionStore: f.provider.Sessions(), WorkingDir: f.dir, Logger: m.logger})
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || old.Status != chatarchivecmd.StatusEnded || old.EndReason != "interrupted" {
		t.Fatalf("former memory=%#v, %v", old, err)
	}
	second, err := next.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetArchiveID() == first.GetArchiveID() || second.GetAgentSessionID() != first.GetAgentSessionID() {
		t.Fatalf("memory identities old=%s/%s new=%s/%s", first.GetArchiveID(), first.GetAgentSessionID(), second.GetArchiveID(), second.GetAgentSessionID())
	}
}

func TestArchivePersistentSuspensionAndShutdownPreserveLifetime(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "suspension", true: "shutdown"}[shutdown], func(t *testing.T) {
			f := newArchiveFixture(t)
			m := f.manager()
			first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			if shutdown {
				m.stopAllWithContext(t.Context())
			} else {
				m.StopSession(archiveInput().Locator)
			}
			restored, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			if restored.GetArchiveID() != first.GetArchiveID() {
				t.Fatal("non-destructive unload split lifetime")
			}
		})
	}
}
func TestArchiveOptionalObserverFailureDoesNotHideDeletion(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	observerErr := errors.New("optional observer failed")
	m.boundaryObserver = &fakeBoundaryObserver{err: observerErr}
	if err := m.ResetSession(t.Context(), archiveInput().Locator); !errors.Is(err, observerErr) {
		t.Fatalf("reset=%v", err)
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || row.Status != chatarchivecmd.StatusEnded {
		t.Fatalf("deleted=%#v, %v", row, err)
	}
}
func TestArchiveDestructionContradictoryEpochStaysUncertain(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := m.beginArchiveDestruction(t.Context(), first.GetArchiveID(), BoundaryReasonReset)
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "other-epoch")
	event.Author = archiveStateAuthor
	event.Actions.StateDelta = map[string]any{chatarchivecmd.EpochStateKey: "contradictory"}
	if err := first.sessionSvc.AppendEvent(t.Context(), first.sess, event); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved"); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("uncertain prepare=%v", err)
	}
	pending, err := f.provider.ChatArchive().Pending(t.Context(), "telegram", "10:0")
	if err != nil || pending.Token != intent.Token || pending.Outcome != chatarchivecmd.StatusUncertain {
		t.Fatalf("uncertain=%#v, %v", pending, err)
	}
	if f.builder.freshCalls != 1 {
		t.Fatal("uncertainty recreated runtime")
	}
}

func TestArchiveResetRetryAfterCompletionFailure(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	if _, err := m.EnsureSession(t.Context(), archiveInput(), "saved"); err != nil {
		t.Fatal(err)
	}
	fault := &destructionFault{ArchiveLifecycle: m.archive, completeErr: chatarchivecmd.ErrUnavailable}
	m.archive = fault
	if err := m.ResetSession(t.Context(), archiveInput().Locator); !errors.Is(err, chatarchivecmd.ErrUnavailable) {
		t.Fatalf("first reset=%v", err)
	}
	fault.completeErr = nil
	if err := m.ResetSession(t.Context(), archiveInput().Locator); err != nil {
		t.Fatalf("retry completed deletion=%v", err)
	}
	if f.builder.freshCalls != 1 {
		t.Fatal("reset retry unexpectedly created runtime")
	}
}

type uncertainDeletionService struct{ adksession.Service }

func (s uncertainDeletionService) Delete(ctx context.Context, r *adksession.DeleteRequest) error {
	if err := s.Service.Delete(ctx, r); err != nil {
		return err
	}
	return errors.New("delete receipt lost")
}
func (s uncertainDeletionService) Get(context.Context, *adksession.GetRequest) (*adksession.GetResponse, error) {
	return nil, errors.New("runtime read unavailable")
}
func TestArchiveLateDestructionEvidenceEvictsCachedRuntime(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	first.sessionSvc = uncertainDeletionService{Service: first.sessionSvc}
	if err := m.ResetSession(t.Context(), archiveInput().Locator); err == nil {
		t.Fatal("uncertain reset accepted")
	}
	next, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if next == first || next.GetArchiveID() == first.GetArchiveID() {
		t.Fatal("proved deleted runtime returned from cache")
	}
}

func TestArchiveLegacyMemoryMetadataCreatesTruthfulFreshLifetime(t *testing.T) {
	f := newArchiveFixture(t)
	old := f.manager()
	old.sessionsPersistent = false
	old.archive = nil
	old.runtimeManager = &fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: adksession.InMemoryService(), RuntimeSnapshotID: currentSnapshotID}}
	if _, err := old.EnsureSession(t.Context(), archiveInput(), "saved"); err != nil {
		t.Fatal(err)
	}
	root := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: adksession.InMemoryService(), RuntimeSnapshotID: currentSnapshotID}
	next, err := NewManager(ManagerParams{Archive: f.provider.ChatArchive(), AgentBuilder: f.builder, RuntimeManager: &fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: root}, SessionStore: f.provider.Sessions(), WorkingDir: f.dir, Logger: old.logger})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := next.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := root.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: ensureTestUserID, SessionID: sess.GetAgentSessionID()})
	if err != nil {
		t.Fatalf("published runtime identity missing: %v", err)
	}
	if archiveStateString(runtime.Session, chatarchivecmd.EpochStateKey) != sess.GetArchiveID() {
		t.Fatal("runtime epoch differs from fresh archive")
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), sess.GetArchiveID())
	if err != nil || row.RuntimeID != sess.GetAgentSessionID() || row.Partial {
		t.Fatalf("fresh memory upgrade=%#v, %v", row, err)
	}
	if err := next.ResetSession(t.Context(), archiveInput().Locator); err != nil {
		t.Fatal(err)
	}
	if _, err := root.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: ensureTestUserID, SessionID: sess.GetAgentSessionID()}); !errors.Is(err, adksession.ErrNotFound) {
		t.Fatalf("actual runtime survived reset: %v", err)
	}
}

func TestArchiveFallbackCreatesDistinctEpochAtSameRuntimeID(t *testing.T) {
	f := newArchiveFixture(t)
	first, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an exact snapshot that can no longer open its runtime.
	event := adksession.NewEvent(t.Context(), "lost-binding")
	event.Author = archiveStateAuthor
	event.Actions.StateDelta = map[string]any{chatarchivecmd.EpochStateKey: "unavailable-epoch"}
	if err := first.sessionSvc.AppendEvent(t.Context(), first.sess, event); err != nil {
		t.Fatal(err)
	}
	second, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetArchiveID() == first.GetArchiveID() || second.GetAgentSessionID() != first.GetAgentSessionID() {
		t.Fatal("fallback conflated archive or changed runtime route")
	}
	old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || old.Status != chatarchivecmd.StatusEnded || old.EndReason != "rotation" {
		t.Fatalf("fallback old=%#v, %v", old, err)
	}
	current, err := f.provider.ChatArchive().Get(t.Context(), second.GetArchiveID())
	if err != nil || current.PredecessorID != old.ID {
		t.Fatalf("fallback replacement=%#v, %v", current, err)
	}
}
func TestArchiveRetainedDestructionCompletionRecoversWithoutHooks(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	hooks := &archiveHooks{}
	m.hooks = hooks
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	first.sessionSvc = &trackingSessionService{Service: first.sessionSvc, deleteErr: errors.New("delete failed")}
	fault := &destructionFault{ArchiveLifecycle: m.archive, completeErr: chatarchivecmd.ErrUnavailable}
	m.archive = fault
	if err := m.ResetSession(t.Context(), archiveInput().Locator); err == nil {
		t.Fatal("failed reset accepted")
	}
	fault.completeErr = nil
	same, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil || same != first || hooks.calls != 1 {
		t.Fatalf("retained=%p hooks=%d, %v", same, hooks.calls, err)
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || row.Status != chatarchivecmd.StatusActive {
		t.Fatalf("retained row=%#v, %v", row, err)
	}
}
