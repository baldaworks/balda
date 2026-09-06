package actors

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
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

func TestTurnDispatcher_CoalescesSteeringRepliesForActiveTurn(t *testing.T) {
	t.Parallel()
	const latestMemoryAt = "2026-08-20T10:00:00Z"

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var executed turncmd.SessionTurnPayload

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(10, 0),
		RequesterUserID: "tg-101",
		MessageID:       100,
		Text:            "root",
	}
	_, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	waitForSignal(t, started, "root start")

	merged := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-101",
		MessageID:        101,
		ReplyToMessageID: 100,
		ReceivedAt:       "2026-07-20T10:00:00Z",
		Text:             "first steer",
		Metadata:         &turncmd.SessionTurnMetadata{LatestMemoryAt: latestMemoryAt},
	}
	pos, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &merged,
		Run: func(context.Context) error {
			executed = merged
			close(done)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(first steering) error = %v", err)
	}
	if pos != 1 {
		t.Fatalf("Enqueue(first steering) position = %d, want 1", pos)
	}

	second := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-101",
		MessageID:        102,
		ReplyToMessageID: 101,
		ReceivedAt:       "2026-07-20T10:01:00Z",
		Text:             "second steer",
		Metadata:         &turncmd.SessionTurnMetadata{LatestMemoryAt: latestMemoryAt},
	}
	pos, err = enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &second,
		Run: func(context.Context) error {
			t.Fatal("second steering task should be coalesced, not executed separately")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(second steering) error = %v", err)
	}
	if pos != 1 {
		t.Fatalf("Enqueue(second steering) position = %d, want 1", pos)
	}

	close(release)
	waitForSignal(t, done, "merged steering completion")

	if len(executed.SteeringMessages) != 2 {
		t.Fatalf("steering messages = %d, want 2", len(executed.SteeringMessages))
	}
	if executed.SteeringMessages[0].Text != "first steer" || executed.SteeringMessages[1].Text != "second steer" {
		t.Fatalf("steering texts = %+v", executed.SteeringMessages)
	}
	if executed.Text == "" || executed.Text == "first steer" {
		t.Fatalf("merged text = %q, want rendered batch", executed.Text)
	}
	if executed.Metadata == nil || executed.Metadata.LatestMemoryAt != latestMemoryAt {
		t.Fatalf("merged metadata = %+v, want latest_memory_at %q", executed.Metadata, latestMemoryAt)
	}
}

func TestReconcileSessionTurnMetadata(t *testing.T) {
	t.Parallel()

	const cursor = "2026-08-20T10:00:00Z"
	tests := []struct {
		name     string
		current  *turncmd.SessionTurnMetadata
		incoming *turncmd.SessionTurnMetadata
		want     string
	}{
		{name: "all absent"},
		{
			name:     "equal cursors",
			current:  &turncmd.SessionTurnMetadata{LatestMemoryAt: cursor},
			incoming: &turncmd.SessionTurnMetadata{LatestMemoryAt: cursor},
			want:     cursor,
		},
		{
			name:     "different cursors",
			current:  &turncmd.SessionTurnMetadata{LatestMemoryAt: cursor},
			incoming: &turncmd.SessionTurnMetadata{LatestMemoryAt: "2026-08-20T10:01:00Z"},
		},
		{
			name:     "incoming cursor absent",
			current:  &turncmd.SessionTurnMetadata{LatestMemoryAt: cursor},
			incoming: nil,
		},
		{
			name:     "current cursor absent",
			current:  nil,
			incoming: &turncmd.SessionTurnMetadata{LatestMemoryAt: cursor},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := reconcileSessionTurnMetadata(test.current, test.incoming)
			if test.want == "" {
				if got != nil {
					t.Fatalf("reconcileSessionTurnMetadata() = %+v, want nil", got)
				}
				return
			}
			if got == nil || got.LatestMemoryAt != test.want {
				t.Fatalf("reconcileSessionTurnMetadata() = %+v, want %q", got, test.want)
			}
		})
	}
}

func TestTurnDispatcher_DoesNotCoalesceDifferentUserSteering(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(11, 0),
		RequesterUserID: "tg-101",
		MessageID:       200,
		Text:            "root",
	}
	_, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	waitForSignal(t, started, "root start")

	first := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-101",
		MessageID:        201,
		ReplyToMessageID: 200,
		ReceivedAt:       "2026-07-20T10:00:00Z",
		Text:             "first steer",
	}
	if _, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &first,
		Run:         func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}

	second := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-202",
		MessageID:        202,
		ReplyToMessageID: 201,
		ReceivedAt:       "2026-07-20T10:01:00Z",
		Text:             "foreign steer",
	}
	pos, err := enqueueTurn(dispatcher, context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &second,
		Run:         func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Enqueue(second) error = %v", err)
	}
	if pos != 2 {
		t.Fatalf("Enqueue(second) position = %d, want 2", pos)
	}
	close(release)
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

