package appports

import (
	"context"

	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
)

type TurnTask struct {
	SessionID   string
	Run         func(context.Context) error
	SessionTurn *turncmd.SessionTurnPayload
}

type TurnQueue interface {
	Enqueue(ctx context.Context, task TurnTask) (<-chan error, int, error)
	CancelSession(locator baldasession.SessionLocator, clearQueued bool) (bool, int, error)
}

type SessionTurnRunner interface {
	RunSessionTurnPayload(ctx context.Context, payload turncmd.SessionTurnPayload) error
}

type ScheduledJobRecorder interface {
	MarkSuccess(ctx context.Context, jobID string) error
	RecordExecutionFailure(ctx context.Context, jobID string, cause error) error
}

type SessionWorkCanceller interface {
	CancelWork(ctx context.Context, locator baldasession.SessionLocator, actor string, reason string) error
}
