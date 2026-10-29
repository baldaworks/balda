package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

const currentSnapshotID = "snapshot-current"

func TestStopAllWithContext_CleansWorkspaceWhenRootContextCanceled(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	initGitRepo(t, ctx, workingDir)

	writeFile(t, filepath.Join(workingDir, "seed.txt"), "seed\n")
	runGit(t, ctx, workingDir, "add", "seed.txt")
	runGit(t, ctx, workingDir, "commit", "-m", "chore: seed")

	workspaceDir := filepath.Join(t.TempDir(), "balda-workspace")
	runGit(t, ctx, workingDir, "worktree", "add", "-b", "norma/balda/tg-1-1", workspaceDir, "HEAD")

	m := &Manager{
		workspaces:       newTestWorkspaceManager(workingDir, t.TempDir(), "master"),
		workspaceEnabled: true,
		logger:           zerolog.Nop(),
		sessions: map[string]*TopicSession{
			"tg-1-1": withTestRuntime(&TopicSession{
				sessionID:    "tg-1-1",
				locator:      testTelegramLocator(1, 1),
				workspaceDir: workspaceDir,
			}, nil, nil, nil),
		},
	}

	m.stopAllWithContext(context.Background())

	if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after StopAll; stat err = %v", err)
	}
}

func TestStopAllHidesEverySessionBeforeCleanup(t *testing.T) {
	first := testTelegramLocator(10, 42)
	second := testTelegramLocator(11, 43)
	cleanupStarted := make(chan string, 1)
	releaseCleanup := make(chan struct{})
	closeRuntime := func(sessionID string) func() error {
		return func() error {
			cleanupStarted <- sessionID
			<-releaseCleanup
			return nil
		}
	}
	m := &Manager{
		logger: zerolog.Nop(),
		sessions: map[string]*TopicSession{
			first.SessionID:  withTestRuntime(&TopicSession{sessionID: first.SessionID, locator: first}, nil, nil, closeRuntime(first.SessionID)),
			second.SessionID: withTestRuntime(&TopicSession{sessionID: second.SessionID, locator: second}, nil, nil, closeRuntime(second.SessionID)),
		},
	}
	stopped := make(chan struct{})
	go func() {
		m.stopAllWithContext(context.Background())
		close(stopped)
	}()
	var waitingOn string
	select {
	case waitingOn = <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown cleanup did not start")
	}
	other := first
	if waitingOn == first.SessionID {
		other = second
	}
	_, lookupErr := m.GetSession(other)
	if lookupErr == nil {
		t.Error("session remained visible after shutdown cleanup began")
	}
	close(releaseCleanup)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown cleanup did not settle")
	}
}

func TestStopAllDoesNotRepeatCompletedResetCleanup(t *testing.T) {
	ctx := context.Background()
	locator := testTelegramLocator(10, 42)
	service := &blockingDeleteSessionService{
		Service:      adksession.InMemoryService(),
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	created, err := service.Create(ctx, &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var closeCalls atomic.Int32
	observer := &fakeBoundaryObserver{}
	m := &Manager{
		logger: zerolog.Nop(), sessionStore: &fakeSessionStore{}, boundaryObserver: observer,
		sessions: map[string]*TopicSession{locator.SessionID: withTestRuntime(&TopicSession{
			sessionID: locator.SessionID, userID: "tg-101", locator: locator,
		}, service, created.Session, func() error { closeCalls.Add(1); return nil })},
	}
	resetDone := make(chan error, 1)
	go func() { resetDone <- m.ResetSession(ctx, locator) }()
	select {
	case <-service.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("reset did not start runtime deletion")
	}
	stopDone := make(chan struct{})
	go func() {
		m.stopAllWithContext(ctx)
		close(stopDone)
	}()
	deadline := time.After(time.Second)
	for {
		m.mu.RLock()
		remaining := len(m.sessions)
		m.mu.RUnlock()
		if remaining == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shutdown did not hide the active session")
		case <-time.After(time.Millisecond):
		}
	}
	close(service.releaseFirst)
	if err := <-resetDone; err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown cleanup did not settle")
	}
	if got := service.deleteCalls.Load(); got != 1 {
		t.Fatalf("runtime deletes = %d, want 1", got)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("scoped runtime closes = %d, want 1", got)
	}
	if len(observer.boundaries) != 1 || observer.boundaries[0].Reason != BoundaryReasonReset {
		t.Fatalf("boundaries after completed reset and shutdown = %+v, want one reset", observer.boundaries)
	}
}

func TestStopSession_UsesNonCanceledCleanupContext(t *testing.T) {
	store := &fakeSessionStore{}
	m := &Manager{
		logger:       zerolog.Nop(),
		sessionStore: store,
		sessions: map[string]*TopicSession{
			"tg-10-42": withTestRuntime(&TopicSession{
				sessionID: "tg-10-42",
				locator:   testTelegramLocator(10, 42),
			}, nil, nil, nil),
		},
	}

	m.StopSession(testTelegramLocator(10, 42))

	if store.deletedSessionID != "tg-10-42" {
		t.Fatalf("DeleteBySessionID called with %q, want %q", store.deletedSessionID, "tg-10-42")
	}
	if store.deleteCtxErr != nil {
		t.Fatalf("DeleteBySessionID ctx was canceled: %v", store.deleteCtxErr)
	}
}

func TestStopSession_PersistentModeSuspendsWithoutDeletingMetadata(t *testing.T) {
	store := &fakeSessionStore{}
	m := &Manager{
		logger:             zerolog.Nop(),
		sessionStore:       store,
		sessionsPersistent: true,
		sessions: map[string]*TopicSession{
			"tg-10-42": withTestRuntime(&TopicSession{
				sessionID: "tg-10-42",
				locator:   testTelegramLocator(10, 42),
			}, nil, nil, nil),
		},
	}

	m.StopSession(testTelegramLocator(10, 42))

	if store.deletedSessionID != "" {
		t.Fatalf("DeleteBySessionID called with %q, want no delete", store.deletedSessionID)
	}
	if _, ok := m.sessions["tg-10-42"]; ok {
		t.Fatal("session still active after StopSession")
	}
}

func TestStopSessionPublishesOneCloseBoundary(t *testing.T) {
	observer := &fakeBoundaryObserver{}
	store := &fakeSessionStore{}
	locator := testTelegramLocator(10, 42)
	m := &Manager{
		logger:           zerolog.Nop(),
		sessionStore:     store,
		boundaryObserver: observer,
		sessions: map[string]*TopicSession{
			locator.SessionID: withTestRuntime(&TopicSession{
				sessionID: locator.SessionID,
				locator:   locator,
			}, nil, nil, nil),
		},
	}

	m.StopSession(locator)

	if len(observer.boundaries) != 1 {
		t.Fatalf("close boundaries = %d, want 1", len(observer.boundaries))
	}
	if observer.boundaries[0].Reason != BoundaryReasonClose {
		t.Fatalf("boundary reason = %q, want close", observer.boundaries[0].Reason)
	}
}

func TestResetSession_DeletesRuntimeHistoryAndPreservesMetadata(t *testing.T) {
	ctx := context.Background()
	store := &fakeSessionStore{}
	svc := adksession.InMemoryService()
	created, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-10-42",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	m := &Manager{
		logger:       zerolog.Nop(),
		sessionStore: store,
		sessions: map[string]*TopicSession{
			"tg-10-42": withTestRuntime(&TopicSession{
				sessionID: "tg-10-42",
				userID:    "tg-101",
				locator:   testTelegramLocator(10, 42),
			}, svc, created.Session, nil),
		},
	}

	if err := m.ResetSession(ctx, testTelegramLocator(10, 42)); err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	if store.deletedSessionID != "" {
		t.Fatalf("DeleteBySessionID called with %q, want metadata preserved", store.deletedSessionID)
	}
	if _, ok := m.sessions["tg-10-42"]; ok {
		t.Fatal("session still active after ResetSession")
	}
	if _, err := svc.Get(ctx, &adksession.GetRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-10-42",
	}); err == nil {
		t.Fatal("runtime session still exists after ResetSession")
	}
}

func TestResetSessionDeleteTimeoutKeepsSessionForRetry(t *testing.T) {
	ctx := context.Background()
	locator := testTelegramLocator(10, 42)
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	service := &timeoutOnceSessionService{Service: provider.RuntimeSessions()}
	created, err := service.Create(ctx, &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	event := adksession.NewEvent(ctx, "old-turn")
	event.Author = "user"
	if err := service.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	closed := false
	active := &TopicSession{
		sessionID: locator.SessionID,
		userID:    "tg-101",
		locator:   locator,
	}
	withTestRuntime(active, service, created.Session, func() error { closed = true; return nil })
	observer := &fakeBoundaryObserver{}
	m := &Manager{
		logger:           zerolog.Nop(),
		sessionStore:     &fakeSessionStore{},
		boundaryObserver: observer,
		sessions:         map[string]*TopicSession{locator.SessionID: active},
	}

	if err := m.ResetSession(ctx, locator); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first ResetSession() error = %v, want deadline exceeded", err)
	}
	got, err := m.GetSession(locator)
	if err != nil || got != active {
		t.Fatalf("GetSession() after failed reset = %p, %v; want original %p", got, err, active)
	}
	if closed {
		t.Fatal("scoped runtime closed after failed reset")
	}
	oldSession, err := service.Get(ctx, &adksession.GetRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("runtime session after failed reset: %v", err)
	}
	if got := oldSession.Session.Events().Len(); got != 1 {
		t.Fatalf("events after failed reset = %d, want 1", got)
	}

	if err := m.ResetSession(ctx, locator); err != nil {
		t.Fatalf("second ResetSession() error = %v", err)
	}
	if _, err := m.GetSession(locator); err == nil {
		t.Fatal("session still active after successful retry")
	}
	if !closed {
		t.Fatal("scoped runtime not closed after successful retry")
	}
	if _, err := service.Get(ctx, &adksession.GetRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	}); err == nil {
		t.Fatal("old runtime session still exists after retry")
	}
	recreated, err := service.Create(ctx, &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("Create() after retry error = %v", err)
	}
	if got := recreated.Session.Events().Len(); got != 0 {
		t.Fatalf("events in fresh session = %d, want 0", got)
	}
	if len(service.deleteRequests) != 2 {
		t.Fatalf("Delete() calls = %d, want 2", len(service.deleteRequests))
	}
	if len(observer.boundaries) != 1 {
		t.Fatalf("reset boundaries = %d, want 1", len(observer.boundaries))
	}
	for _, req := range service.deleteRequests {
		if req.AppName != baldaRuntimeAppName || req.UserID != "tg-101" || req.SessionID != locator.SessionID {
			t.Fatalf("Delete() request = %+v, want original runtime identity", req)
		}
	}
}

type timeoutOnceSessionService struct {
	adksession.Service
	deleteRequests []adksession.DeleteRequest
}

type trackingSessionService struct {
	adksession.Service
	deleteRequests []adksession.DeleteRequest
	deleteErr      error
	beforeDelete   func()
}

type closeSensitiveSessionService struct {
	adksession.Service
	closed *atomic.Bool
}

func (s *closeSensitiveSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if s.closed.Load() {
		return errors.New("shared root runtime is closed")
	}
	return s.Service.Delete(ctx, req)
}

func (s *trackingSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if s.beforeDelete != nil {
		s.beforeDelete()
	}
	s.deleteRequests = append(s.deleteRequests, *req)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Service.Delete(ctx, req)
}

