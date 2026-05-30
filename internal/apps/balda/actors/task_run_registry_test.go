package actors

import (
	"context"
	"testing"
	"time"
)

func TestTaskRunRegistryCancelCancelsAllRunsForTask(t *testing.T) {
	t.Parallel()

	registry := NewTaskRunRegistry()

	ctxOne, cancelOne := context.WithCancel(context.Background())
	defer cancelOne()
	ctxTwo, cancelTwo := context.WithCancel(context.Background())
	defer cancelTwo()

	runOne := registry.Register("task-1", cancelOne)
	runTwo := registry.Register("task-1", cancelTwo)
	if runOne == "" || runTwo == "" || runOne == runTwo {
		t.Fatalf("Register() ids = %q/%q, want distinct non-empty run ids", runOne, runTwo)
	}

	if canceled := registry.Cancel("task-1"); !canceled {
		t.Fatal("Cancel(task-1) = false, want true")
	}
	waitContextDone(t, ctxOne, "run one cancellation")
	waitContextDone(t, ctxTwo, "run two cancellation")

	if canceled := registry.Cancel("task-1"); canceled {
		t.Fatal("Cancel(task-1) second call = true, want false")
	}
}

func TestTaskRunRegistryUnregisterRemovesSingleRunOnly(t *testing.T) {
	t.Parallel()

	registry := NewTaskRunRegistry()

	ctxOne, cancelOne := context.WithCancel(context.Background())
	defer cancelOne()
	ctxTwo, cancelTwo := context.WithCancel(context.Background())
	defer cancelTwo()

	runOne := registry.Register("task-1", cancelOne)
	runTwo := registry.Register("task-1", cancelTwo)
	registry.Unregister("task-1", runOne)

	if canceled := registry.Cancel("task-1"); !canceled {
		t.Fatal("Cancel(task-1) = false, want true for remaining run")
	}

	select {
	case <-ctxOne.Done():
		t.Fatal("unregistered run one was canceled")
	case <-time.After(50 * time.Millisecond):
	}
	waitContextDone(t, ctxTwo, "registered run two cancellation")
	if runTwo == "" {
		t.Fatal("run two id is empty")
	}
}

func waitContextDone(t *testing.T, ctx context.Context, label string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}
