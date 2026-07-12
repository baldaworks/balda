package jobexec

import (
	"context"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type JobLifecycle interface {
	Create(ctx context.Context, record baldastate.JobRecord, actor string, payload any) (bool, error)
	Get(ctx context.Context, jobID string) (baldastate.JobRecord, bool, error)
	MarkStatus(ctx context.Context, jobID string, status string, actor string, messageID string, reason string, payload any) error
}