func TestConcurrentResetsDoNotDeleteTheSameActiveRuntimeTogether(t *testing.T) {
	ctx := context.Background()
	locator := testTelegramLocator(10, 42)
	service := &blockingDeleteSessionService{
		Service:       adksession.InMemoryService(),
		firstEntered:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
	created, err := service.Create(ctx, &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m := &Manager{
		logger:       zerolog.Nop(),
		sessionStore: &fakeSessionStore{},
		sessions: map[string]*TopicSession{locator.SessionID: withTestRuntime(&TopicSession{
			sessionID: locator.SessionID, userID: "tg-101", locator: locator,
		}, service, created.Session, nil)},
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- m.ResetSession(ctx, locator) }()
	select {
	case <-service.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first reset did not start runtime deletion")
	}
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- m.ResetSession(ctx, locator)
	}()
	<-secondStarted
	select {
	case <-service.secondEntered:
		t.Fatal("second reset started deleting the active runtime before the first completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(service.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ResetSession() error = %v", err)
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second reset did not settle")
	}
}

func TestSessionLookupWaitsForFailedResetDeletion(t *testing.T) {
	ctx := context.Background()
	locator := testTelegramLocator(10, 42)
	service := &blockingDeleteSessionService{
		Service:      adksession.InMemoryService(),
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		firstErr:     context.DeadlineExceeded,
	}
	created, err := service.Create(ctx, &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	active := &TopicSession{
		sessionID: locator.SessionID, userID: "tg-101", locator: locator,
	}
	withTestRuntime(active, service, created.Session, nil)
	m := &Manager{
		logger: zerolog.Nop(), sessionStore: &fakeSessionStore{},
		sessions: map[string]*TopicSession{locator.SessionID: active},
	}
	resetDone := make(chan error, 1)
	go func() { resetDone <- m.ResetSession(ctx, locator) }()
	select {
	case <-service.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("reset did not start runtime deletion")
	}
	type lookupResult struct {
		session *TopicSession
		err     error
	}
	lookupDone := make(chan lookupResult, 1)
	lookupStarted := make(chan struct{})
	go func() {
		close(lookupStarted)
		session, err := m.GetSession(locator)
		lookupDone <- lookupResult{session: session, err: err}
	}()
	<-lookupStarted
	select {
	case <-lookupDone:
		t.Fatal("GetSession returned while reset deletion was unresolved")
	case <-time.After(100 * time.Millisecond):
	}
	close(service.releaseFirst)
	if err := <-resetDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResetSession() error = %v, want deadline exceeded", err)
	}
	select {
	case result := <-lookupDone:
		if result.err != nil || result.session != active {
			t.Fatalf("GetSession() = %p, %v; want original %p", result.session, result.err, active)
		}
	case <-time.After(time.Second):
		t.Fatal("GetSession did not resume after failed deletion")
	}
}

func TestStopSessionWaitsForResetDeletion(t *testing.T) {
	ctx := context.Background()
	locator := testTelegramLocator(10, 42)
	service := &blockingDeleteSessionService{
		Service:      adksession.InMemoryService(),
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		firstErr:     context.DeadlineExceeded,
	}
	created, err := service.Create(ctx, &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "tg-101", SessionID: locator.SessionID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	closed := false
	m := &Manager{
		logger: zerolog.Nop(), sessionStore: &fakeSessionStore{}, sessionsPersistent: true,
		sessions: map[string]*TopicSession{locator.SessionID: withTestRuntime(&TopicSession{
			sessionID: locator.SessionID, userID: "tg-101", locator: locator,
		}, service, created.Session, func() error { closed = true; return nil })},
	}
	resetDone := make(chan error, 1)
	go func() { resetDone <- m.ResetSession(ctx, locator) }()
	select {
	case <-service.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("reset did not start runtime deletion")
	}
	stopStarted := make(chan struct{})
	stopDone := make(chan struct{})
	go func() {
		close(stopStarted)
		m.StopSession(locator)
		close(stopDone)
	}()
	<-stopStarted
	select {
	case <-stopDone:
		t.Fatal("StopSession completed while reset deletion was unresolved")
	case <-time.After(100 * time.Millisecond):
	}
	if closed {
		t.Fatal("scoped runtime closed while reset deletion was unresolved")
	}
	close(service.releaseFirst)
	if err := <-resetDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResetSession() error = %v, want deadline exceeded", err)
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("StopSession did not settle after reset deletion")
	}
	if !closed {
		t.Fatal("scoped runtime remained open after StopSession")
	}
}

type blockingDeleteSessionService struct {
	adksession.Service
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
	deleteCalls   atomic.Int32
	firstErr      error
}

func (s *blockingDeleteSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if s.deleteCalls.Add(1) == 1 {
		close(s.firstEntered)
		<-s.releaseFirst
		if s.firstErr != nil {
			return s.firstErr
		}
	} else if s.secondEntered != nil {
		close(s.secondEntered)
	}
	return s.Service.Delete(ctx, req)
}

func (s *timeoutOnceSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	s.deleteRequests = append(s.deleteRequests, *req)
	if len(s.deleteRequests) == 1 {
		return context.DeadlineExceeded
	}
	return s.Service.Delete(ctx, req)
}

func TestResetSessionPublishesBoundaryBeforeRuntimeDeletion(t *testing.T) {
	ctx := context.Background()
	svc := adksession.InMemoryService()
	created, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-10-42",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	observer := &fakeBoundaryObserver{}
	observer.before = func(ctx context.Context, boundary SessionBoundary) {
		if boundary.Reason != BoundaryReasonReset {
			return
		}
		_, getErr := svc.Get(ctx, &adksession.GetRequest{
			AppName:   "norma-balda",
			UserID:    "tg-101",
			SessionID: boundary.AgentSessionID,
		})
		observer.runtimeStillExists = getErr == nil
	}
	m := &Manager{
		logger:           zerolog.Nop(),
		sessionStore:     &fakeSessionStore{},
		boundaryObserver: observer,
		sessions: map[string]*TopicSession{
			"tg-10-42": withTestRuntime(&TopicSession{
				sessionID: "tg-10-42",
				userID:    "tg-101",
				locator:   testTelegramLocator(10, 42),
			}, svc, created.Session, nil),
		},
	}

	if err := m.ResetSession(ctx, testTelegramLocator(10, 42)); err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	if len(observer.boundaries) != 1 {
		t.Fatalf("boundaries = %d, want 1", len(observer.boundaries))
	}
	boundary := observer.boundaries[0]
	if boundary.Reason != BoundaryReasonReset || boundary.TransitionID == "" || boundary.OccurredAt.IsZero() {
		t.Fatalf("boundary = %+v", boundary)
	}
	if !observer.runtimeStillExists {
		t.Fatal("runtime session was deleted before boundary observer ran")
	}
}

func TestResetSessionBoundaryFailureDoesNotPreventRuntimeDeletion(t *testing.T) {
	ctx := context.Background()
	svc := adksession.InMemoryService()
	created, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-10-42",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m := &Manager{
		logger:           zerolog.Nop(),
		sessionStore:     &fakeSessionStore{},
		boundaryObserver: &fakeBoundaryObserver{err: errors.New("publisher unavailable")},
		sessions: map[string]*TopicSession{
			"tg-10-42": withTestRuntime(&TopicSession{
				sessionID: "tg-10-42",
				userID:    "tg-101",
				locator:   testTelegramLocator(10, 42),
			}, svc, created.Session, nil),
		},
	}

	if err := m.ResetSession(ctx, testTelegramLocator(10, 42)); err == nil {
		t.Fatal("ResetSession() error = nil, want boundary failure")
	}
	if _, err := svc.Get(ctx, &adksession.GetRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-10-42",
	}); err == nil {
		t.Fatal("runtime session still exists after failed boundary publication")
	}
}

func TestShutdownBoundaryIsPublishedOnceAcrossLifecycleAndManagerStop(t *testing.T) {
	observer := &fakeBoundaryObserver{}
	locator := testTelegramLocator(10, 42)
	m := &Manager{
		logger:           zerolog.Nop(),
		boundaryObserver: observer,
		sessions: map[string]*TopicSession{
			locator.SessionID: {
				sessionID: locator.SessionID,
				locator:   locator,
				runtime: newRuntimeSlot(runtimeSlotConfig{
					runtime:   &BuiltRuntime{},
					sessionID: "adk-42",
				}),
			},
		},
	}

	if err := m.PublishShutdownBoundaries(context.Background()); err != nil {
		t.Fatalf("PublishShutdownBoundaries() error = %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if len(observer.boundaries) != 1 {
		t.Fatalf("shutdown boundaries = %d, want 1", len(observer.boundaries))
	}
	if observer.boundaries[0].Reason != BoundaryReasonShutdown {
		t.Fatalf("boundary reason = %q, want shutdown", observer.boundaries[0].Reason)
	}
}

func TestGetAgentMetadata_MergesUniqueMCPServers(t *testing.T) {
	m := &Manager{
		agentBuilder: &fakeAgentBuilder{
			agentMetadata: AgentMetadata{
				MCPServers: []string{"balda", "shared"},
			},
		},
		baldaMCPServerIDs: []string{" custom.one ", "shared", "", "custom.two"},
	}

	got := m.GetAgentMetadata("ignored").MCPServers
	want := []string{"balda", "shared", "custom.one", "custom.two"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("GetAgentMetadata().MCPServers = %#v, want %#v", got, want)
	}
}

func TestGetAgentMetadata_PreservesReasoningEffort(t *testing.T) {
	m := &Manager{
		agentBuilder: &fakeAgentBuilder{
			agentMetadata: AgentMetadata{
				Type:            "codex_acp",
				Model:           "gpt-5",
				ReasoningEffort: "high",
				MCPServers:      []string{"balda"},
			},
		},
	}

	got := m.GetAgentMetadata("ignored")
	if got.ReasoningEffort != "high" {
		t.Fatalf("GetAgentMetadata().ReasoningEffort = %q, want high", got.ReasoningEffort)
	}
}

func TestGetSessionInfo_ReturnsPersistedSession(t *testing.T) {
	store := &fakeSessionStore{
		recordsByID: map[string]baldastate.SessionRecord{
			"tg-10-42": {
				SessionID:    "tg-10-42",
				UserID:       "tg-201",
				ChannelType:  baldastate.ChannelTypeTelegram,
				AddressKey:   "10:42",
				AddressJSON:  `{"chat_id":10,"topic_id":42}`,
				AgentName:    "opencode",
				WorkspaceDir: "/tmp/workspace",
				BranchName:   "norma/balda/tg-10-42",
				Status:       baldastate.SessionStatusActive,
			},
		},
	}

	m := &Manager{
		logger:       zerolog.Nop(),
		sessionStore: store,
		sessions:     map[string]*TopicSession{},
	}

	info, err := m.GetSessionInfo(context.Background(), "tg-10-42")
	if err != nil {
		t.Fatalf("GetSessionInfo() error = %v", err)
	}
	if info.SessionID != "tg-10-42" {
		t.Fatalf("GetSessionInfo() = %+v, want session tg-10-42", info)
	}
	if info.UserID != "tg-201" {
		t.Fatalf("GetSessionInfo() user_id = %q, want tg-201", info.UserID)
	}
	if info.Status != sessionStatusPersisted {
		t.Fatalf("GetSessionInfo() status = %q, want %q", info.Status, sessionStatusPersisted)
	}
}

func TestRuntimeStateAccessRestoresPersistedSession(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		run  func(context.Context, *Manager, SessionLocator) error
	}{
		{
			name: "read",
			run: func(ctx context.Context, manager *Manager, locator SessionLocator) error {
				_, _, err := manager.RuntimeStateValue(ctx, locator, "balda_auto_enabled")
				return err
			},
		},
		{
			name: "update",
			run: func(ctx context.Context, manager *Manager, locator SessionLocator) error {
				return manager.UpdateRuntimeState(ctx, locator, map[string]any{"balda_auto_enabled": true})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			locator := testTelegramLocator(10, 42)
			builder := &fakeAgentBuilder{}
			manager := &Manager{
				baldaProviderName: "balda-provider",
				runtimeManager:    &fakeBaldaRuntimeManager{providerID: "balda-provider"},
				agentBuilder:      builder,
				logger:            zerolog.Nop(),
				sessions:          make(map[string]*TopicSession),
				sessionStore: &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
					sessionAddressKey(baldastate.ChannelTypeTelegram, "10:42"): {
						SessionID:   locator.SessionID,
						UserID:      "tg-201",
						ChannelType: baldastate.ChannelTypeTelegram,
						AddressKey:  "10:42",
						AddressJSON: `{"chat_id":10,"topic_id":42}`,
						AgentName:   "balda",
						Status:      baldastate.SessionStatusActive,
					},
				}},
			}

			if err := tt.run(context.Background(), manager, locator); err != nil {
				t.Fatalf("runtime state %s error = %v", tt.name, err)
			}
			if len(builder.createRuntimeSessionSessionIDs) != 1 {
				t.Fatalf("restored runtime session ids = %v, want one", builder.createRuntimeSessionSessionIDs)
			}
			if _, ok := manager.sessions[locator.SessionID]; !ok {
				t.Fatalf("Balda session %q was not restored", locator.SessionID)
			}
		})
	}
}

