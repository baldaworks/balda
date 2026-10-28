package actors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

func TestTurnDispatcherReplacementUsesExistingFIFOAndRejectsLaterTurns(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	priorStarted := make(chan struct{})
	releasePrior := make(chan struct{})
	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})

	activeResult, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
		SessionID: "session-a",
		Run: func(context.Context) error {
			close(activeStarted)
			<-releaseActive
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(active) error = %v", err)
	}
	waitForSignal(t, activeStarted, "active turn start")

	priorResult, priorPosition, err := dispatcher.Enqueue(t.Context(), TurnTask{
		SessionID: "session-a",
		Run: func(context.Context) error {
			close(priorStarted)
			<-releasePrior
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(prior) error = %v", err)
	}
	if priorPosition != 1 {
		t.Fatalf("Enqueue(prior) position = %d, want 1", priorPosition)
	}

	replacementResult, replacementPosition, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID:   "session-a",
		OperationID: "replace-a",
		InputHash:   "hash-a",
		Run: func(context.Context) error {
			close(replacementStarted)
			<-releaseReplacement
			return nil
		},
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement() error = %v", err)
	}
	if replacementPosition != 2 {
		t.Fatalf("EnqueueReplacement() position = %d, want 2", replacementPosition)
	}

	if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrTurnReplacementPending) {
		t.Fatalf("Enqueue(later pending) error = %v, want %v", err, ErrTurnReplacementPending)
	}

	unrelatedDone := make(chan struct{})
	if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
		SessionID: "session-b",
		Run: func(context.Context) error {
			close(unrelatedDone)
			return nil
		},
	}); err != nil {
		t.Fatalf("Enqueue(unrelated) error = %v", err)
	}
	waitForSignal(t, unrelatedDone, "unrelated turn completion")

	close(releaseActive)
	waitTurnResult(t, activeResult, nil, "active turn")
	waitForSignal(t, priorStarted, "prior turn start")
	ensureChannelBlocked(t, replacementStarted, "replacement before prior completion")

	close(releasePrior)
	waitTurnResult(t, priorResult, nil, "prior turn")
	waitForSignal(t, replacementStarted, "replacement start")
	if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrTurnReplacementPending) {
		t.Fatalf("Enqueue(later running) error = %v, want %v", err, ErrTurnReplacementPending)
	}

	close(releaseReplacement)
	waitTurnResult(t, replacementResult, nil, "replacement")

	laterDone := make(chan struct{})
	if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
		SessionID: "session-a",
		Run: func(context.Context) error {
			close(laterDone)
			return nil
		},
	}); err != nil {
		t.Fatalf("Enqueue(after replacement) error = %v", err)
	}
	waitForSignal(t, laterDone, "turn after replacement")
}

