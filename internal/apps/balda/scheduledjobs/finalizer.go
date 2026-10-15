package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// RunSessionCloser is the scheduler's narrow port for deleting private runtime state.
type RunSessionCloser interface {
	CloseRunSession(ctx context.Context, sessionID, userID string) error
}

// RunFinalizer closes a private session after execution and any report settle.
type RunFinalizer struct {
	runs       state.ScheduleRunStore
	jobs       state.JobLifecycleStore
	deliveries state.DeliveryStore
	sessions   RunSessionCloser
	now        func() time.Time
	mu         sync.Mutex
	lastAt     time.Time
	lastID     string
}

func NewRunFinalizer(runs state.ScheduleRunStore, jobs state.JobLifecycleStore,
	deliveries state.DeliveryStore, sessions RunSessionCloser) *RunFinalizer {
	return &RunFinalizer{runs: runs, jobs: jobs, deliveries: deliveries,
		sessions: sessions, now: time.Now}
}

// Settle retries deletion after restarts without resending a reported result.
func (f *RunFinalizer) Settle(ctx context.Context, limit int) error {
	if f == nil || f.runs == nil || f.jobs == nil || f.deliveries == nil || f.sessions == nil {
		return fmt.Errorf("schedule run finalizer is unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	runs, err := f.runs.ListUnclosedDispatched(ctx, f.lastAt, f.lastID, limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 && !f.lastAt.IsZero() {
		f.lastAt, f.lastID = time.Time{}, ""
		runs, err = f.runs.ListUnclosedDispatched(ctx, f.lastAt, f.lastID, limit)
		if err != nil {
			return err
		}
	}
	if len(runs) > 0 {
		last := runs[len(runs)-1]
		f.lastAt, f.lastID = last.RequestedAt, last.RunID
	}
	var failures error
	for _, run := range runs {
		if err := f.settleRun(ctx, run); err != nil {
			failures = errors.Join(failures, fmt.Errorf("settle schedule run %q: %w", run.RunID, err))
		}
	}
	return failures
}

func (f *RunFinalizer) settleRun(ctx context.Context, run state.ScheduleRunRecord) error {
	if run.ExecutionJobID == "" {
		return nil
	}
	job, found, err := f.jobs.GetJob(ctx, run.ExecutionJobID)
	if err != nil || !found {
		return err
	}
	if job.SessionID != turncmd.ScheduledExecutionSessionID(run.ExecutionJobID) {
		// Older, pre-isolation runs belong to ordinary chat sessions.
		return f.runs.MarkClosed(ctx, run.RunID, f.now().UTC())
	}
	switch job.Status {
	case state.JobStatusCompleted, state.JobStatusFailed, state.JobStatusCanceled, state.JobStatusDeadLettered:
	default:
		return nil
	}
	var snapshot state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &snapshot); err != nil {
		return fmt.Errorf("decode run snapshot: %w", err)
	}
	if snapshot.ReportToEnabled {
		delivery, found, err := f.deliveries.FinalDelivery(ctx, run.ExecutionJobID)
		if err != nil {
			return err
		}
		if !found && job.Status != state.JobStatusCanceled && job.Status != state.JobStatusDeadLettered {
			return nil
		}
		if found && delivery.Status != state.DeliveryStatusSent &&
			(delivery.Status != state.DeliveryStatusFailed || delivery.Error != "permanent") {
			return nil
		}
	}
	if err := f.sessions.CloseRunSession(ctx, job.SessionID, strings.TrimSpace(job.CreatedBy)); err != nil {
		return err
	}
	return f.runs.MarkClosed(ctx, run.RunID, f.now().UTC())
}