func TestRestoreSessionReusesActivePersistedSessionWhenLocatorIDChanged(t *testing.T) {
	address := testTelegramLocator(10, 42)
	legacyID := "legacy-session-id"
	active := &TopicSession{sessionID: legacyID, locator: address, kind: KindChat, runtime: newPublishedRuntimeSlot(runtimeSlotConfig{})}
	record := baldastate.SessionRecord{
		SessionID:   legacyID,
		UserID:      "tg-201",
		ChannelType: address.ChannelType,
		AddressKey:  address.AddressKey,
		AddressJSON: address.AddressJSON,
		AgentName:   defaultSessionLabel,
		Status:      baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
		sessionAddressKey(address.ChannelType, address.AddressKey): record,
	}}
	locator := address
	locator.SessionID = "new-session-id"
	m := &Manager{
		logger:       zerolog.Nop(),
		sessions:     map[string]*TopicSession{legacyID: active},
		sessionStore: store,
	}

	got, err := m.RestoreSession(context.Background(), SessionContext{Locator: locator, UserID: "tg-201"})
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if got != active {
		t.Fatalf("RestoreSession() returned %p, want active session %p", got, active)
	}
}

func TestGetSessionInfo_UsesPersistedTransportUserID(t *testing.T) {
	store := &fakeSessionStore{recordsByID: map[string]baldastate.SessionRecord{
		"tg-10-42": {
			SessionID: "tg-10-42", UserID: "tg-201", ChannelType: baldastate.ChannelTypeTelegram,
			AddressKey: "10:42", AddressJSON: `{"chat_id":10,"topic_id":42}`, Status: baldastate.SessionStatusActive,
		},
	}}
	m := &Manager{
		logger:       zerolog.Nop(),
		sessionStore: store,
		sessions: map[string]*TopicSession{
			"tg-10-42": {
				sessionID: "tg-10-42",
				userID:    "tg-101",
				locator:   testTelegramLocator(10, 42),
				agentName: "opencode",
			},
		},
	}

	info, err := m.GetSessionInfo(context.Background(), "tg-10-42")
	if err != nil {
		t.Fatalf("GetSessionInfo() error = %v", err)
	}
	if info.UserID != "tg-201" {
		t.Fatalf("GetSessionInfo() user_id = %q, want tg-201", info.UserID)
	}
}

type fakeAgentBuilder struct {
	agentMetadata                     AgentMetadata
	metadataAgentNames                []string
	runtimeSession                    adksession.Session
	createRuntimeSessionCalls         int
	openRuntimeSessionCalls           int
	freshRuntimeSessionCalls          int
	createRuntimeSessionAgentNames    []string
	createRuntimeSessionUserIDs       []string
	createRuntimeSessionSessionIDs    []string
	createRuntimeSessionWorkspaceDirs []string
	createRuntimeSessionContexts      []RuntimeSessionContext
	createRuntimeSessionErr           error
	openRuntimeSessionErr             error
	beforeFreshRuntimeSession         func()
}

func (f *fakeAgentBuilder) CreateRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	agentName string,
	userID, sessionID, workspaceDir string,
	sessionCtx RuntimeSessionContext,
) (adksession.Session, error) {
	f.createRuntimeSessionCalls++
	return f.recordRuntimeSession(ctx, runtime, agentName, userID, sessionID, workspaceDir, sessionCtx)
}

func (f *fakeAgentBuilder) OpenExistingRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	agentName string,
	userID, sessionID, workspaceDir string,
	sessionCtx RuntimeSessionContext,
) (adksession.Session, error) {
	f.openRuntimeSessionCalls++
	if f.openRuntimeSessionErr != nil {
		return nil, f.openRuntimeSessionErr
	}
	return f.recordRuntimeSession(ctx, runtime, agentName, userID, sessionID, workspaceDir, sessionCtx)
}

func (f *fakeAgentBuilder) CreateFreshRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	agentName string,
	userID, sessionID, workspaceDir string,
	sessionCtx RuntimeSessionContext,
) (adksession.Session, error) {
	f.freshRuntimeSessionCalls++
	if f.beforeFreshRuntimeSession != nil {
		f.beforeFreshRuntimeSession()
	}
	return f.recordRuntimeSession(ctx, runtime, agentName, userID, sessionID, workspaceDir, sessionCtx)
}

func (f *fakeAgentBuilder) recordRuntimeSession(
	_ context.Context,
	_ *BuiltRuntime,
	agentName string,
	userID, sessionID, workspaceDir string,
	sessionCtx RuntimeSessionContext,
) (adksession.Session, error) {
	f.createRuntimeSessionAgentNames = append(f.createRuntimeSessionAgentNames, agentName)
	f.createRuntimeSessionUserIDs = append(f.createRuntimeSessionUserIDs, userID)
	f.createRuntimeSessionSessionIDs = append(f.createRuntimeSessionSessionIDs, sessionID)
	f.createRuntimeSessionWorkspaceDirs = append(f.createRuntimeSessionWorkspaceDirs, workspaceDir)
	f.createRuntimeSessionContexts = append(f.createRuntimeSessionContexts, sessionCtx)
	if f.createRuntimeSessionErr != nil {
		return nil, f.createRuntimeSessionErr
	}
	return f.runtimeSession, nil
}

func (f *fakeAgentBuilder) GetAgentMetadata(agentName string) AgentMetadata {
	f.metadataAgentNames = append(f.metadataAgentNames, agentName)
	return f.agentMetadata
}

type sqliteAgentBuilder struct {
	legacyCalls int
	freshCalls  int
	metadata    AgentMetadata
}

func (b *sqliteAgentBuilder) CreateRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	_, userID, sessionID, _ string,
	_ RuntimeSessionContext,
) (adksession.Session, error) {
	b.legacyCalls++
	response, err := runtime.SessionSvc.Get(ctx, &adksession.GetRequest{
		AppName: runtime.AppName, UserID: userID, SessionID: sessionID,
	})
	if err == nil {
		return response.Session, nil
	}
	if !errors.Is(err, adksession.ErrNotFound) {
		return nil, err
	}
	return createSQLiteTestSession(ctx, runtime, userID, sessionID)
}

func (b *sqliteAgentBuilder) OpenExistingRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	_, userID, sessionID, _ string,
	_ RuntimeSessionContext,
) (adksession.Session, error) {
	response, err := runtime.SessionSvc.Get(ctx, &adksession.GetRequest{
		AppName: runtime.AppName, UserID: userID, SessionID: sessionID,
	})
	if err != nil {
		return nil, err
	}
	return response.Session, nil
}

func (b *sqliteAgentBuilder) CreateFreshRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	_, userID, sessionID, _ string,
	_ RuntimeSessionContext,
) (adksession.Session, error) {
	b.freshCalls++
	return createSQLiteTestSession(ctx, runtime, userID, sessionID)
}

func (b *sqliteAgentBuilder) GetAgentMetadata(string) AgentMetadata { return b.metadata }

func createSQLiteTestSession(ctx context.Context, runtime *BuiltRuntime, userID, sessionID string) (adksession.Session, error) {
	response, err := runtime.SessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: runtime.AppName, UserID: userID, SessionID: sessionID,
	})
	if err != nil {
		return nil, err
	}
	return response.Session, nil
}

type fakeBaldaRuntimeManager struct {
	providerID   string
	runtime      *BuiltRuntime
	runtimeErr   error
	runtimeCalls int
}

func (f *fakeBaldaRuntimeManager) Runtime(context.Context) (*BuiltRuntime, error) {
	f.runtimeCalls++
	if f.runtimeErr != nil {
		return nil, f.runtimeErr
	}
	if f.runtime != nil {
		return f.runtime, nil
	}
	return &BuiltRuntime{}, nil
}

func (f *fakeBaldaRuntimeManager) ProviderID() string {
	return f.providerID
}

type fakeScopedRuntimeManager struct {
	fakeBaldaRuntimeManager
	sessionRuntime    *BuiltRuntime
	sessionErr        error
	runtimeForSession func(SessionRuntimeRequest) (*BuiltRuntime, error)
	sessionRequests   []SessionRuntimeRequest
}

func (f *fakeScopedRuntimeManager) RuntimeForSession(_ context.Context, request SessionRuntimeRequest) (*BuiltRuntime, error) {
	f.sessionRequests = append(f.sessionRequests, request)
	if f.runtimeForSession != nil {
		return f.runtimeForSession(request)
	}
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	if f.sessionRuntime != nil {
		return f.sessionRuntime, nil
	}
	return &BuiltRuntime{}, nil
}

func TestCreateSessionPersistsRuntimeSnapshot(t *testing.T) {
	store := &fakeSessionStore{}
	workingDir := t.TempDir()
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		sessionRuntime:          &BuiltRuntime{RuntimeSnapshotID: currentSnapshotID, ProviderBindingJSON: `{"root_provider_id":"current"}`},
	}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      &fakeAgentBuilder{},
		workingDir:        workingDir,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
	}
	locator := testTelegramLocator(10, 42)

	if err := m.CreateSession(context.Background(), SessionContext{Locator: locator, UserID: "tg-101"}, "topic"); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if len(runtimeManager.sessionRequests) != 1 {
		t.Fatalf("RuntimeForSession() calls = %d, want 1", len(runtimeManager.sessionRequests))
	}
	if got := runtimeManager.sessionRequests[0].RuntimeSnapshotID; got != "" {
		t.Fatalf("requested runtime snapshot = %q, want current selection", got)
	}
	if got := runtimeManager.sessionRequests[0].WorkspaceDir; got != workingDir {
		t.Fatalf("requested workspace dir = %q, want %q", got, workingDir)
	}
	if len(store.upsertedRecords) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(store.upsertedRecords))
	}
	if got := store.upsertedRecords[0].RuntimeSnapshotID; got != currentSnapshotID {
		t.Fatalf("persisted runtime snapshot = %q, want snapshot-current", got)
	}
	if got := store.upsertedRecords[0].ProviderBindingJSON; got != `{"root_provider_id":"current"}` {
		t.Fatalf("persisted provider binding = %q", got)
	}
	ts, err := m.GetSession(locator)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if got := ts.GetRuntimeSnapshotID(); got != currentSnapshotID {
		t.Fatalf("GetRuntimeSnapshotID() = %q, want snapshot-current", got)
	}
}

