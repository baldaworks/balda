package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/actors"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

func (h *BaldaHandler) submitSessionTurn(ctx context.Context, payload actors.SessionTurnPayload) (*swarm.CommandPublishResult, error) {
	return h.submitSessionTurnToSwarm(ctx, payload)
}

func (h *BaldaHandler) submitSessionTurnToSwarm(ctx context.Context, payload actors.SessionTurnPayload) (*swarm.CommandPublishResult, error) {
	if h.swarmCoordinator == nil || !h.swarmCoordinator.RuntimeEnabled() {
		return nil, fmt.Errorf("jetstream swarm runtime is unavailable")
	}
	env, err := actors.SessionTurnEnvelope(payload)
	if err != nil {
		return nil, err
	}
	return h.swarmCoordinator.Submit(ctx, env)
}

func (h *BaldaHandler) submitWebhookTask(ctx context.Context, payload actors.SessionTurnPayload, routeName string, requestID string) (*swarm.CommandPublishResult, string, error) {
	if h.swarmCoordinator == nil || !h.swarmCoordinator.RuntimeEnabled() {
		return nil, "", fmt.Errorf("jetstream swarm runtime is unavailable")
	}
	env, taskID, err := actors.WebhookTaskEnvelope(payload, routeName, requestID)
	if err != nil {
		return nil, "", err
	}
	result, err := h.swarmCoordinator.Submit(ctx, env)
	if err != nil {
		return nil, "", err
	}
	return result, taskID, nil
}

func (h *BaldaHandler) RunSessionTurnPayload(ctx context.Context, payload actors.SessionTurnPayload) error {
	ts, err := h.sessionManager.GetSession(payload.Locator)
	if err != nil {
		userID := strings.TrimSpace(payload.UserID)
		if userID == "" {
			h.logger.Debug().
				Str("session_id", payload.Locator.SessionID).
				Str("address_key", payload.Locator.AddressKey).
				Msg("dropping queued turn for inactive session without restore user")
			return nil
		}
		ts, err = h.sessionManager.RestoreSession(ctx, baldasession.SessionContext{
			Locator: payload.Locator,
			UserID:  userID,
		})
		if err != nil {
			if !errors.Is(err, baldasession.ErrNoPersistedSession) {
				return fmt.Errorf("restore session for queued turn: %w", err)
			}
			ts, err = h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{
				Locator: payload.Locator,
				UserID:  userID,
			}, ownerSessionLabel)
			if err != nil {
				return fmt.Errorf("create session for queued turn: %w", err)
			}
		}
	}
	userID := strings.TrimSpace(payload.UserID)
	if userID == "" {
		userID = ts.GetUserID()
	}
	agentSessionID := strings.TrimSpace(payload.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = ts.GetAgentSessionID()
	}
	deliveryLocator := payload.Locator
	if payload.ReportTo != nil {
		deliveryLocator = *payload.ReportTo
	}
	return h.runTurnTaskWithDelivery(
		ctx,
		payload.Text,
		ts.GetRunner(),
		userID,
		ts.GetSessionID(),
		agentSessionID,
		deliveryLocator,
		payload.MessageID,
		payload.TopicID,
		payload.ProgressPolicy,
		payload.Deliver,
	)
}

func (h *CommandHandler) submitGoalTask(ctx context.Context, locator baldasession.SessionLocator, objective string, transportUserID string) (bool, error) {
	maxIterations := normalizeGoalMaxIterations(h.goalMaxIterations)
	env, err := actors.GoalTaskEnvelope(locator, objective, transportUserID, maxIterations)
	if err != nil {
		return false, err
	}
	if h.swarmCoordinator == nil || !h.swarmCoordinator.Enabled() {
		return false, fmt.Errorf("jetstream swarm runtime is unavailable")
	}
	_, err = h.swarmCoordinator.Submit(ctx, env)
	if err != nil {
		return false, err
	}
	return true, nil
}
