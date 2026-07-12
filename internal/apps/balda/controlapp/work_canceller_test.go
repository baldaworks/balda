package controlapp

import (
	"context"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/appports"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

type cancelSessionCall struct {
	SessionID   string
	ClearQueued bool
}

type fakeTurnQueue struct {
	cancelCalls []cancelSessionCall
}

func (f *fakeTurnQueue) Enqueue(context.Context, appports.TurnTask) (<-chan error, int, error) {
	return nil, 0, nil
}

func (f *fakeTurnQueue) CancelSession(locator baldasession.SessionLocator, clearQueued bool) (bool, int, error) {
	f.cancelCalls = append(f.cancelCalls, cancelSessionCall{SessionID: locator.SessionID, ClearQueued: clearQueued})
	return true, 0, nil
}

type fakeJobRuns struct {
	canceled []string
}

func (f *fakeJobRuns) Cancel(jobID string) bool {
	f.canceled = append(f.canceled, jobID)
	return true
}

func TestSessionWorkCancellerCancelsQueueJobsAndRuns(t *testing.T) {
	ctx := context.Background()
	provider, err := baldastate.NewSQLiteProvider(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	lifecycle, err := baldajobs.NewJobLifecycleServiceForTests(provider.Jobs(), nil)
	if err != nil {
		t.Fatalf("NewJobLifecycleServiceForTests() error = %v", err)
	}

	locator := baldatelegram.NewLocator(9001, 0)
	_, err = lifecycle.Create(ctx, baldastate.JobRecord{
		ID:        "task-session",
		SessionID: locator.SessionID,
		Objective: "active",
		Status:    baldastate.JobStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}

	turns := &fakeTurnQueue{}
	runs := &fakeJobRuns{}
	canceller := NewSessionWorkCanceller(turns, lifecycle, runs, zerolog.Nop())

	if err := canceller.CancelWork(ctx, locator, "command.reset", "session canceled by reset command"); err != nil {
		t.Fatalf("CancelWork() error = %v", err)
	}

	if len(turns.cancelCalls) != 1 || turns.cancelCalls[0].SessionID != locator.SessionID || !turns.cancelCalls[0].ClearQueued {
		t.Fatalf("CancelSession calls = %+v, want one queued session cancel", turns.cancelCalls)
	}
	if len(runs.canceled) != 1 || runs.canceled[0] != "task-session" {
		t.Fatalf("canceled runs = %+v, want task-session", runs.canceled)
	}
	task, ok, err := lifecycle.Get(ctx, "task-session")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.JobStatusCanceled {
		t.Fatalf("task = %+v found=%v, want canceled", task, ok)
	}
}