func TestEnsureTransientSessionDoesNotPersistAddressMetadata(t *testing.T) {
	store := &fakeSessionStore{}
	builder := &fakeAgentBuilder{}
	m := &Manager{baldaProviderName: "balda-provider", runtimeManager: &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		sessionRuntime:          &BuiltRuntime{RuntimeSnapshotID: currentSnapshotID},
	}, agentBuilder: builder, workingDir: t.TempDir(), logger: zerolog.Nop(),
		sessions: make(map[string]*TopicSession), sessionStore: store, sessionsPersistent: true}
	recipient := testTelegramLocator(10, 42)
	first := recipient
	first.SessionID = "sch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	second := recipient
	second.SessionID = "wh-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.ChannelType = "webhook"
	second.AddressKey = second.SessionID
	second.AddressJSON = "{}"
	contexts := []SessionContext{
		{Locator: first, UserID: "tg-101", Kind: KindSchedule, OwnerJobID: "scheduled-a"},
		{Locator: second, UserID: "tg-101", Kind: KindWebhook, OwnerJobID: "webhook-b"},
		{Locator: first, UserID: "tg-101", Kind: KindSchedule, OwnerJobID: "scheduled-a"},
	}
	for _, sessionCtx := range contexts {
		if _, err := m.EnsureTransientSession(t.Context(), sessionCtx, "balda"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.EnsureTransientSession(t.Context(), SessionContext{
		Locator: first, UserID: "tg-101", Kind: KindSchedule, OwnerJobID: "scheduled-other",
	}, "balda"); err == nil {
		t.Fatal("active private session accepted a different owner")
	}
	if len(store.upsertedRecords) != 0 {
		t.Fatalf("private sessions persisted address metadata: %+v", store.upsertedRecords)
	}
	if len(builder.createRuntimeSessionSessionIDs) != 2 || builder.createRuntimeSessionSessionIDs[0] != first.SessionID ||
		builder.createRuntimeSessionSessionIDs[1] != second.SessionID {
		t.Fatalf("runtime session IDs = %v", builder.createRuntimeSessionSessionIDs)
	}
	if builder.createRuntimeSessionCalls != 2 || builder.freshRuntimeSessionCalls != 0 {
		t.Fatalf("private builder calls = legacy %d, fresh %d; want legacy 2, fresh 0", builder.createRuntimeSessionCalls, builder.freshRuntimeSessionCalls)
	}
	if _, err := m.GetSession(recipient); err == nil {
		t.Fatal("recipient chat was created by private schedule sessions")
	}
	for _, sessionID := range []string{"sch-short", "wh-short", "tg-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		invalid := first
		invalid.SessionID = sessionID
		if _, err := m.EnsureTransientSession(t.Context(), SessionContext{Locator: invalid, UserID: "owner"}, "balda"); err == nil {
			t.Fatalf("invalid private session id %q accepted", sessionID)
		}
	}
	// A restarted manager has no address record to restore, yet uses the same
	// private ADK identity for an unfinished run.
	restarted := &Manager{baldaProviderName: m.baldaProviderName, runtimeManager: m.runtimeManager,
		agentBuilder: builder, workingDir: m.workingDir, logger: zerolog.Nop(),
		sessions: make(map[string]*TopicSession), sessionStore: store, sessionsPersistent: true}
	if _, err := restarted.EnsureTransientSession(t.Context(), SessionContext{
		Locator: first, UserID: "tg-101", Kind: KindSchedule, OwnerJobID: "scheduled-a",
	}, "balda"); err != nil {
		t.Fatal(err)
	}
	if len(builder.createRuntimeSessionSessionIDs) != 3 || builder.createRuntimeSessionSessionIDs[2] != first.SessionID ||
		len(store.upsertedRecords) != 0 {
		t.Fatalf("restart identities=%v, metadata=%d", builder.createRuntimeSessionSessionIDs, len(store.upsertedRecords))
	}
	if builder.createRuntimeSessionCalls != 3 || builder.freshRuntimeSessionCalls != 0 {
		t.Fatalf("restart builder calls = legacy %d, fresh %d; want legacy 3, fresh 0", builder.createRuntimeSessionCalls, builder.freshRuntimeSessionCalls)
	}
}

func TestEnsureTransientSessionReopensSQLiteRuntimeAfterRestart(t *testing.T) {
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	service := provider.RuntimeSessions()
	runtimeManager := &fakeBaldaRuntimeManager{
		providerID: "balda-provider",
		runtime:    &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service},
	}
	builder := &sqliteAgentBuilder{}
	locator := testTelegramLocator(10, 42)
	locator.SessionID = "sch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newManager := func() *Manager {
		return &Manager{
			baldaProviderName: "balda-provider", runtimeManager: runtimeManager, agentBuilder: builder,
			workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
			sessionStore: &fakeSessionStore{}, sessionsPersistent: true,
		}
	}

	if _, err := newManager().EnsureTransientSession(t.Context(), SessionContext{
		Locator: locator, UserID: "tg-101", Kind: KindSchedule, OwnerJobID: "scheduled-a",
	}, "balda"); err != nil {
		t.Fatalf("first EnsureTransientSession() error = %v", err)
	}
	if _, err := newManager().EnsureTransientSession(t.Context(), SessionContext{
		Locator: locator, UserID: "tg-101", Kind: KindSchedule, OwnerJobID: "scheduled-a",
	}, "balda"); err != nil {
		t.Fatalf("restart EnsureTransientSession() error = %v", err)
	}
	if builder.legacyCalls != 2 || builder.freshCalls != 0 {
		t.Fatalf("builder calls = legacy %d, fresh %d; want legacy 2, fresh 0", builder.legacyCalls, builder.freshCalls)
	}
}

func TestCloseRunSessionDeletesPersistedEventsAfterRestart(t *testing.T) {
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	service := provider.RuntimeSessions()
	manager := &Manager{runtimeManager: &fakeBaldaRuntimeManager{
		runtime: &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service},
	}, sessions: make(map[string]*TopicSession)}
	if err := manager.CloseRunSession(t.Context(), "tg-ordinary", "owner"); err == nil {
		t.Fatal("ordinary chat session accepted as private run")
	}
	for _, sessionID := range []string{
		"sch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"wh-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		created, err := service.Create(t.Context(), &adksession.CreateRequest{
			AppName: baldaRuntimeAppName, UserID: "owner", SessionID: sessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		event := adksession.NewEvent(t.Context(), "run-turn")
		event.Author = "user"
		if err := service.AppendEvent(t.Context(), created.Session, event); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := manager.CloseRunSession(t.Context(), sessionID, "owner"); err != nil {
				t.Fatal(err)
			}
		}
		recreated, err := service.Create(t.Context(), &adksession.CreateRequest{
			AppName: baldaRuntimeAppName, UserID: "owner", SessionID: sessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := recreated.Session.Events().Len(); got != 0 {
			t.Fatalf("events after private session %q delete = %d", sessionID, got)
		}
	}
}

func TestResetThenCreateSessionAdoptsCurrentRuntimeSnapshot(t *testing.T) {
	locator := testTelegramLocator(10, 42)
	otherLocator := testTelegramLocator(11, 43)
	oldRecord := baldastate.SessionRecord{
		SessionID:         locator.SessionID,
		UserID:            "tg-101",
		ChannelType:       locator.ChannelType,
		AddressKey:        locator.AddressKey,
		AddressJSON:       locator.AddressJSON,
		AgentName:         "project",
		RuntimeSnapshotID: "snapshot-old",
		Status:            baldastate.SessionStatusActive,
	}
	otherRecord := baldastate.SessionRecord{
		SessionID:         otherLocator.SessionID,
		UserID:            "tg-102",
		ChannelType:       otherLocator.ChannelType,
		AddressKey:        otherLocator.AddressKey,
		AddressJSON:       otherLocator.AddressJSON,
		AgentName:         "other",
		RuntimeSnapshotID: "snapshot-other",
		Status:            baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByID: map[string]baldastate.SessionRecord{
		locator.SessionID:      oldRecord,
		otherLocator.SessionID: otherRecord,
	}}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		sessionRuntime:          &BuiltRuntime{RuntimeSnapshotID: currentSnapshotID},
	}
	closed := 0
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      &fakeAgentBuilder{},
		workingDir:        t.TempDir(),
		logger:            zerolog.Nop(),
		sessionStore:      store,
		sessions: map[string]*TopicSession{
			locator.SessionID: withTestRuntime(&TopicSession{
				sessionID: locator.SessionID,
				userID:    oldRecord.UserID,
				locator:   locator,
				agentName: oldRecord.AgentName,
			}, nil, nil, func() error {
				closed++
				return nil
			}),
		},
	}

	if err := m.ResetSession(context.Background(), locator); err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	if err := m.CreateSession(context.Background(), SessionContext{Locator: locator, UserID: oldRecord.UserID}, oldRecord.AgentName); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("old runtime close calls = %d, want 1", closed)
	}
	if len(runtimeManager.sessionRequests) != 1 {
		t.Fatalf("RuntimeForSession() calls = %d, want 1", len(runtimeManager.sessionRequests))
	}
	request := runtimeManager.sessionRequests[0]
	if request.RuntimeSnapshotID != "" {
		t.Fatalf("requested runtime snapshot = %q, want current selection", request.RuntimeSnapshotID)
	}
	if request.Locator != locator || request.UserID != oldRecord.UserID {
		t.Fatalf("runtime request = %+v, want preserved locator and user", request)
	}
	if len(store.upsertedRecords) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(store.upsertedRecords))
	}
	got := store.upsertedRecords[0]
	if got.RuntimeSnapshotID != currentSnapshotID || got.AgentName != oldRecord.AgentName || got.UserID != oldRecord.UserID {
		t.Fatalf("recreated record = %+v, want current snapshot with preserved identity", got)
	}
	if store.recordsByID[otherLocator.SessionID] != otherRecord {
		t.Fatalf("other session changed: %+v", store.recordsByID[otherLocator.SessionID])
	}
}

func TestCreateSessionRejectsRuntimeWithoutSnapshot(t *testing.T) {
	closed := 0
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager: &fakeScopedRuntimeManager{
			fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
			sessionRuntime:          &BuiltRuntime{Close: func() error { closed++; return nil }},
		},
		agentBuilder: &fakeAgentBuilder{},
		workingDir:   t.TempDir(),
		logger:       zerolog.Nop(),
		sessions:     make(map[string]*TopicSession),
		sessionStore: &fakeSessionStore{},
	}

	err := m.CreateSession(context.Background(), SessionContext{Locator: testTelegramLocator(10, 42), UserID: "tg-101"}, "topic")
	if err == nil || !strings.Contains(err.Error(), "runtime snapshot is required") {
		t.Fatalf("CreateSession() error = %v, want missing snapshot", err)
	}
	if closed != 1 {
		t.Fatalf("runtime close calls = %d, want 1", closed)
	}
}

func TestCreateSessionPublishesCurrentEffectiveModel(t *testing.T) {
	locator := testTelegramLocator(9, 81)
	builder := &fakeAgentBuilder{agentMetadata: AgentMetadata{Model: "current-model"}}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager: &fakeScopedRuntimeManager{
			fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
			sessionRuntime:          &BuiltRuntime{RuntimeSnapshotID: currentSnapshotID, RecipeModel: "current-model"},
		},
		agentBuilder: builder,
		workingDir:   t.TempDir(),
		logger:       zerolog.Nop(),
		sessions:     make(map[string]*TopicSession),
		sessionStore: &fakeSessionStore{},
	}
	if err := m.CreateSession(t.Context(), SessionContext{Locator: locator, UserID: "user"}, "topic"); err != nil {
		t.Fatal(err)
	}
	created, err := m.GetSession(locator)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := created.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.EffectiveModel() != "current-model" {
		t.Fatalf("fresh effective model = %q, want current-model", lease.EffectiveModel())
	}
	if len(builder.metadataAgentNames) != 0 {
		t.Fatalf("post-construction model metadata lookups = %v, want none", builder.metadataAgentNames)
	}
}

