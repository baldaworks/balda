package actors

import (
	"context"
	"sync"
	"testing"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adkrunner "google.golang.org/adk/v2/runner"
)

func TestQueuedTurnAcquiresSuccessorRuntimeAfterDequeue(t *testing.T) {
	oldSlot := newDispatcherRuntimeSlot("provider-session-old", "epoch")
	newSlot := newDispatcherRuntimeSlot("provider-session-new", "epoch")
	active := &dispatcherActiveSession{current: oldSlot}
	executor := &dispatcherRuntimeExecutor{
		oldStarted:  make(chan struct{}),
		continueOld: make(chan struct{}),
		calls:       make(chan string, 2),
	}
	runner := sessionturn.New(dispatcherSessionAccessor{active: active}, executor, nil, zerolog.Nop())
	dispatcher := NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() { _ = dispatcher.Shutdown(context.Background()) })

	enqueue := func() <-chan error {
		payload := turncmd.SessionTurnPayload{ArchiveID: "epoch", Locator: baldasession.SessionLocator{SessionID: "tg-1-0"}}
		result, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
			SessionID: "tg-1-0",
			Run: func(ctx context.Context) error {
				return runner.RunSessionTurnPayload(ctx, payload)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	first := enqueue()
	<-executor.oldStarted
	second := enqueue()
	active.swap(newSlot)
	oldSlot.retire()
	select {
	case <-oldSlot.closed:
		t.Fatal("predecessor runtime closed under active turn lease")
	default:
	}

	close(executor.continueOld)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	<-oldSlot.closed
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if firstID, secondID := <-executor.calls, <-executor.calls; firstID != "provider-session-old" || secondID != "provider-session-new" {
		t.Fatalf("provider sessions = %q, %q, want predecessor then successor", firstID, secondID)
	}
	if got := oldSlot.releaseCalls; got != 1 {
		t.Fatalf("predecessor release calls = %d, want 1", got)
	}
}

type dispatcherActiveSession struct {
	mu      sync.Mutex
	current *dispatcherRuntimeSlot
}

func (s *dispatcherActiveSession) GetSessionID() string { return "tg-1-0" }

func (s *dispatcherActiveSession) AcquireRuntime() (sessionturn.RuntimeLease, error) {
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	return current.acquire()
}

func (s *dispatcherActiveSession) swap(next *dispatcherRuntimeSlot) {
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
}

type dispatcherSessionAccessor struct{ active *dispatcherActiveSession }

func (a dispatcherSessionAccessor) GetSession(sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	return a.active, nil
}
func (a dispatcherSessionAccessor) RestoreSession(context.Context, sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	return a.active, nil
}
func (a dispatcherSessionAccessor) EnsureSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return a.active, nil
}
func (a dispatcherSessionAccessor) EnsureTransientSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return a.active, nil
}

type dispatcherRuntimeExecutor struct {
	oldStarted  chan struct{}
	continueOld chan struct{}
	calls       chan string
}

func (e *dispatcherRuntimeExecutor) ExecuteSessionTurn(_ context.Context, request sessionturn.Request) error {
	if request.Runtime.SessionID() == "provider-session-old" {
		close(e.oldStarted)
		<-e.continueOld
	}
	e.calls <- request.Runtime.SessionID()
	return nil
}

type dispatcherRuntimeSlot struct {
	mu           sync.Mutex
	sessionID    string
	archiveID    string
	leases       int
	retired      bool
	releaseCalls int
	closed       chan struct{}
	closeOnce    sync.Once
}

func newDispatcherRuntimeSlot(sessionID, archiveID string) *dispatcherRuntimeSlot {
	return &dispatcherRuntimeSlot{sessionID: sessionID, archiveID: archiveID, closed: make(chan struct{})}
}

func (s *dispatcherRuntimeSlot) acquire() (sessionturn.RuntimeLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired {
		return nil, baldasession.ErrRuntimeUnavailable
	}
	s.leases++
	return &dispatcherRuntimeLease{slot: s}, nil
}

func (s *dispatcherRuntimeSlot) retire() {
	s.mu.Lock()
	s.retired = true
	closeNow := s.leases == 0
	s.mu.Unlock()
	if closeNow {
		s.closeOnce.Do(func() { close(s.closed) })
	}
}

type dispatcherRuntimeLease struct {
	slot *dispatcherRuntimeSlot
	once sync.Once
}

func (*dispatcherRuntimeLease) Runner() *adkrunner.Runner { return nil }
func (*dispatcherRuntimeLease) UserID() string            { return "telegram-user" }
func (l *dispatcherRuntimeLease) SessionID() string       { return l.slot.sessionID }
func (l *dispatcherRuntimeLease) ArchiveID() string       { return l.slot.archiveID }
func (*dispatcherRuntimeLease) RuntimeSnapshotID() string { return "snapshot" }
func (*dispatcherRuntimeLease) RuntimeStateValue(context.Context, string) (any, bool, error) {
	return nil, false, nil
}
func (l *dispatcherRuntimeLease) Release() {
	l.once.Do(func() {
		l.slot.mu.Lock()
		l.slot.leases--
		l.slot.releaseCalls++
		closeNow := l.slot.retired && l.slot.leases == 0
		l.slot.mu.Unlock()
		if closeNow {
			l.slot.closeOnce.Do(func() { close(l.slot.closed) })
		}
	})
}
