package actors

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
)

const preparationFailureMessage = "Balda could not start this turn. Please retry your message. If it fails again, ask the operator to check the service."

func (e *SessionActorExecutor) reportPreparationFailure(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload, runErr error) error {
	var preparation *turncmd.PreparationError
	if !payload.Deliver || errors.Is(runErr, context.Canceled) || !errors.As(runErr, &preparation) {
		return nil
	}
	terminal := sessionTurnUsesJobLifecycle(env, payload) || !actorlayer.IsRetryableError(runErr) || actorlayer.RetryExhausted(env.Attempt+1, env.MaxAttempts)
	if !terminal {
		return nil
	}
	if e.dispatcher == nil {
		return fmt.Errorf("preparation failure delivery dispatcher is unavailable")
	}
	locator := payload.Locator
	if payload.ReportTo != nil {
		locator = *payload.ReportTo
	}
	delivery, err := deliverycmd.AgentReplyEnvelopeWithSettlement(payload.JobID, env.To, locator, deliverycmd.SettlementOutbox, preparationFailureMessage, "preparation-failure")
	if err != nil {
		return fmt.Errorf("build preparation failure delivery: %w", err)
	}
	// Ordinary chat turns may have no JobID. Preserve one delivery identity
	// across redelivery/restart without manufacturing a job or exposing input.
	key := fmt.Sprintf("turn-preparation-failure:%x", sha256.Sum256([]byte(env.ID)))
	delivery.ID = key
	delivery.DedupeKey = key
	if _, err := e.dispatcher.Dispatch(ctx, delivery); err != nil {
		return fmt.Errorf("dispatch preparation failure delivery: %w", err)
	}
	return nil
}