func TestRestoreSessionRequestsPersistedRuntimeSnapshot(t *testing.T) {
	locator := testTelegramLocator(10, 42)
	record := baldastate.SessionRecord{
		SessionID:           locator.SessionID,
		UserID:              "tg-101",
		ChannelType:         locator.ChannelType,
		AddressKey:          locator.AddressKey,
		AddressJSON:         locator.AddressJSON,
		AgentName:           "topic",
		RuntimeSnapshotID:   "snapshot-persisted",
		ProviderBindingJSON: `{"root_provider_id":"persisted"}`,
		Status:              baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
		sessionAddressKey(locator.ChannelType, locator.AddressKey): record,
	}}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		sessionRuntime:          &BuiltRuntime{RuntimeSnapshotID: "snapshot-persisted", ProviderBindingJSON: record.ProviderBindingJSON},
	}
	builder := &fakeAgentBuilder{agentMetadata: AgentMetadata{Model: "current-default-model"}}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		workingDir:        t.TempDir(),
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
	}

	restored, err := m.RestoreSession(context.Background(), SessionContext{Locator: locator})
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	lease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.EffectiveModel() != "" {
		t.Fatalf("restored blank effective model = %q, want unknown", lease.EffectiveModel())
	}
	lease.Release()
	if len(builder.metadataAgentNames) != 0 {
		t.Fatalf("exact restore model metadata lookups = %v, want none", builder.metadataAgentNames)
	}
	if len(runtimeManager.sessionRequests) != 1 {
		t.Fatalf("RuntimeForSession() calls = %d, want 1", len(runtimeManager.sessionRequests))
	}
	if got := runtimeManager.sessionRequests[0].RuntimeSnapshotID; got != "snapshot-persisted" {
		t.Fatalf("requested runtime snapshot = %q, want snapshot-persisted", got)
	}
	if got := runtimeManager.sessionRequests[0].ProviderBindingJSON; got != record.ProviderBindingJSON {
		t.Fatalf("requested provider binding = %q, want persisted binding", got)
	}
	if builder.openRuntimeSessionCalls != 1 || builder.freshRuntimeSessionCalls != 0 {
		t.Fatalf("builder calls = open %d, fresh %d; want open 1, fresh 0", builder.openRuntimeSessionCalls, builder.freshRuntimeSessionCalls)
	}
	if len(store.upsertedRecords) != 0 {
		t.Fatalf("exact restore metadata writes = %d, want 0", len(store.upsertedRecords))
	}
}

func TestRestoreLegacySessionKeepsBlankBindingWithoutWrite(t *testing.T) {
	locator := testTelegramLocator(20, 84)
	dbPath := filepath.Join(t.TempDir(), "legacy-runtime.db")
	provider, err := baldastate.NewSQLiteProvider(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	record := baldastate.SessionRecord{
		SessionID: locator.SessionID, UserID: "tg-201", ChannelType: locator.ChannelType,
		AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "topic",
		RuntimeSnapshotID: "snapshot-persisted", Status: baldastate.SessionStatusActive,
	}
	if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	service := provider.RuntimeSessions()
	if _, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: record.UserID, SessionID: record.SessionID,
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &BuiltRuntime{
		AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: record.RuntimeSnapshotID,
		ProviderBindingJSON: publicationBinding,
	}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: runtime},
		sessionRuntime:          runtime,
	}
	builder := &sqliteAgentBuilder{}
	m := &Manager{
		baldaProviderName: "balda-provider", runtimeManager: runtimeManager, agentBuilder: builder,
		workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
		sessionStore: provider.Sessions(), runtimeOperations: provider.RuntimeOperations(),
	}
	probe, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	if _, err := probe.ExecContext(t.Context(), `CREATE TRIGGER reject_legacy_binding_adoption BEFORE UPDATE ON balda_session_metadata BEGIN SELECT RAISE(ABORT,'legacy row must not be rewritten'); END`); err != nil {
		t.Fatal(err)
	}

	restored, err := m.RestoreSession(t.Context(), SessionContext{Locator: locator})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtimeManager.sessionRequests[0].ProviderBindingJSON; got != "" {
		t.Fatalf("legacy restore requested binding = %q", got)
	}
	repeated, err := m.RestoreSession(t.Context(), SessionContext{Locator: locator})
	if err != nil || repeated != restored {
		t.Fatalf("repeated RestoreSession() = %p, %v; want %p", repeated, err, restored)
	}
	lease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.EffectiveModel() != "" || lease.Generation() != 0 || lease.ProviderBindingJSON() != publicationBinding {
		t.Fatalf("legacy runtime = model %q generation %d binding %q", lease.EffectiveModel(), lease.Generation(), lease.ProviderBindingJSON())
	}
	lease.Release()
	got, found, err := provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
	if err != nil || !found || got != record {
		t.Fatalf("legacy metadata = %+v, %t, %v; want unchanged %+v", got, found, err, record)
	}
}

func TestSessionRecordPreservesRuntimeSlotMetadata(t *testing.T) {
	const snapshotID = "snapshot"
	store := &fakeSessionStore{}
	ts := &TopicSession{sessionID: "session", userID: ensureTestAuthor, locator: testTelegramLocator(10, 0)}
	ts.runtime = newRuntimeSlot(runtimeSlotConfig{
		runtime:        &BuiltRuntime{RuntimeSnapshotID: snapshotID, ProviderBindingJSON: `{"root_provider_id":"provider"}`},
		userID:         ensureTestAuthor,
		sessionID:      "runtime-session",
		archiveID:      "archive",
		generation:     11,
		effectiveModel: "model-persisted",
		outcome:        "context_preserved",
	})
	m := &Manager{sessionStore: store}
	if err := m.persistSessionRecord(t.Context(), ts, baldastate.SessionStatusActive); err != nil {
		t.Fatal(err)
	}
	if len(store.upsertedRecords) != 1 {
		t.Fatalf("upserted records = %d, want 1", len(store.upsertedRecords))
	}
	record := store.upsertedRecords[0]
	if record.ArchiveID != runtimeSlotTestArchiveID || record.RuntimeGeneration != 11 || record.EffectiveModel != "model-persisted" || record.RuntimeOutcome != "context_preserved" ||
		record.RuntimeSnapshotID != snapshotID || record.ProviderBindingJSON != `{"root_provider_id":"provider"}` {
		t.Fatalf("runtime metadata lost: %+v", record)
	}
}

func TestRestoreSessionSnapshotMismatchStartsFreshWithCurrentRuntime(t *testing.T) {
	locator := testTelegramLocator(10, 42)
	unrelatedLocator := testTelegramLocator(11, 43)
	record := baldastate.SessionRecord{
		SessionID:         locator.SessionID,
		UserID:            "tg-101",
		ChannelType:       locator.ChannelType,
		AddressKey:        locator.AddressKey,
		AddressJSON:       locator.AddressJSON,
		AgentName:         "topic",
		RuntimeSnapshotID: "snapshot-persisted",
		Status:            baldastate.SessionStatusActive,
	}
	unrelatedRecord := baldastate.SessionRecord{
		SessionID: unrelatedLocator.SessionID, UserID: "tg-202", ChannelType: unrelatedLocator.ChannelType,
		AddressKey: unrelatedLocator.AddressKey, AddressJSON: unrelatedLocator.AddressJSON, AgentName: "other",
		RuntimeSnapshotID: "snapshot-unrelated", Status: baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
		sessionAddressKey(locator.ChannelType, locator.AddressKey):                   record,
		sessionAddressKey(unrelatedLocator.ChannelType, unrelatedLocator.AddressKey): unrelatedRecord,
	}}
	var events []string
	store.beforeUpsert = func() { events = append(events, "upsert") }
	exactCloseCalls := 0
	service := &trackingSessionService{
		Service:      adksession.InMemoryService(),
		beforeDelete: func() { events = append(events, "delete") },
	}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
			if request.RuntimeSnapshotID != "" {
				return &BuiltRuntime{
					RuntimeSnapshotID: "snapshot-other",
					Close: func() error {
						exactCloseCalls++
						return nil
					},
				}, nil
			}
			return &BuiltRuntime{
				AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID,
			}, nil
		},
	}
	builder := &fakeAgentBuilder{beforeFreshRuntimeSession: func() { events = append(events, "create") }}
	observer := &fakeBoundaryObserver{before: func(context.Context, SessionBoundary) { events = append(events, "boundary") }}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		workingDir:        t.TempDir(),
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
		boundaryObserver:  observer,
	}

	ts, err := m.RestoreSession(context.Background(), SessionContext{Locator: locator})
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if exactCloseCalls != 1 {
		t.Fatalf("failed exact runtime close calls = %d, want 1", exactCloseCalls)
	}
	if len(runtimeManager.sessionRequests) != 2 || runtimeManager.sessionRequests[0].RuntimeSnapshotID != "snapshot-persisted" || runtimeManager.sessionRequests[1].RuntimeSnapshotID != "" {
		t.Fatalf("runtime requests = %+v, want saved snapshot then current selection", runtimeManager.sessionRequests)
	}
	if builder.openRuntimeSessionCalls != 0 || builder.freshRuntimeSessionCalls != 1 {
		t.Fatalf("builder calls = open %d, fresh %d; want open 0, fresh 1", builder.openRuntimeSessionCalls, builder.freshRuntimeSessionCalls)
	}
	if len(observer.boundaries) != 1 || observer.boundaries[0].Reason != BoundaryReasonRotation {
		t.Fatalf("boundaries = %+v, want one rotation", observer.boundaries)
	}
	if len(service.deleteRequests) != 1 || service.deleteRequests[0].SessionID != locator.SessionID {
		t.Fatalf("delete requests = %+v, want stable session %q", service.deleteRequests, locator.SessionID)
	}
	if len(store.upsertedRecords) != 1 || store.upsertedRecords[0].RuntimeSnapshotID != currentSnapshotID {
		t.Fatalf("persisted records = %+v, want current snapshot", store.upsertedRecords)
	}
	if ts.GetRuntimeSnapshotID() != currentSnapshotID {
		t.Fatalf("published snapshot = %q, want %q", ts.GetRuntimeSnapshotID(), currentSnapshotID)
	}
	gotUnrelated := store.recordsByAddress[sessionAddressKey(unrelatedLocator.ChannelType, unrelatedLocator.AddressKey)]
	if gotUnrelated.RuntimeSnapshotID != unrelatedRecord.RuntimeSnapshotID || gotUnrelated.SessionID != unrelatedRecord.SessionID {
		t.Fatalf("unrelated locator changed during lazy recovery: %+v", gotUnrelated)
	}
	if _, err := m.GetSession(unrelatedLocator); err == nil {
		t.Fatal("lazy recovery published unrelated locator")
	}
	if got, want := strings.Join(events, ","), "boundary,delete,create,upsert"; got != want {
		t.Fatalf("fallback order = %q, want %q", got, want)
	}

	restartedRuntime := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		sessionRuntime:          &BuiltRuntime{RuntimeSnapshotID: currentSnapshotID},
	}
	restartedBuilder := &fakeAgentBuilder{}
	restarted := &Manager{
		baldaProviderName: "balda-provider", runtimeManager: restartedRuntime, agentBuilder: restartedBuilder,
		workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: store,
	}
	if _, err := restarted.RestoreSession(context.Background(), SessionContext{Locator: locator}); err != nil {
		t.Fatalf("RestoreSession() after restart error = %v", err)
	}
	if len(restartedRuntime.sessionRequests) != 1 || restartedRuntime.sessionRequests[0].RuntimeSnapshotID != currentSnapshotID {
		t.Fatalf("restart runtime requests = %+v, want recovered snapshot", restartedRuntime.sessionRequests)
	}
	if restartedBuilder.openRuntimeSessionCalls != 1 || restartedBuilder.freshRuntimeSessionCalls != 0 {
		t.Fatalf("restart builder calls = open %d, fresh %d; want open 1, fresh 0", restartedBuilder.openRuntimeSessionCalls, restartedBuilder.freshRuntimeSessionCalls)
	}
}