func TestTurnDispatcherReplacementAllowsExactOrdinaryRedelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		queueTurn func(*testing.T, *TurnDispatcher, chan struct{}, chan struct{}) (<-chan error, func())
	}{
		{
			name: "active",
			queueTurn: func(t *testing.T, dispatcher *TurnDispatcher, started, release chan struct{}) (<-chan error, func()) {
				t.Helper()
				result, _, err := dispatcher.Enqueue(t.Context(), dedupedTurn("session-a", "turn-a", started, release))
				if err != nil {
					t.Fatalf("Enqueue(active) error = %v", err)
				}
				waitForSignal(t, started, "active turn start")
				return result, func() {}
			},
		},
		{
			name: "pending",
			queueTurn: func(t *testing.T, dispatcher *TurnDispatcher, started, release chan struct{}) (<-chan error, func()) {
				t.Helper()
				blockerStarted := make(chan struct{})
				blockerRelease := make(chan struct{})
				if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error {
					close(blockerStarted)
					<-blockerRelease
					return nil
				}}); err != nil {
					t.Fatalf("Enqueue(blocker) error = %v", err)
				}
				waitForSignal(t, blockerStarted, "blocker start")
				result, _, err := dispatcher.Enqueue(t.Context(), dedupedTurn("session-a", "turn-a", started, release))
				if err != nil {
					t.Fatalf("Enqueue(pending) error = %v", err)
				}
				return result, func() { close(blockerRelease) }
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := NewTurnDispatcher(zerolog.Nop())
			defer func() { _ = dispatcher.Shutdown(context.Background()) }()

			started := make(chan struct{})
			release := make(chan struct{})
			original, startOriginal := test.queueTurn(t, dispatcher, started, release)
			replacement, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
				SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a", Run: func(context.Context) error { return nil },
			})
			if err != nil {
				t.Fatalf("EnqueueReplacement() error = %v", err)
			}

			duplicateRan := make(chan struct{})
			duplicateTask := dedupedTurn("session-a", "turn-a", make(chan struct{}), duplicateRan)
			duplicateTask.Run = func(context.Context) error {
				close(duplicateRan)
				return nil
			}
			duplicate, _, err := dispatcher.Enqueue(t.Context(), duplicateTask)
			if err != nil {
				t.Fatalf("Enqueue(duplicate) error = %v", err)
			}
			if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrTurnReplacementPending) {
				t.Fatalf("Enqueue(unrelated) error = %v, want %v", err, ErrTurnReplacementPending)
			}

			startOriginal()
			waitForSignal(t, started, "original turn start")
			close(release)
			waitTurnResult(t, original, nil, "original turn")
			waitTurnResult(t, duplicate, nil, "duplicate turn")
			waitTurnResult(t, replacement, nil, "replacement")
			ensureChannelBlocked(t, duplicateRan, "duplicate runner")
		})
	}
}

func dedupedTurn(sessionID, dedupeKey string, started chan<- struct{}, release <-chan struct{}) TurnTask {
	return TurnTask{
		SessionID: sessionID,
		SessionTurn: &turncmd.SessionTurnPayload{
			DedupeKey: dedupeKey,
		},
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}
}

func TestTurnDispatcherReplacementDeduplicatesAndReplaysBoundedResult(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
		SessionID: "session-a",
		Run: func(context.Context) error {
			close(activeStarted)
			<-releaseActive
			return nil
		},
	}); err != nil {
		t.Fatalf("Enqueue(active) error = %v", err)
	}
	waitForSignal(t, activeStarted, "active turn start")

	boundaryStarted := make(chan struct{})
	releaseBoundary := make(chan struct{})
	boundaryErr := context.DeadlineExceeded
	var calls atomic.Int32
	task := ReplacementTask{
		SessionID:   "session-a",
		OperationID: "replace-a",
		InputHash:   "hash-a",
		Run: func(context.Context) error {
			calls.Add(1)
			close(boundaryStarted)
			<-releaseBoundary
			return boundaryErr
		},
	}
	first, firstPosition, err := dispatcher.EnqueueReplacement(t.Context(), task)
	if err != nil {
		t.Fatalf("EnqueueReplacement(first) error = %v", err)
	}
	duplicate, duplicatePosition, err := dispatcher.EnqueueReplacement(t.Context(), task)
	if err != nil {
		t.Fatalf("EnqueueReplacement(duplicate) error = %v", err)
	}
	if duplicatePosition != firstPosition {
		t.Fatalf("duplicate position = %d, want %d", duplicatePosition, firstPosition)
	}

	conflict := task
	conflict.InputHash = "different-hash"
	if _, _, err := dispatcher.EnqueueReplacement(t.Context(), conflict); !errors.Is(err, ErrTurnReplacementConflict) {
		t.Fatalf("EnqueueReplacement(conflict) error = %v, want %v", err, ErrTurnReplacementConflict)
	}
	other := task
	other.OperationID = "replace-b"
	if _, _, err := dispatcher.EnqueueReplacement(t.Context(), other); !errors.Is(err, ErrTurnReplacementPending) {
		t.Fatalf("EnqueueReplacement(other) error = %v, want %v", err, ErrTurnReplacementPending)
	}

	close(releaseActive)
	waitForSignal(t, boundaryStarted, "replacement start")
	close(releaseBoundary)
	waitTurnResult(t, first, boundaryErr, "first replacement")
	waitTurnResult(t, duplicate, boundaryErr, "duplicate replacement")
	if got := calls.Load(); got != 1 {
		t.Fatalf("replacement calls = %d, want 1", got)
	}
	var callsB atomic.Int32
	resultB, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID: "session-a", OperationID: "replace-b", InputHash: "hash-b",
		Run: func(context.Context) error { callsB.Add(1); return nil },
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement(B) error = %v", err)
	}
	waitTurnResult(t, resultB, nil, "replacement B")

	replay, _, err := dispatcher.EnqueueReplacement(t.Context(), task)
	if err != nil {
		t.Fatalf("EnqueueReplacement(replay) error = %v", err)
	}
	waitTurnResult(t, replay, boundaryErr, "replacement replay")
	if got := calls.Load(); got != 1 {
		t.Fatalf("replacement calls after replay = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Fatalf("replacement B calls = %d, want 1", got)
	}
	if _, _, err := dispatcher.EnqueueReplacement(t.Context(), conflict); !errors.Is(err, ErrTurnReplacementConflict) {
		t.Fatalf("EnqueueReplacement(terminal conflict) error = %v, want %v", err, ErrTurnReplacementConflict)
	}
}

