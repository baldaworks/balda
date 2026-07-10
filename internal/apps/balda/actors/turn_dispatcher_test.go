package actors

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/rs/zerolog"
)

func TestTurnDispatcher_PerSessionFIFOQueue(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	thirdDone := make(chan struct{})

	var mu sync.Mutex
	order := make([]string, 0, 3)

	pos, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-1-0",
		Run: func(context.Context) error {
			close(firstStarted)
			<-releaseFirst
			mu.Lock()
			order = append(order, "first")
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	if pos != 0 {
		t.Fatalf("Enqueue(first) position = %d, want 0", pos)
	}
	waitForSignal(t, firstStarted, "first task start")

	pos, err = enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-1-0",
		Run: func(context.Context) error {
			mu.Lock()
			order = append(order, "second")
			mu.Unlock()
			close(secondDone)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(second) error = %v", err)
	}
	if pos != 1 {
		t.Fatalf("Enqueue(second) position = %d, want 1", pos)
	}

	pos, err = enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-1-0",
		Run: func(context.Context) error {
			mu.Lock()
			order = append(order, "third")
			mu.Unlock()
			close(thirdDone)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(third) error = %v", err)
	}
	if pos != 2 {
		t.Fatalf("Enqueue(third) position = %d, want 2", pos)
	}

	close(releaseFirst)
	waitForSignal(t, secondDone, "second task completion")
	waitForSignal(t, thirdDone, "third task completion")

	mu.Lock()
	defer mu.Unlock()
	got := append([]string(nil), order...)
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("execution order len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execution order[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestTurnDispatcher_QueueLimit(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	_, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-2-0",
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(active) error = %v", err)
	}
	waitForSignal(t, started, "active task start")

	for i := 0; i < perSessionQueueLimit; i++ {
		pos, enqueueErr := enqueueTurn(dispatcher, context.Background(), TurnTask{
			SessionID: "tg-2-0",
			Run: func(context.Context) error {
				return nil
			},
		})
		if enqueueErr != nil {
			t.Fatalf("Enqueue(pending %d) error = %v", i, enqueueErr)
		}
		wantPos := i + 1
		if pos != wantPos {
			t.Fatalf("Enqueue(pending %d) position = %d, want %d", i, pos, wantPos)
		}
	}

	if _, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-2-0",
		Run: func(context.Context) error {
			return nil
		},
	}); !errors.Is(err, ErrTurnQueueFull) {
		t.Fatalf("Enqueue(over limit) error = %v, want %v", err, ErrTurnQueueFull)
	}

	close(release)
}

func TestTurnDispatcher_CancelSessionClearsPendingAndCancelsRunning(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	canceled := make(chan struct{})
	pendingExecuted := make(chan struct{})

	_, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-3-0",
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(active) error = %v", err)
	}
	waitForSignal(t, started, "active task start")

	pendingResult, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID: "tg-3-0",
		Run: func(context.Context) error {
			close(pendingExecuted)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(pending) error = %v", err)
	}

	hadInFlight, dropped, err := dispatcher.CancelSession(baldatelegram.NewLocator(3, 0), true)
	if err != nil {
		t.Fatalf("CancelSession() error = %v", err)
	}
	if !hadInFlight {
		t.Fatalf("CancelSession() hadInFlight = false, want true")
	}
	if dropped != 1 {
		t.Fatalf("CancelSession() dropped = %d, want 1", dropped)
	}

	waitForSignal(t, canceled, "active task cancellation")
	select {
	case resultErr := <-pendingResult:
		if !errors.Is(resultErr, context.Canceled) {
			t.Fatalf("pending result = %v, want %v", resultErr, context.Canceled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for dropped task completion")
	}
	ensureNoSignal(t, pendingExecuted, 200*time.Millisecond, "pending task should be dropped after cancel")
}

func TestTurnDispatcher_TaskContextCancellationStopsRunningTask(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	taskCtx, cancelTask := context.WithCancel(context.Background())
	defer cancelTask()

	started := make(chan struct{})
	stopped := make(chan struct{})

	_, _, err := dispatcher.Enqueue(taskCtx, TurnTask{
		SessionID: "tg-ctx-1",
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	waitForSignal(t, started, "task start")
	cancelTask()
	waitForSignal(t, stopped, "task context cancellation")
}

func TestTurnDispatcher_SkipsTaskRunWhenTaskContextAlreadyCanceled(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	executed := make(chan struct{}, 1)

	_, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-ctx-2",
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	waitForSignal(t, started, "first task start")

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = dispatcher.Enqueue(canceledCtx, TurnTask{
		SessionID: "tg-ctx-2",
		Run: func(context.Context) error {
			executed <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(canceled) error = %v", err)
	}

	close(release)
	ensureNoSignal(t, executed, 250*time.Millisecond, "canceled task should not run")
}

func TestTurnDispatcher_AllowsConcurrentSessions(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	startedA := make(chan struct{})
	startedB := make(chan struct{})
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})

	_, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-4-1",
		Run: func(context.Context) error {
			close(startedA)
			<-releaseA
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(session A) error = %v", err)
	}
	_, err = enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID: "tg-4-2",
		Run: func(context.Context) error {
			close(startedB)
			<-releaseB
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(session B) error = %v", err)
	}

	waitForSignal(t, startedA, "session A start")
	waitForSignal(t, startedB, "session B start")
	close(releaseA)
	close(releaseB)
}

func waitForSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

func ensureNoSignal(t *testing.T, ch <-chan struct{}, wait time.Duration, label string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected signal: %s", label)
	case <-time.After(wait):
	}
}

func enqueueTurn(dispatcher *TurnDispatcher, ctx context.Context, task TurnTask) (int, error) {
	_, position, err := dispatcher.Enqueue(ctx, task)
	return position, err
}