func TestRestoreFallbackPreservesRequestedModelPreference(t *testing.T) {
	const (
		providerID     = "provider-id"
		displayLabel   = "display-label"
		recipeModel    = "recipe-a"
		requestedModel = "requested-b"
	)
	locator := testTelegramLocator(31, 73)
	dbPath := filepath.Join(t.TempDir(), "requested-model-fallback.db")
	provider, err := baldastate.NewSQLiteProvider(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	requestedPreference := requestedModel
	record := baldastate.SessionRecord{
		SessionID: locator.SessionID, UserID: ensureTestUserID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON,
		AgentName: displayLabel, RuntimeSnapshotID: "snapshot-persisted", ProviderBindingJSON: publicationBinding,
		RuntimeGeneration: 4, RequestedModel: &requestedPreference, EffectiveModel: recipeModel,
		Status: baldastate.SessionStatusActive,
	}
	if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RuntimeSessions().Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: record.UserID, SessionID: record.SessionID,
	}); err != nil {
		t.Fatal(err)
	}
	currentRuntime := &BuiltRuntime{
		AppName: baldaRuntimeAppName, SessionSvc: provider.RuntimeSessions(), RuntimeSnapshotID: currentSnapshotID,
		ProviderBindingJSON: publicationBinding, RecipeModel: requestedModel,
	}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: providerID},
		runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
			if request.ModelOverride == nil || *request.ModelOverride != requestedModel {
				t.Fatalf("runtime model override = %v, want %q", request.ModelOverride, requestedModel)
			}
			if request.RuntimeSnapshotID != "" {
				return &BuiltRuntime{RuntimeSnapshotID: "snapshot-mismatch", ProviderBindingJSON: publicationBinding}, nil
			}
			return currentRuntime, nil
		},
	}
	builder := &sqliteAgentBuilder{metadata: AgentMetadata{Model: recipeModel}}
	manager := &Manager{
		baldaProviderName: providerID, runtimeManager: runtimeManager, agentBuilder: builder,
		workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
		sessionStore: provider.Sessions(), runtimeOperations: provider.RuntimeOperations(),
	}

	restored, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeManager.sessionRequests) != 2 {
		t.Fatalf("runtime requests = %+v, want exact then fallback", runtimeManager.sessionRequests)
	}
	lease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.EffectiveModel() != requestedModel || lease.Generation() != record.RuntimeGeneration+1 {
		t.Fatalf("fallback slot model/generation = %q/%d", lease.EffectiveModel(), lease.Generation())
	}
	lease.Release()
	info, err := manager.GetSessionInfo(t.Context(), record.SessionID)
	if err != nil || info.RequestedModel == nil || *info.RequestedModel != requestedModel || info.ActiveModel != "" || info.Model != "" {
		t.Fatalf("fallback model truth = %+v, %v", info, err)
	}
	committed, found, err := provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
	if err != nil || !found || committed.RequestedModel == nil || *committed.RequestedModel != requestedModel ||
		committed.EffectiveModel != requestedModel || committed.RuntimeGeneration != record.RuntimeGeneration+1 {
		t.Fatalf("committed fallback metadata = %+v, %t, %v", committed, found, err)
	}

	probe, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	var count int
	if err := probe.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM balda_runtime_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fallback operation count = %d, %v", count, err)
	}
	var kind, targetPreference, targetModel, operationState string
	if err := probe.QueryRowContext(t.Context(), `SELECT kind,target_requested_model,target_model,state FROM balda_runtime_operations LIMIT 1`).
		Scan(&kind, &targetPreference, &targetModel, &operationState); err != nil {
		t.Fatal(err)
	}
	if kind != string(baldastate.RuntimeOperationKindLifetimePreference) || targetPreference != requestedModel ||
		targetModel != requestedModel || operationState != string(baldastate.RuntimeOperationTerminal) {
		t.Fatalf("fallback operation = kind %q preference %q model %q state %q", kind, targetPreference, targetModel, operationState)
	}
	replayed, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator})
	if err != nil || replayed != restored || len(runtimeManager.sessionRequests) != 2 {
		t.Fatalf("fallback replay = %p, %v; runtime requests=%d", replayed, err, len(runtimeManager.sessionRequests))
	}
	if err := probe.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM balda_runtime_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fallback replay operation count = %d, %v", count, err)
	}
}

func TestRestoreFallbackUsesBoundRuntimeRecipeReceipt(t *testing.T) {
	const (
		providerID   = "managed-only"
		displayModel = "root-display-a"
		boundModel   = "managed-binding-b"
	)
	locator := testTelegramLocator(32, 74)
	dbPath := filepath.Join(t.TempDir(), "bound-recipe-fallback.db")
	provider, err := baldastate.NewSQLiteProvider(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	record := baldastate.SessionRecord{
		SessionID: locator.SessionID, UserID: ensureTestUserID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON,
		AgentName: displayModel, RuntimeSnapshotID: "snapshot-persisted", ProviderBindingJSON: publicationBinding,
		RuntimeGeneration: 2, EffectiveModel: displayModel, Status: baldastate.SessionStatusActive,
	}
	if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RuntimeSessions().Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: record.UserID, SessionID: record.SessionID,
	}); err != nil {
		t.Fatal(err)
	}
	currentRuntime := &BuiltRuntime{
		AppName: baldaRuntimeAppName, SessionSvc: provider.RuntimeSessions(), RuntimeSnapshotID: currentSnapshotID,
		ProviderBindingJSON: publicationBinding, RecipeModel: boundModel,
	}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: providerID},
		runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
			if request.ModelOverride != nil {
				t.Fatalf("runtime model override = %v, want nil recipe selection", request.ModelOverride)
			}
			if request.RuntimeSnapshotID != "" {
				return &BuiltRuntime{
					RuntimeSnapshotID: "snapshot-mismatch", ProviderBindingJSON: publicationBinding, RecipeModel: displayModel,
				}, nil
			}
			return currentRuntime, nil
		},
	}
	manager := &Manager{
		baldaProviderName: providerID, runtimeManager: runtimeManager,
		agentBuilder: &sqliteAgentBuilder{metadata: AgentMetadata{Model: displayModel}},
		workingDir:   t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
		sessionStore: provider.Sessions(), runtimeOperations: provider.RuntimeOperations(),
	}

	restored, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.EffectiveModel() != boundModel || lease.Generation() != record.RuntimeGeneration+1 {
		t.Fatalf("bound fallback slot = model %q generation %d", lease.EffectiveModel(), lease.Generation())
	}
	lease.Release()
	committed, found, err := provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
	if err != nil || !found || committed.RequestedModel != nil || committed.EffectiveModel != boundModel {
		t.Fatalf("bound fallback metadata = %+v, %t, %v", committed, found, err)
	}
	probe, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	var kind, targetModel string
	var targetRequested sql.NullString
	if err := probe.QueryRowContext(t.Context(), `SELECT kind,target_requested_model,target_model FROM balda_runtime_operations LIMIT 1`).
		Scan(&kind, &targetRequested, &targetModel); err != nil {
		t.Fatal(err)
	}
	if kind != string(baldastate.RuntimeOperationKindLifetimePreference) || targetRequested.Valid || targetModel != boundModel {
		t.Fatalf("bound fallback operation = kind %q requested %v model %q", kind, targetRequested, targetModel)
	}
}

func TestRestoreFallbackModelProjection(t *testing.T) {
	requested := "requested-b"
	for _, test := range []struct {
		name          string
		requested     *string
		recipe        string
		wantModel     string
		wantRequested *string
	}{
		{name: "requested wins", requested: &requested, recipe: "recipe-a", wantModel: requested, wantRequested: &requested},
		{name: "exact recipe fallback", recipe: "recipe-a", wantModel: "recipe-a"},
		{name: "unknown remains empty", recipe: "", wantModel: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, projected := restoreFallbackModelProjection(test.requested, test.recipe)
			if model != test.wantModel || !sameOptionalModel(projected, test.wantRequested) {
				t.Fatalf("projection = %q/%v, want %q/%v", model, projected, test.wantModel, test.wantRequested)
			}
			if projected != nil && test.requested != nil && projected == test.requested {
				t.Fatal("projection retained caller-owned preference pointer")
			}
		})
	}
}

func TestRestoreSessionStopsBeforeRecoveryMutationWhenManagerStopsDuringCurrentBind(t *testing.T) {
	locator := testTelegramLocator(10, 42)
	record := baldastate.SessionRecord{
		SessionID: locator.SessionID, UserID: "tg-101", ChannelType: locator.ChannelType,
		AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "topic",
		RuntimeSnapshotID: "snapshot-persisted", Status: baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
		sessionAddressKey(locator.ChannelType, locator.AddressKey): record,
	}}
	currentBindStarted := make(chan struct{})
	releaseCurrentBind := make(chan struct{})
	service := &trackingSessionService{Service: adksession.InMemoryService()}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
		runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
			if request.RuntimeSnapshotID != "" {
				return &BuiltRuntime{RuntimeSnapshotID: request.RuntimeSnapshotID}, nil
			}
			close(currentBindStarted)
			<-releaseCurrentBind
			return &BuiltRuntime{
				AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID,
			}, nil
		},
	}
	builder := &fakeAgentBuilder{openRuntimeSessionErr: errors.New("saved attachment unavailable")}
	observer := &fakeBoundaryObserver{}
	m := &Manager{
		baldaProviderName: "balda-provider", runtimeManager: runtimeManager, agentBuilder: builder,
		workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
		sessionStore: store, boundaryObserver: observer,
	}

	restoreDone := make(chan error, 1)
	go func() {
		_, err := m.RestoreSession(t.Context(), SessionContext{Locator: locator})
		restoreDone <- err
	}()
	select {
	case <-currentBindStarted:
	case <-time.After(time.Second):
		t.Fatal("current runtime bind did not start")
	}
	stopDone := make(chan struct{})
	go func() {
		_ = m.Stop(t.Context())
		close(stopDone)
	}()
	deadline := time.After(time.Second)
	for !m.stopping.Load() {
		select {
		case <-deadline:
			t.Fatal("Stop did not mark manager stopping")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-stopDone:
		t.Fatal("Stop returned while restore held the lifecycle read guard")
	default:
	}
	close(releaseCurrentBind)
	if err := <-restoreDone; !errors.Is(err, errManagerStopping) {
		t.Fatalf("RestoreSession() error = %v, want manager stopping", err)
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after restore aborted")
	}
	if len(observer.boundaries) != 0 || len(service.deleteRequests) != 0 || builder.freshRuntimeSessionCalls != 0 || len(store.upsertedRecords) != 0 {
		t.Fatalf("recovery mutation after Stop: boundaries=%d deletes=%d creates=%d upserts=%d",
			len(observer.boundaries), len(service.deleteRequests), builder.freshRuntimeSessionCalls, len(store.upsertedRecords))
	}
	if _, ok := m.sessions[locator.SessionID]; ok {
		t.Fatal("restore published a session while manager was stopping")
	}
}

func TestRestoreSessionDoesNotCloseSharedRootRuntime(t *testing.T) {
	locator := testTelegramLocator(10, 42)
	record := baldastate.SessionRecord{
		SessionID: locator.SessionID, UserID: "tg-101", ChannelType: locator.ChannelType,
		AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "topic",
		RuntimeSnapshotID: "snapshot-persisted", Status: baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
		sessionAddressKey(locator.ChannelType, locator.AddressKey): record,
	}}
	closed := &atomic.Bool{}
	closeCalls := 0
	service := &closeSensitiveSessionService{Service: adksession.InMemoryService(), closed: closed}
	rootRuntime := &BuiltRuntime{
		AppName: baldaRuntimeAppName, SessionSvc: service,
		Close: func() error {
			closeCalls++
			closed.Store(true)
			return nil
		},
	}
	builder := &fakeAgentBuilder{openRuntimeSessionErr: errors.New("saved attachment unavailable")}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager: &fakeBaldaRuntimeManager{
			providerID: "balda-provider", runtime: rootRuntime,
		},
		agentBuilder: builder, workingDir: t.TempDir(), logger: zerolog.Nop(),
		sessions: make(map[string]*TopicSession), sessionStore: store, sessionsPersistent: true,
	}

	if _, err := m.RestoreSession(t.Context(), SessionContext{Locator: locator}); err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if closeCalls != 0 || closed.Load() {
		t.Fatalf("shared root closed during fallback: calls=%d closed=%t", closeCalls, closed.Load())
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if closeCalls != 0 || closed.Load() {
		t.Fatalf("shared root closed during session cleanup: calls=%d closed=%t", closeCalls, closed.Load())
	}
}