func TestTurnDispatcherReplacementTerminalCacheIsBounded(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	var calls atomic.Int32
	for i := range terminalReplacementLimit + 2 {
		result, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID:   "session-a",
			OperationID: fmt.Sprintf("replace-%02d", i),
			InputHash:   fmt.Sprintf("hash-%02d", i),
			Run: func(context.Context) error {
				calls.Add(1)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement(%d) error = %v", i, err)
		}
		waitTurnResult(t, result, nil, fmt.Sprintf("replacement %d", i))
	}
	if got := calls.Load(); got != terminalReplacementLimit+2 {
		t.Fatalf("replacement calls = %d, want %d", got, terminalReplacementLimit+2)
	}

	dispatcher.mu.Lock()
	queue := dispatcher.sessions["session-a"]
	if queue == nil {
		dispatcher.mu.Unlock()
		t.Fatal("session lane was evicted while continuously active")
	}
	if got := len(queue.terminalReplacements); got != terminalReplacementLimit {
		dispatcher.mu.Unlock()
		t.Fatalf("terminal cache size = %d, want %d", got, terminalReplacementLimit)
	}
	if got := len(queue.terminalOrder); got != terminalReplacementLimit {
		dispatcher.mu.Unlock()
		t.Fatalf("terminal order size = %d, want %d", got, terminalReplacementLimit)
	}
	orderBeforeReplay := append([]string(nil), queue.terminalOrder...)
	if _, ok := queue.terminalReplacements["replace-00"]; ok {
		dispatcher.mu.Unlock()
		t.Fatal("oldest replacement remains cached")
	}
	if _, ok := queue.terminalReplacements[fmt.Sprintf("replace-%02d", terminalReplacementLimit+1)]; !ok {
		dispatcher.mu.Unlock()
		t.Fatal("newest replacement was evicted")
	}
	dispatcher.mu.Unlock()

	newestID := fmt.Sprintf("replace-%02d", terminalReplacementLimit+1)
	newestHash := fmt.Sprintf("hash-%02d", terminalReplacementLimit+1)
	newestReplay, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID: "session-a", OperationID: newestID, InputHash: newestHash,
		Run: func(context.Context) error { calls.Add(1000); return nil },
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement(newest replay) error = %v", err)
	}
	waitTurnResult(t, newestReplay, nil, "newest replay")
	newestConflict := ReplacementTask{
		SessionID: "session-a", OperationID: newestID, InputHash: "different-hash", Run: func(context.Context) error { return nil },
	}
	if _, _, err := dispatcher.EnqueueReplacement(t.Context(), newestConflict); !errors.Is(err, ErrTurnReplacementConflict) {
		t.Fatalf("EnqueueReplacement(newest conflict) error = %v, want %v", err, ErrTurnReplacementConflict)
	}
	dispatcher.mu.Lock()
	if !slices.Equal(queue.terminalOrder, orderBeforeReplay) {
		got := append([]string(nil), queue.terminalOrder...)
		dispatcher.mu.Unlock()
		t.Fatalf("terminal replay changed insertion order: got %v, want %v", got, orderBeforeReplay)
	}
	dispatcher.mu.Unlock()

	oldestReplay, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID: "session-a", OperationID: "replace-00", InputHash: "hash-00",
		Run: func(context.Context) error { calls.Add(1); return nil },
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement(evicted replay) error = %v", err)
	}
	waitTurnResult(t, oldestReplay, nil, "evicted replay")
	if got := calls.Load(); got != terminalReplacementLimit+3 {
		t.Fatalf("replacement calls after replays = %d, want %d", got, terminalReplacementLimit+3)
	}
}

