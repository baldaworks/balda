package handlers

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

func (h *CommandHandler) submitGoalJob(ctx context.Context, locator baldasession.SessionLocator, objective string, transportUserID string) (bool, error) {
	return h.submitGoalJobWithOptions(ctx, locator, deliveryfmt.Options{}, objective, transportUserID)
}

func (h *CommandHandler) submitGoalJobWithOptions(ctx context.Context, locator baldasession.SessionLocator, deliveryOptions deliveryfmt.Options, objective string, transportUserID string) (bool, error) {
	if h.goalJobs != nil {
		active, err := h.goalJobs.HasActiveGoalJob(ctx, locator.SessionID)
		if err != nil {
			return false, fmt.Errorf("list active goal jobs: %w", err)
		}
		if active {
			return false, nil
		}
	}
	maxIterations := normalizeGoalMaxIterations(h.goalMaxIterations)
	env, err := goalkeepercmd.JobEnvelopeWithOptions(locator, deliveryfmt.NormalizeOptions(deliveryOptions), objective, transportUserID, maxIterations)
	if err != nil {
		return false, err
	}
	if h.actorDispatcher == nil {
		return false, fmt.Errorf("runtime is unavailable")
	}
	_, err = h.actorDispatcher.Dispatch(ctx, env)
	if err != nil {
		return false, err
	}
	return true, nil
}
