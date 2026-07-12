package controlapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/appports"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
)

// SessionWorkCanceller synchronously stops queued, running, and job-backed
// work for a session without going through the async control actor path.
type SessionWorkCanceller struct {
	turnDispatcher appports.TurnQueue
	jobs           JobLifecycle
	jobRuns        JobRuns
	logger         zerolog.Logger
}

func NewSessionWorkCanceller(turns appports.TurnQueue, jobs JobLifecycle, jobRuns JobRuns, logger zerolog.Logger) *SessionWorkCanceller {
	return &SessionWorkCanceller{
		turnDispatcher: turns,
		jobs:           jobs,
		jobRuns:        jobRuns,
		logger:         logger.With().Str("component", "balda.session_work_canceller").Logger(),
	}
}

func (c *SessionWorkCanceller) CancelWork(ctx context.Context, locator baldasession.SessionLocator, actor string, reason string) error {
	if c == nil {
		return nil
	}
	sessionID := strings.TrimSpace(locator.SessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if c.turnDispatcher != nil {
		if _, _, err := c.turnDispatcher.CancelSession(locator, true); err != nil {
			return fmt.Errorf("cancel session turn queue: %w", err)
		}
	}
	if c.jobs == nil {
		return nil
	}
	jobIDs, err := c.jobs.CancelBySession(ctx, sessionID, actor, reason)
	if err != nil {
		return fmt.Errorf("cancel session jobs: %w", err)
	}
	if c.jobRuns == nil {
		return nil
	}
	for _, jobID := range jobIDs {
		c.jobRuns.Cancel(jobID)
	}
	return nil
}