func TestTurnDispatcher_DefersSteeringConstituentCompletionUntilBatchRuns(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	batchStarted := make(chan struct{})
	batchRelease := make(chan struct{})

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(20, 0),
		RequesterUserID: "tg-user-1",
		MessageID:       300,
		Text:            "root message",
	}
	_, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	waitForSignal(t, started, "root start")

	steer1 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-1",
		MessageID:        301,
		ReplyToMessageID: 300,
		Text:             "steer 1",
	}
	res1, pos1, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer1,
		Run: func(context.Context) error {
			close(batchStarted)
			<-batchRelease
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer1) error = %v", err)
	}
	if pos1 != 1 {
		t.Fatalf("Enqueue(steer1) position = %d, want 1", pos1)
	}

	steer2 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-1",
		MessageID:        302,
		ReplyToMessageID: 301,
		Text:             "steer 2",
	}
	res2, pos2, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer2,
		Run: func(context.Context) error {
			t.Fatal("steer2 should not run as a separate task")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer2) error = %v", err)
	}
	if pos2 != 1 {
		t.Fatalf("Enqueue(steer2) position = %d, want 1", pos2)
	}

	// Verify that neither steer1 nor steer2 has completed while root is running.
	select {
	case errVal := <-res1:
		t.Fatalf("steer1 completed prematurely with %v while root is still running", errVal)
	default:
	}
	select {
	case errVal := <-res2:
		t.Fatalf("steer2 completed prematurely with %v while root is still running", errVal)
	default:
	}

	// Finish root. Now the batch should start running.
	close(release)
	waitForSignal(t, batchStarted, "batch execution start")

	// Even while the batch is running, steer2 should still be uncompleted.
	select {
	case errVal := <-res2:
		t.Fatalf("steer2 completed prematurely with %v while batch is still running", errVal)
	default:
	}

	// Release the batch execution.
	close(batchRelease)

	// Now both constituent result channels should complete with nil.
	select {
	case errVal := <-res1:
		if errVal != nil {
			t.Fatalf("res1 error = %v, want nil", errVal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for res1")
	}

	select {
	case errVal := <-res2:
		if errVal != nil {
			t.Fatalf("res2 error = %v, want nil", errVal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for res2")
	}
}

func TestTurnDispatcher_DeduplicatesConstituentArrivalInPendingBatch(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	batchDone := make(chan struct{})
	var executedPayload turncmd.SessionTurnPayload

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(21, 0),
		RequesterUserID: "tg-user-2",
		MessageID:       400,
		Text:            "root message",
	}
	_, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	waitForSignal(t, started, "root start")

	steer1 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-2",
		MessageID:        401,
		ReplyToMessageID: 400,
		DedupeKey:        "turn-401",
		Text:             "steer 1",
	}
	res1, pos1, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer1,
		Run: func(ctx context.Context) error {
			if steer1.Metadata != nil {
				executedPayload = steer1
			} else {
				executedPayload = *dispatcher.sessions[root.Locator.SessionID].runningTurn.task.SessionTurn
			}
			close(batchDone)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer1) error = %v", err)
	}
	if pos1 != 1 {
		t.Fatalf("Enqueue(steer1) position = %d, want 1", pos1)
	}

	// Enqueue duplicate of steer1.
	steer1Dup := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-2",
		MessageID:        401,
		ReplyToMessageID: 400,
		DedupeKey:        "turn-401",
		Text:             "steer 1 duplicate text that should be ignored",
	}
	res1Dup, pos1Dup, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer1Dup,
		Run: func(context.Context) error {
			t.Fatal("duplicate should not run as separate task")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer1Dup) error = %v", err)
	}
	if pos1Dup != 1 {
		t.Fatalf("Enqueue(steer1Dup) position = %d, want 1", pos1Dup)
	}

	// Enqueue steer2.
	steer2 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-2",
		MessageID:        402,
		ReplyToMessageID: 401,
		DedupeKey:        "turn-402",
		Text:             "steer 2",
	}
	res2, pos2, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer2,
		Run: func(context.Context) error {
			t.Fatal("steer2 should not run as separate task")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer2) error = %v", err)
	}
	if pos2 != 1 {
		t.Fatalf("Enqueue(steer2) position = %d, want 1", pos2)
	}

	// Enqueue duplicate of steer2.
	steer2Dup := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-2",
		MessageID:        402,
		ReplyToMessageID: 401,
		DedupeKey:        "turn-402",
		Text:             "steer 2 duplicate text that should be ignored",
	}
	res2Dup, pos2Dup, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer2Dup,
		Run: func(context.Context) error {
			t.Fatal("steer2 duplicate should not run as separate task")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer2Dup) error = %v", err)
	}
	if pos2Dup != 1 {
		t.Fatalf("Enqueue(steer2Dup) position = %d, want 1", pos2Dup)
	}

	close(release)
	waitForSignal(t, batchDone, "batch execution done")

	if len(executedPayload.SteeringMessages) != 2 {
		t.Fatalf("batch steering messages = %d, want 2 (duplicates must not be added to batch)", len(executedPayload.SteeringMessages))
	}
	if executedPayload.SteeringMessages[0].Text != "steer 1" || executedPayload.SteeringMessages[1].Text != "steer 2" {
		t.Fatalf("batch steering messages texts = %+v", executedPayload.SteeringMessages)
	}

	// All 4 result channels (original and duplicate arrivals) must complete with nil.
	for idx, ch := range []<-chan error{res1, res1Dup, res2, res2Dup} {
		select {
		case errVal := <-ch:
			if errVal != nil {
				t.Fatalf("result[%d] error = %v, want nil", idx, errVal)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for result[%d]", idx)
		}
	}
}