func TestRememberReplacementResultUpdatePreservesInsertionOrder(t *testing.T) {
	t.Parallel()

	queue := &sessionTurnQueue{}
	for i := range terminalReplacementLimit {
		rememberReplacementResult(queue, fmt.Sprintf("operation-%02d", i), replacementResult{inputHash: fmt.Sprintf("hash-%02d", i)})
	}
	wantOrder := append([]string(nil), queue.terminalOrder...)
	rememberReplacementResult(queue, "operation-05", replacementResult{inputHash: "updated", err: ErrTurnReplacementFailed})
	if !slices.Equal(queue.terminalOrder, wantOrder) {
		t.Fatalf("updated result changed insertion order: got %v, want %v", queue.terminalOrder, wantOrder)
	}
	if got := len(queue.terminalReplacements); got != terminalReplacementLimit {
		t.Fatalf("terminal cache size = %d, want %d", got, terminalReplacementLimit)
	}
	updated := queue.terminalReplacements["operation-05"]
	if updated.inputHash != "updated" || !errors.Is(updated.err, ErrTurnReplacementFailed) {
		t.Fatalf("updated result = %+v, want updated bounded result", updated)
	}
	for i := terminalReplacementLimit; i < terminalReplacementLimit*2; i++ {
		rememberReplacementResult(queue, fmt.Sprintf("operation-%02d", i), replacementResult{inputHash: fmt.Sprintf("hash-%02d", i)})
		if got := len(queue.terminalReplacements); got > terminalReplacementLimit {
			t.Fatalf("terminal cache size after insertion %d = %d, limit %d", i, got, terminalReplacementLimit)
		}
	}
}

func TestTurnDispatcherIdleEvictionDropsTerminalReplacementCache(t *testing.T) {
	t.Parallel()

	queue := &sessionTurnQueue{
		terminalReplacements: map[string]replacementResult{"replace-a": {inputHash: "hash-a"}},
		terminalOrder:        []string{"replace-a"},
	}
	dispatcher := &TurnDispatcher{sessions: map[string]*sessionTurnQueue{"session-a": queue}}
	if !dispatcher.evictIdleSessionLocked("session-a", queue) {
		t.Fatal("evictIdleSessionLocked() = false, want true")
	}
	if _, ok := dispatcher.sessions["session-a"]; ok {
		t.Fatal("idle session lane and terminal cache remain reachable")
	}
}

func TestTurnDispatcherReplacementBoundsErrorsAndLogs(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	dispatcher := NewTurnDispatcher(zerolog.New(&logs))
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	const secret = "provider-secret-do-not-log"
	rawErr := errors.New(secret)
	result, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a",
		Run: func(context.Context) error { return rawErr },
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement() error = %v", err)
	}
	waitTurnResult(t, result, ErrTurnReplacementFailed, "failed replacement")

	replay, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a",
		Run: func(context.Context) error { t.Fatal("terminal replay ran callback"); return nil },
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement(replay) error = %v", err)
	}
	waitTurnResult(t, replay, ErrTurnReplacementFailed, "failed replacement replay")
	drain, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatalf("Enqueue(drain) error = %v", err)
	}
	waitTurnResult(t, drain, nil, "log drain")

	gotLogs := logs.String()
	if strings.Contains(gotLogs, secret) || strings.Contains(gotLogs, `"error":`) {
		t.Fatalf("replacement log contains raw error: %s", gotLogs)
	}
	if !strings.Contains(gotLogs, `"error_class":"replacement_failed"`) || !strings.Contains(gotLogs, `"message":"replacement task failed"`) {
		t.Fatalf("replacement log lacks bounded classification: %s", gotLogs)
	}
}

