package appports

import (
	"context"
	"errors"
	"time"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
)

// ErrTurnQueueFull is the bounded admission classification shared by the
// dispatcher and command-neutral queued use cases.
var ErrTurnQueueFull = errors.New("turn queue is full")

type TurnTask struct {
	SessionID   string
	Run         func(context.Context) error
	SessionTurn *turncmd.SessionTurnPayload
}

type TurnQueue interface {
	Enqueue(ctx context.Context, task TurnTask) (<-chan error, int, error)
	CancelSession(locator baldasession.SessionLocator, clearQueued bool) (bool, int, error)
}

// TurnStatusRecorder records lifecycle evidence for conversation turns.
type TurnStatusRecorder interface {
	Begin(ctx context.Context, sessionID, archiveID string, startedAt time.Time) (context.Context, error)
	Observe(ctx context.Context, kind turnstatuscmd.ActivityKind, occurredAt time.Time) error
	Complete(ctx context.Context, outcome turnstatuscmd.Outcome, completedAt time.Time) error
}

type CurrentTurn struct {
	SessionID       string
	RequesterUserID string
	Text            string
	StartedAt       time.Time
	PendingTurns    int
}

type CurrentTurnResolver interface {
	CurrentTurn(sessionID string) (CurrentTurn, bool)
}

type SessionTurnRunner interface {
	RunSessionTurnPayload(ctx context.Context, payload turncmd.SessionTurnPayload) error
}

type ScheduledJobRecorder interface {
	RecordExecution(ctx context.Context, jobID, dispatchKey string, cause error) error
}

type SessionWorkCanceller interface {
	CancelWork(ctx context.Context, locator baldasession.SessionLocator, actor string, reason string) error
}