func TestRestoreSessionFallbackHonorsCallerAndManagerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		restoreErr error
		cancel     bool
		stopping   bool
		wantFresh  bool
	}{
		{name: "canceled caller", restoreErr: errors.New("restore failed"), cancel: true},
		{name: "manager stopping", restoreErr: errors.New("restore failed"), stopping: true},
		{name: "live dependency canceled", restoreErr: context.Canceled, wantFresh: true},
		{name: "live dependency deadline", restoreErr: context.DeadlineExceeded, wantFresh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locator := testTelegramLocator(10, 42)
			record := baldastate.SessionRecord{
				SessionID: locator.SessionID, UserID: "tg-101", ChannelType: locator.ChannelType,
				AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "topic",
				RuntimeSnapshotID: "snapshot-persisted", Status: baldastate.SessionStatusActive,
			}
			store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
				sessionAddressKey(locator.ChannelType, locator.AddressKey): record,
			}}
			closed := 0
			service := &trackingSessionService{Service: adksession.InMemoryService()}
			runtimeManager := &fakeScopedRuntimeManager{
				fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
				runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
					if request.RuntimeSnapshotID != "" {
						return &BuiltRuntime{RuntimeSnapshotID: request.RuntimeSnapshotID, Close: func() error {
							closed++
							return nil
						}}, nil
					}
					return &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID}, nil
				},
			}
			builder := &fakeAgentBuilder{openRuntimeSessionErr: tc.restoreErr}
			observer := &fakeBoundaryObserver{}
			m := &Manager{
				baldaProviderName: "balda-provider", runtimeManager: runtimeManager, agentBuilder: builder,
				workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
				sessionStore: store, boundaryObserver: observer,
			}
			m.stopping.Store(tc.stopping)
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			} else {
				defer cancel()
			}

			_, err := m.RestoreSession(ctx, SessionContext{Locator: locator})
			if tc.wantFresh && err != nil {
				t.Fatalf("RestoreSession() error = %v", err)
			}
			if !tc.wantFresh && err == nil {
				t.Fatal("RestoreSession() error = nil, want interrupted restore")
			}
			wantRequests := 0
			wantBoundaries := 0
			wantFreshCalls := 0
			wantClosed := 0
			if tc.wantFresh {
				wantRequests = 2
				wantBoundaries = 1
				wantFreshCalls = 1
				wantClosed = 1
			}
			if len(runtimeManager.sessionRequests) != wantRequests || builder.freshRuntimeSessionCalls != wantFreshCalls || len(observer.boundaries) != wantBoundaries {
				t.Fatalf("fallback activity: requests=%d fresh=%d boundaries=%d", len(runtimeManager.sessionRequests), builder.freshRuntimeSessionCalls, len(observer.boundaries))
			}
			if closed != wantClosed {
				t.Fatalf("failed exact runtime close calls = %d, want %d", closed, wantClosed)
			}
			_, published := m.sessions[locator.SessionID]
			if published != tc.wantFresh {
				t.Fatalf("session published = %t, want %t", published, tc.wantFresh)
			}
		})
	}
}

func TestRestoreSessionFreshFailurePublishesNothing(t *testing.T) {
	for _, tc := range []struct {
		name           string
		configure      func(*fakeAgentBuilder, *fakeSessionStore)
		currentBindErr error
		boundaryErr    error
		deleteErr      error
		wantDeletes    int
		wantFreshCall  int
		wantCloseCalls int
	}{
		{
			name: "current bind", currentBindErr: errors.New("current catalog unavailable"),
			wantDeletes: 0, wantFreshCall: 0, wantCloseCalls: 0,
		},
		{
			name: "boundary", boundaryErr: errors.New("boundary unavailable"),
			wantDeletes: 0, wantFreshCall: 0, wantCloseCalls: 1,
		},
		{
			name: "delete", deleteErr: errors.New("delete unavailable"),
			wantDeletes: 1, wantFreshCall: 0, wantCloseCalls: 1,
		},
		{
			name: "create",
			configure: func(builder *fakeAgentBuilder, _ *fakeSessionStore) {
				builder.createRuntimeSessionErr = errors.New("create unavailable")
			},
			wantDeletes: 1, wantFreshCall: 1, wantCloseCalls: 1,
		},
		{
			name: "metadata upsert",
			configure: func(_ *fakeAgentBuilder, store *fakeSessionStore) {
				store.upsertErr = errors.New("metadata unavailable")
			},
			wantDeletes: 2, wantFreshCall: 1, wantCloseCalls: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locator := testTelegramLocator(10, 42)
			record := baldastate.SessionRecord{
				SessionID: locator.SessionID, UserID: "tg-101", ChannelType: locator.ChannelType,
				AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "topic",
				RuntimeSnapshotID: "snapshot-persisted", Status: baldastate.SessionStatusActive,
			}
			store := &fakeSessionStore{recordsByAddress: map[string]baldastate.SessionRecord{
				sessionAddressKey(locator.ChannelType, locator.AddressKey): record,
			}}
			builder := &fakeAgentBuilder{}
			if tc.configure != nil {
				tc.configure(builder, store)
			}
			service := &trackingSessionService{Service: adksession.InMemoryService(), deleteErr: tc.deleteErr}
			currentCloseCalls := 0
			runtimeManager := &fakeScopedRuntimeManager{
				fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider"},
				runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
					if request.RuntimeSnapshotID != "" {
						return &BuiltRuntime{RuntimeSnapshotID: "snapshot-other"}, nil
					}
					if tc.currentBindErr != nil {
						return nil, tc.currentBindErr
					}
					return &BuiltRuntime{
						AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID,
						Close: func() error { currentCloseCalls++; return nil },
					}, nil
				},
			}
			m := &Manager{
				baldaProviderName: "balda-provider", runtimeManager: runtimeManager, agentBuilder: builder,
				workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
				sessionStore: store, boundaryObserver: &fakeBoundaryObserver{err: tc.boundaryErr},
			}

			if _, err := m.RestoreSession(context.Background(), SessionContext{Locator: locator}); err == nil {
				t.Fatal("RestoreSession() error = nil, want fresh-start failure")
			}
			if _, ok := m.sessions[locator.SessionID]; ok {
				t.Fatal("failed fresh start published a session")
			}
			if builder.freshRuntimeSessionCalls != tc.wantFreshCall || len(service.deleteRequests) != tc.wantDeletes {
				t.Fatalf("fresh calls = %d, deletes = %d; want %d, %d", builder.freshRuntimeSessionCalls, len(service.deleteRequests), tc.wantFreshCall, tc.wantDeletes)
			}
			if currentCloseCalls != tc.wantCloseCalls {
				t.Fatalf("current runtime close calls = %d, want %d", currentCloseCalls, tc.wantCloseCalls)
			}
		})
	}
}

func TestCreateSession_ReusesSingleRuntimeAndMapsAgentSessions(t *testing.T) {
	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "balda-provider"}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		workingDir:        t.TempDir(),
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      &fakeSessionStore{},
	}

	first := SessionContext{
		Locator: testTelegramLocator(10, 41),
		UserID:  "tg-201",
	}
	second := SessionContext{
		Locator: testTelegramLocator(10, 42),
		UserID:  "tg-202",
	}

	if err := m.CreateSession(context.Background(), first, "topic-a"); err != nil {
		t.Fatalf("CreateSession(first) error = %v", err)
	}
	if err := m.CreateSession(context.Background(), second, "topic-b"); err != nil {
		t.Fatalf("CreateSession(second) error = %v", err)
	}

	if runtimeManager.runtimeCalls != 2 {
		t.Fatalf("Runtime() calls = %d, want 2", runtimeManager.runtimeCalls)
	}
	if got := len(builder.createRuntimeSessionSessionIDs); got != 2 {
		t.Fatalf("CreateRuntimeSession calls = %d, want 2", got)
	}
	if got := len(builder.createRuntimeSessionContexts); got != 2 {
		t.Fatalf("CreateRuntimeSession contexts = %d, want 2", got)
	}

	firstSessionID := builder.createRuntimeSessionSessionIDs[0]
	secondSessionID := builder.createRuntimeSessionSessionIDs[1]
	if firstSessionID == secondSessionID {
		t.Fatalf("agent session ids are equal (%q), want unique per balda session", firstSessionID)
	}
	if !strings.HasPrefix(firstSessionID, first.Locator.SessionID+"-a") {
		t.Fatalf("first agent session id = %q, want prefix %q", firstSessionID, first.Locator.SessionID+"-a")
	}
	if !strings.HasPrefix(secondSessionID, second.Locator.SessionID+"-a") {
		t.Fatalf("second agent session id = %q, want prefix %q", secondSessionID, second.Locator.SessionID+"-a")
	}
	if got := builder.createRuntimeSessionContexts[0].BaldaSessionID; got != first.Locator.SessionID {
		t.Fatalf("first BaldaSessionID = %q, want %q", got, first.Locator.SessionID)
	}
	if got := builder.createRuntimeSessionContexts[1].BaldaSessionID; got != second.Locator.SessionID {
		t.Fatalf("second BaldaSessionID = %q, want %q", got, second.Locator.SessionID)
	}
}

func TestCreateSession_PersistentModeUsesStableAgentSessionID(t *testing.T) {
	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "balda-provider"}
	locator := testTelegramLocator(10, 41)
	m := &Manager{
		baldaProviderName:  "balda-provider",
		runtimeManager:     runtimeManager,
		agentBuilder:       builder,
		workingDir:         t.TempDir(),
		logger:             zerolog.Nop(),
		sessions:           make(map[string]*TopicSession),
		sessionStore:       &fakeSessionStore{},
		sessionsPersistent: true,
	}

	if err := m.CreateSession(context.Background(), SessionContext{
		Locator: locator,
		UserID:  "tg-201",
	}, "topic-a"); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if got := builder.createRuntimeSessionSessionIDs[0]; got != locator.SessionID {
		t.Fatalf("CreateRuntimeSession sessionID = %q, want stable balda session ID %q", got, locator.SessionID)
	}
	if got := builder.createRuntimeSessionContexts[0].BaldaSessionID; got != locator.SessionID {
		t.Fatalf("CreateRuntimeSession BaldaSessionID = %q, want %q", got, locator.SessionID)
	}
}

func TestCreateSession_UsesBaldaProviderBackend(t *testing.T) {
	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "balda-provider"}
	m := &Manager{
		baldaProviderName: "balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      &fakeSessionStore{},
	}

	err := m.CreateSession(context.Background(), SessionContext{
		Locator: testTelegramLocator(10, 42),
		UserID:  "tg-201",
	}, "custom-label")

	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if got, want := builder.createRuntimeSessionAgentNames[0], "balda-provider"; got != want {
		t.Fatalf("CreateRuntimeSession provider = %q, want %q", got, want)
	}

	ts := m.sessions[testTelegramLocator(10, 42).SessionID]
	if ts.agentName != "custom-label" {
		t.Fatalf("session label = %q, want %q", ts.agentName, "custom-label")
	}
}

func TestRestoreSession_AlwaysUsesCurrentBaldaProviderBackend(t *testing.T) {
	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "new-balda-provider"}
	locator := testTelegramLocator(10, 42)
	store := &fakeSessionStore{
		recordsByAddress: map[string]baldastate.SessionRecord{
			sessionAddressKey(baldastate.ChannelTypeTelegram, "10:42"): {
				SessionID:   locator.SessionID,
				ChannelType: baldastate.ChannelTypeTelegram,
				AddressKey:  "10:42",
				AddressJSON: `{"chat_id":10,"topic_id":42}`,
				AgentName:   "previous-persisted-label",
				Status:      baldastate.SessionStatusActive,
			},
		},
	}

	m := &Manager{
		baldaProviderName: "new-balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
	}

	_, err := m.RestoreSession(context.Background(), SessionContext{
		Locator:                    locator,
		UserID:                     "tg-201",
		AllowBaldaProviderFallback: true,
	})

	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}

	if got, want := builder.createRuntimeSessionAgentNames[0], "new-balda-provider"; got != want {
		t.Fatalf("CreateRuntimeSession provider = %q, want %q", got, want)
	}

	ts := m.sessions[locator.SessionID]
	if ts.agentName != "previous-persisted-label" {
		t.Fatalf("session label = %q, want %q", ts.agentName, "previous-persisted-label")
	}
}