func TestBoundedReplacementErrorClassification(t *testing.T) {
	t.Parallel()

	privateErr := errors.New("private callback error")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "nil", err: nil, want: nil},
		{name: "canceled", err: fmt.Errorf("wrapped: %w", context.Canceled), want: context.Canceled},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), want: context.DeadlineExceeded},
		{name: "pending", err: fmt.Errorf("wrapped: %w", ErrTurnReplacementPending), want: ErrTurnReplacementPending},
		{name: "conflict", err: fmt.Errorf("wrapped: %w", ErrTurnReplacementConflict), want: ErrTurnReplacementConflict},
		{name: "failed", err: fmt.Errorf("wrapped: %w", ErrTurnReplacementFailed), want: ErrTurnReplacementFailed},
		{name: "unknown", err: privateErr, want: ErrTurnReplacementFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := boundedReplacementError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("boundedReplacementError(%v) = %v, want %v", test.err, got, test.want)
			}
			if test.err == privateErr && errors.Is(got, privateErr) {
				t.Fatalf("boundedReplacementError(private) retains raw error: %v", got)
			}
		})
	}
}

func TestTurnDispatcherReplacementDoesNotIncreasePendingTurnCount(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	release := make(chan struct{})
	started := make(chan struct{})
	running := enqueueCurrentTurnTest(t, dispatcher, "session-a", "requester", started, release)
	waitForSignal(t, started, "current turn start")
	replacement, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
		SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a", Run: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("EnqueueReplacement() error = %v", err)
	}
	current, ok := dispatcher.CurrentTurn("session-a")
	if !ok {
		t.Fatal("CurrentTurn() = absent, want active turn")
	}
	if current.PendingTurns != 0 {
		t.Fatalf("CurrentTurn().PendingTurns = %d, want 0", current.PendingTurns)
	}
	close(release)
	waitTurnResult(t, running, nil, "current turn")
	waitTurnResult(t, replacement, nil, "replacement")
}

func TestTurnDispatcherReplacementConcurrentRedeliveryRunsOnce(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	task := ReplacementTask{
		SessionID:   "session-a",
		OperationID: "replace-a",
		InputHash:   "hash-a",
		Run: func(context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		},
	}

	const admissions = 32
	results := make(chan (<-chan error), admissions)
	errs := make(chan error, admissions)
	var wg sync.WaitGroup
	for range admissions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := dispatcher.EnqueueReplacement(t.Context(), task)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("EnqueueReplacement() error = %v", err)
	}
	waitForSignal(t, started, "replacement start")
	close(release)
	for result := range results {
		waitTurnResult(t, result, nil, "concurrent replacement")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("replacement calls = %d, want 1", got)
	}
}

