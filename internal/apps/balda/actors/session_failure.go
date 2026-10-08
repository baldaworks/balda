package actors

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
)

const preparationFailureMessage = "Balda could not start this turn. Please retry your message. If it fails again, ask the operator to check the service."
const scheduledFailureMessage = "The scheduled run could not complete. Ask the operator to check Balda."
const webhookFailureMessage = "The webhook run could not complete. Ask the operator to check Balda."

func (e *SessionActorExecutor) reportPreparationFailure(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload, runErr error) error {
	if payload.Source == turncmd.SourceWebhook {
		return e.reportWebhookFailure(ctx, env, payload, runErr)
	}
	var preparation *turncmd.PreparationError
	if !payload.Deliver || runErr == nil || errors.Is(runErr, context.Canceled) ||
		errors.Is(runErr, turncmd.ErrScheduledReportQueued) {
		return nil
	}
	recurring := payload.Source == turncmd.SourceSchedule && payload.ScheduleOneShot != nil && !*payload.ScheduleOneShot
	if !recurring && !errors.As(runErr, &preparation) {
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
	message, suffix := preparationFailureMessage, "preparation-failure"
	if recurring {
		message, suffix = scheduledFailureMessage, "final"
	}
	delivery, err := deliverycmd.AgentReplyEnvelopeWithSettlement(payload.JobID, env.To, locator, deliverycmd.SettlementOutbox, message, suffix)
	if err != nil {
		return fmt.Errorf("build preparation failure delivery: %w", err)
	}
	// Ordinary chat turns may have no JobID. Preserve one delivery identity
	// across redelivery/restart without manufacturing a job or exposing input.
	if !recurring {
		key := fmt.Sprintf("turn-preparation-failure:%x", sha256.Sum256([]byte(env.ID)))
		delivery.ID = key
		delivery.DedupeKey = key
	}
	if _, err := e.dispatcher.Dispatch(ctx, delivery); err != nil {
		return fmt.Errorf("dispatch preparation failure delivery: %w", err)
	}
	return nil
}

func (e *SessionActorExecutor) reportWebhookFailure(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload, runErr error) error {
	if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, turncmd.ErrWebhookReportQueued) {
		return nil
	}
	if e.tasks == nil {
		return fmt.Errorf("webhook job lifecycle is unavailable")
	}
	job, found, err := e.tasks.Get(ctx, payload.JobID)
	if err != nil {
		return fmt.Errorf("load webhook output: %w", err)
	}
	if !found {
		return fmt.Errorf("webhook job is unavailable")
	}
	output := job.Result
	if output == "" {
		output = webhookFailureMessage
		if err := e.tasks.RecordPrivateOutput(ctx, payload.JobID, output, true); err != nil {
			return fmt.Errorf("record webhook failure output: %w", err)
		}
	}
	return e.dispatchWebhookFinal(ctx, env, payload, output)
}

func (e *SessionActorExecutor) replayWebhookOutput(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload,
	settlement sessionSettlementCoordinator) (bool, error) {
	if e.tasks == nil {
		return false, nil
	}
	job, found, err := e.tasks.Get(ctx, payload.JobID)
	if err != nil {
		return true, actorlayer.TransientError(fmt.Errorf("load webhook replay output: %w", err))
	}
	if !found || job.Result == "" {
		return false, nil
	}
	if err := e.dispatchWebhookFinal(ctx, env, payload, job.Result); err != nil {
		return true, actorlayer.TransientError(err)
	}
	var runErr error
	if job.Error != "" {
		runErr = fmt.Errorf("private webhook execution failed")
	}
	return true, settlement.settle(ctx, env, payload, runErr)
}

func (e *SessionActorExecutor) dispatchWebhookFinal(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload, output string) error {
	if !payload.Deliver {
		return nil
	}
	if payload.ReportTo == nil || e.dispatcher == nil {
		return fmt.Errorf("webhook final delivery is unavailable")
	}
	delivery, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement(payload.JobID, env.To,
		*payload.ReportTo, deliveryfmt.DeliveryFormatNone, deliverycmd.SettlementOutbox, output, "final")
	if err != nil {
		return fmt.Errorf("build webhook final delivery: %w", err)
	}
	if _, err := e.dispatcher.Dispatch(ctx, delivery); err != nil {
		return fmt.Errorf("dispatch webhook final delivery: %w", err)
	}
	return nil
}