func TestTurnDispatcher_PropagatesFailureToAllConstituents(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	batchStarted := make(chan struct{})

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(22, 0),
		RequesterUserID: "tg-user-3",
		MessageID:       500,
		Text:            "root",
	}
	_, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	waitForSignal(t, started, "root start")

	steer1 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-3",
		MessageID:        501,
		ReplyToMessageID: 500,
		Text:             "steer 1",
	}
	expectedErr := errors.New("simulated batch failure")
	res1, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer1,
		Run: func(context.Context) error {
			close(batchStarted)
			return expectedErr
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer1) error = %v", err)
	}

	steer2 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-3",
		MessageID:        502,
		ReplyToMessageID: 501,
		Text:             "steer 2",
	}
	res2, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer2,
		Run: func(context.Context) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer2) error = %v", err)
	}

	close(release)
	waitForSignal(t, batchStarted, "batch start")

	for idx, ch := range []<-chan error{res1, res2} {
		select {
		case errVal := <-ch:
			if !errors.Is(errVal, expectedErr) {
				t.Fatalf("result[%d] error = %v, want %v", idx, errVal, expectedErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for result[%d]", idx)
		}
	}
}

func TestTurnDispatcher_CancelSessionPropagatesToConstituents(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	canceled := make(chan struct{})

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(23, 0),
		RequesterUserID: "tg-user-4",
		MessageID:       600,
		Text:            "root",
	}
	_, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	waitForSignal(t, started, "root start")

	steer1 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-4",
		MessageID:        601,
		ReplyToMessageID: 600,
		Text:             "steer 1",
	}
	res1, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer1,
		Run: func(context.Context) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer1) error = %v", err)
	}

	steer2 := turncmd.SessionTurnPayload{
		Locator:          root.Locator,
		RequesterUserID:  "tg-user-4",
		MessageID:        602,
		ReplyToMessageID: 601,
		Text:             "steer 2",
	}
	res2, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &steer2,
		Run: func(context.Context) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(steer2) error = %v", err)
	}

	hadInFlight, dropped, err := dispatcher.CancelSession(root.Locator, true)
	if err != nil {
		t.Fatalf("CancelSession() error = %v", err)
	}
	if !hadInFlight {
		t.Fatalf("hadInFlight = false, want true")
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}

	waitForSignal(t, canceled, "root cancellation")

	for idx, ch := range []<-chan error{res1, res2} {
		select {
		case errVal := <-ch:
			if !errors.Is(errVal, context.Canceled) {
				t.Fatalf("result[%d] = %v, want %v", idx, errVal, context.Canceled)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for result[%d]", idx)
		}
	}
}

func TestTurnDispatcher_DeduplicatesConstituentArrivalInRunningBatch(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})

	root := turncmd.SessionTurnPayload{
		Locator:         baldatelegram.NewLocator(24, 0),
		RequesterUserID: "tg-user-5",
		MessageID:       700,
		DedupeKey:        "turn-700",
		Text:             "running root",
	}
	resRoot, posRoot, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &root,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	if posRoot != 0 {
		t.Fatalf("Enqueue(root) pos = %d, want 0", posRoot)
	}
	waitForSignal(t, started, "root start")

	// Enqueue duplicate while it is running.
	rootDup := turncmd.SessionTurnPayload{
		Locator:         root.Locator,
		RequesterUserID: "tg-user-5",
		MessageID:       700,
		DedupeKey:        "turn-700",
		Text:             "running root duplicate",
	}
	resDup, posDup, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID:   root.Locator.SessionID,
		SessionTurn: &rootDup,
		Run: func(context.Context) error {
			t.Fatal("duplicate should not run as separate task")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(rootDup) error = %v", err)
	}
	if posDup != 0 {
		t.Fatalf("Enqueue(rootDup) pos = %d, want 0", posDup)
	}

	close(release)

	for idx, ch := range []<-chan error{resRoot, resDup} {
		select {
		case errVal := <-ch:
			if errVal != nil {
				t.Fatalf("result[%d] error = %v, want nil", idx, errVal)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for result[%d]", idx)
		}
	}
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