func TestTurnDispatcherReplacementCancellationPanicAndShutdown(t *testing.T) {
	t.Parallel()

	t.Run("deadline before run", func(t *testing.T) {
		dispatcher := NewTurnDispatcher(zerolog.Nop())
		defer func() { _ = dispatcher.Shutdown(context.Background()) }()

		activeStarted := make(chan struct{})
		releaseActive := make(chan struct{})
		if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error {
			close(activeStarted)
			<-releaseActive
			return nil
		}}); err != nil {
			t.Fatalf("Enqueue(active) error = %v", err)
		}
		waitForSignal(t, activeStarted, "active turn start")

		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		var calls atomic.Int32
		result, _, err := dispatcher.EnqueueReplacement(ctx, ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a",
			Run: func(context.Context) error { calls.Add(1); return nil },
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement() error = %v", err)
		}
		duplicate, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a", Run: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement(duplicate) error = %v", err)
		}
		close(releaseActive)
		waitTurnResult(t, result, context.DeadlineExceeded, "deadline replacement")
		waitTurnResult(t, duplicate, context.DeadlineExceeded, "deadline duplicate")
		if got := calls.Load(); got != 0 {
			t.Fatalf("replacement calls = %d, want 0", got)
		}
	})

	t.Run("panic is bounded", func(t *testing.T) {
		dispatcher := NewTurnDispatcher(zerolog.Nop())
		defer func() { _ = dispatcher.Shutdown(context.Background()) }()

		result, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a",
			Run: func(context.Context) error { panic("private panic") },
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement() error = %v", err)
		}
		waitTurnResult(t, result, ErrTurnReplacementFailed, "panicked replacement")
	})

	t.Run("cancel session drops pending boundary", func(t *testing.T) {
		dispatcher := NewTurnDispatcher(zerolog.Nop())
		defer func() { _ = dispatcher.Shutdown(context.Background()) }()

		activeStarted := make(chan struct{})
		activeStopped := make(chan struct{})
		if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(ctx context.Context) error {
			close(activeStarted)
			<-ctx.Done()
			close(activeStopped)
			return ctx.Err()
		}}); err != nil {
			t.Fatalf("Enqueue(active) error = %v", err)
		}
		waitForSignal(t, activeStarted, "active turn start")

		var calls atomic.Int32
		result, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a",
			Run: func(context.Context) error { calls.Add(1); return nil },
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement() error = %v", err)
		}
		duplicate, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a", Run: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement(duplicate) error = %v", err)
		}
		hadInFlight, dropped, err := dispatcher.CancelSession(session.SessionLocator{SessionID: "session-a"}, true)
		if err != nil {
			t.Fatalf("CancelSession() error = %v", err)
		}
		if !hadInFlight || dropped != 1 {
			t.Fatalf("CancelSession() = (%t, %d), want (true, 1)", hadInFlight, dropped)
		}
		waitForSignal(t, activeStopped, "active turn cancellation")
		waitTurnResult(t, result, context.Canceled, "dropped replacement")
		waitTurnResult(t, duplicate, context.Canceled, "dropped duplicate")
		if got := calls.Load(); got != 0 {
			t.Fatalf("replacement calls = %d, want 0", got)
		}

		afterDone := make(chan struct{})
		if _, _, err := dispatcher.Enqueue(t.Context(), TurnTask{SessionID: "session-a", Run: func(context.Context) error {
			close(afterDone)
			return nil
		}}); err != nil {
			t.Fatalf("Enqueue(after cancel) error = %v", err)
		}
		waitForSignal(t, afterDone, "turn after canceled replacement")
	})

	t.Run("shutdown completes pending and running", func(t *testing.T) {
		dispatcher := NewTurnDispatcher(zerolog.Nop())
		started := make(chan struct{})
		result, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a",
			Run: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			},
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement() error = %v", err)
		}
		waitForSignal(t, started, "replacement start")
		duplicate, _, err := dispatcher.EnqueueReplacement(t.Context(), ReplacementTask{
			SessionID: "session-a", OperationID: "replace-a", InputHash: "hash-a", Run: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("EnqueueReplacement(duplicate) error = %v", err)
		}
		if err := dispatcher.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
		waitTurnResult(t, result, context.Canceled, "replacement shutdown")
		waitTurnResult(t, duplicate, context.Canceled, "replacement duplicate shutdown")
	})
}

func waitTurnResult(t *testing.T, result <-chan error, want error, label string) {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("%s result = %v, want %v", label, err, want)
		}
	case <-t.Context().Done():
		t.Fatalf("waiting for %s: %v", label, t.Context().Err())
	}
}

func ensureChannelBlocked(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected signal: %s", label)
	default:
	}
}