func TestRestoreSession_UserIDSelection(t *testing.T) {
	tests := []struct {
		name            string
		persistedUserID string
		wantUserID      string
	}{
		{
			name:            "persisted user id",
			persistedUserID: "tg-101",
			wantUserID:      "tg-101",
		},
		{
			name:            "fallback current user id",
			persistedUserID: " ",
			wantUserID:      "tg-202",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := &fakeAgentBuilder{}
			runtimeManager := &fakeBaldaRuntimeManager{providerID: "balda-provider"}
			locator := testTelegramLocator(10, 42)
			store := &fakeSessionStore{
				recordsByAddress: map[string]baldastate.SessionRecord{
					sessionAddressKey(baldastate.ChannelTypeTelegram, "10:42"): {
						SessionID:   locator.SessionID,
						UserID:      tt.persistedUserID,
						ChannelType: baldastate.ChannelTypeTelegram,
						AddressKey:  "10:42",
						AddressJSON: `{"chat_id":10,"topic_id":42}`,
						AgentName:   "persisted-label",
						Status:      baldastate.SessionStatusActive,
					},
				},
			}

			m := &Manager{
				baldaProviderName: "balda-provider",
				runtimeManager:    runtimeManager,
				agentBuilder:      builder,
				logger:            zerolog.Nop(),
				sessions:          make(map[string]*TopicSession),
				sessionStore:      store,
			}

			_, err := m.RestoreSession(context.Background(), SessionContext{
				Locator: locator,
				UserID:  "tg-202",
			})

			if err != nil {
				t.Fatalf("RestoreSession() error = %v", err)
			}
			if got := builder.createRuntimeSessionUserIDs[0]; got != tt.wantUserID {
				t.Fatalf("CreateRuntimeSession userID = %q, want %q", got, tt.wantUserID)
			}
		})
	}
}

func TestRestoreSession_UsesAutoLabelWhenPersistedLabelMissing(t *testing.T) {
	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "new-balda-provider"}
	locator := testTelegramLocator(11, 43)
	store := &fakeSessionStore{
		recordsByAddress: map[string]baldastate.SessionRecord{
			sessionAddressKey(baldastate.ChannelTypeTelegram, "11:43"): {
				SessionID:   locator.SessionID,
				ChannelType: baldastate.ChannelTypeTelegram,
				AddressKey:  "11:43",
				AddressJSON: `{"chat_id":11,"topic_id":43}`,
				AgentName:   " ",
				Status:      baldastate.SessionStatusActive,
			},
		},
	}

	m := &Manager{
		baldaProviderName: "new-balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
	}

	_, err := m.RestoreSession(context.Background(), SessionContext{
		Locator: locator,
		UserID:  "tg-201",
	})

	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}

	ts := m.sessions[locator.SessionID]
	if ts.agentName != defaultSessionLabel {
		t.Fatalf("session label = %q, want auto", ts.agentName)
	}
}

func TestRestoreSession_FailsWhenPersistedWorkspaceBranchMissing(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	initGitRepo(t, ctx, workingDir)

	writeFile(t, filepath.Join(workingDir, "seed.txt"), "seed\n")
	runGit(t, ctx, workingDir, "add", "seed.txt")
	runGit(t, ctx, workingDir, "commit", "-m", "chore: seed")

	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "new-balda-provider"}
	locator := testTelegramLocator(12, 44)
	store := &fakeSessionStore{
		recordsByAddress: map[string]baldastate.SessionRecord{
			sessionAddressKey(baldastate.ChannelTypeTelegram, "12:44"): {
				SessionID:    locator.SessionID,
				ChannelType:  baldastate.ChannelTypeTelegram,
				AddressKey:   "12:44",
				AddressJSON:  `{"chat_id":12,"topic_id":44}`,
				AgentName:    "persisted",
				WorkspaceDir: filepath.Join(t.TempDir(), "missing-workspace"),
				BranchName:   "norma/balda/missing-branch",
				Status:       baldastate.SessionStatusActive,
			},
		},
	}

	m := &Manager{
		baldaProviderName: "new-balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		workingDir:        workingDir,
		workspaces:        newTestWorkspaceManager(workingDir, t.TempDir(), currentBranch(t, ctx, workingDir)),
		workspaceEnabled:  true,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
	}

	_, err := m.RestoreSession(ctx, SessionContext{
		Locator: locator,
		UserID:  "tg-201",
	})
	if err == nil {
		t.Fatal("RestoreSession() error = nil, want missing persisted branch error")
	}
	if !strings.Contains(err.Error(), `persisted workspace branch "norma/balda/missing-branch" not found`) {
		t.Fatalf("RestoreSession() error = %q, want missing persisted branch context", err.Error())
	}
}

func TestRestoreSession_ForceRemountsCanonicalWorkspaceAfterCollision(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	initGitRepo(t, ctx, workingDir)

	writeFile(t, filepath.Join(workingDir, "seed.txt"), "seed\n")
	runGit(t, ctx, workingDir, "add", "seed.txt")
	runGit(t, ctx, workingDir, "commit", "-m", "chore: seed")

	builder := &fakeAgentBuilder{}
	runtimeManager := &fakeBaldaRuntimeManager{providerID: "new-balda-provider"}
	stateDir := t.TempDir()
	locator := testTelegramLocator(23, 102)
	branchName := "norma/balda/" + locator.SessionID
	canonicalWorkspaceDir := filepath.Join(stateDir, "sessions", locator.SessionID)
	conflictBranch := "feature/conflict-" + locator.SessionID

	runGit(t, ctx, workingDir, "branch", branchName, "HEAD")
	runGit(t, ctx, workingDir, "worktree", "add", "-b", conflictBranch, canonicalWorkspaceDir, "HEAD")
	t.Cleanup(func() {
		_ = runGitAllowError(ctx, workingDir, "worktree", "remove", "--force", canonicalWorkspaceDir)
	})

	store := &fakeSessionStore{
		recordsByAddress: map[string]baldastate.SessionRecord{
			sessionAddressKey(baldastate.ChannelTypeTelegram, locator.AddressKey): {
				SessionID:    locator.SessionID,
				ChannelType:  baldastate.ChannelTypeTelegram,
				AddressKey:   locator.AddressKey,
				AddressJSON:  locator.AddressJSON,
				AgentName:    "persisted",
				WorkspaceDir: canonicalWorkspaceDir,
				BranchName:   branchName,
				Status:       baldastate.SessionStatusActive,
			},
		},
	}

	m := &Manager{
		baldaProviderName: "new-balda-provider",
		runtimeManager:    runtimeManager,
		agentBuilder:      builder,
		workingDir:        workingDir,
		workspaces:        newTestWorkspaceManager(workingDir, stateDir, currentBranch(t, ctx, workingDir)),
		workspaceEnabled:  true,
		logger:            zerolog.Nop(),
		sessions:          make(map[string]*TopicSession),
		sessionStore:      store,
	}

	ts, err := m.RestoreSession(ctx, SessionContext{
		Locator: locator,
		UserID:  "tg-201",
	})
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}

	if got := ts.GetWorkspaceDir(); got != canonicalWorkspaceDir {
		t.Fatalf("workspace dir = %q, want %q", got, canonicalWorkspaceDir)
	}
	if head := strings.TrimSpace(runGit(t, ctx, canonicalWorkspaceDir, "rev-parse", "--abbrev-ref", "HEAD")); head != branchName {
		t.Fatalf("canonical workspace HEAD = %q, want %q", head, branchName)
	}
}

func TestTakeStartupNotice_ReturnsAndClears(t *testing.T) {
	m := &Manager{
		sessions: map[string]*TopicSession{
			"tg-1-1": {
				sessionID:     "tg-1-1",
				startupNotice: "notice text",
			},
		},
	}

	if got := m.TakeStartupNotice("tg-1-1"); got != "notice text" {
		t.Fatalf("TakeStartupNotice(first) = %q, want %q", got, "notice text")
	}
	if got := m.TakeStartupNotice("tg-1-1"); got != "" {
		t.Fatalf("TakeStartupNotice(second) = %q, want empty string", got)
	}
}

func testTelegramLocator(chatID int64, topicID int) SessionLocator {
	address := struct {
		ChatID  int64 `json:"chat_id"`
		TopicID int   `json:"topic_id"`
	}{
		ChatID:  chatID,
		TopicID: topicID,
	}
	raw, _ := json.Marshal(address)
	locator, err := NewSessionLocator(
		baldastate.ChannelTypeTelegram,
		fmt.Sprintf("%d:%d", chatID, topicID),
		string(raw),
		fmt.Sprintf("tg-%d-%d", chatID, topicID),
	)
	if err != nil {
		panic(err)
	}
	return locator
}

type fakeBoundaryObserver struct {
	boundaries         []SessionBoundary
	err                error
	before             func(context.Context, SessionBoundary)
	runtimeStillExists bool
}

func (f *fakeBoundaryObserver) BeforeSessionBoundary(ctx context.Context, boundary SessionBoundary) error {
	f.boundaries = append(f.boundaries, boundary)
	if f.before != nil {
		f.before(ctx, boundary)
	}
	return f.err
}

type fakeSessionStore struct {
	deletedSessionID string
	deleteCtxErr     error
	getByAddressErr  error
	recordsByAddress map[string]baldastate.SessionRecord
	recordsByID      map[string]baldastate.SessionRecord
	listRecords      []baldastate.SessionRecord
	upsertedRecords  []baldastate.SessionRecord
	upsertErr        error
	beforeUpsert     func()
}

func (f *fakeSessionStore) Upsert(_ context.Context, record baldastate.SessionRecord) error {
	if f.beforeUpsert != nil {
		f.beforeUpsert()
	}
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upsertedRecords = append(f.upsertedRecords, record)
	if f.recordsByAddress != nil {
		f.recordsByAddress[sessionAddressKey(record.ChannelType, record.AddressKey)] = record
	}
	if f.recordsByID != nil {
		f.recordsByID[record.SessionID] = record
	}
	return nil
}

func sessionAddressKey(channelType, addressKey string) string {
	return channelType + "|" + addressKey
}

func (f *fakeSessionStore) GetByAddress(_ context.Context, channelType, addressKey string) (baldastate.SessionRecord, bool, error) {
	if f.getByAddressErr != nil {
		return baldastate.SessionRecord{}, false, f.getByAddressErr
	}
	if f.recordsByAddress == nil {
		return baldastate.SessionRecord{}, false, nil
	}
	record, ok := f.recordsByAddress[sessionAddressKey(channelType, addressKey)]
	return record, ok, nil
}

func (f *fakeSessionStore) GetBySessionID(_ context.Context, sessionID string) (baldastate.SessionRecord, bool, error) {
	if f.recordsByID == nil {
		return baldastate.SessionRecord{}, false, nil
	}
	record, ok := f.recordsByID[sessionID]
	return record, ok, nil
}

func (f *fakeSessionStore) DeleteBySessionID(ctx context.Context, sessionID string) error {
	f.deletedSessionID = sessionID
	f.deleteCtxErr = ctx.Err()
	return nil
}

func (f *fakeSessionStore) List(context.Context) ([]baldastate.SessionRecord, error) {
	if f.listRecords == nil {
		return nil, nil
	}
	return append([]baldastate.SessionRecord(nil), f.listRecords...), nil
}

func initGitRepo(t *testing.T, ctx context.Context, workingDir string) {
	t.Helper()
	runGit(t, ctx, workingDir, "init")
	runGit(t, ctx, workingDir, "config", "user.name", "Balda Test")
	runGit(t, ctx, workingDir, "config", "user.email", "balda-test@example.com")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return string(data)
}

func runGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runGitAllowError(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

func currentBranch(t *testing.T, ctx context.Context, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"))
}
