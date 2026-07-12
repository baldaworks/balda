package controlapp

import (
	"context"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type JobLifecycle interface {
	Get(ctx context.Context, jobID string) (baldastate.JobRecord, bool, error)
	CancelJob(ctx context.Context, jobID string, actor string, reason string) error
	CancelBySession(ctx context.Context, sessionID string, actor string, reason string) ([]string, error)
	ListActiveGoalJobsBySession(ctx context.Context, sessionID string) ([]baldastate.JobRecord, error)
}

type JobRuns interface {
	Cancel(jobID string) bool
}

type ScheduledJobs interface {
	Upsert(ctx context.Context, record baldastate.ScheduledJobRecord) error
}
