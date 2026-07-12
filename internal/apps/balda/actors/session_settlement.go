package actors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/appports"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/pkg/actorlayer"
)

// session_settlement.go owns session turn durable settlement policy. The actor
// entrypoint should delegate here instead of carrying lifecycle semantics.
type sessionSettlementCoordinator struct {
	tasks     sessionJobLifecycle
	scheduler appports.ScheduledJobRecorder
}

func newSessionSettlementCoordinator(tasks sessionJobLifecycle, scheduler appports.ScheduledJobRecorder) sessionSettlementCoordinator {
	return sessionSettlementCoordinator{
		tasks:     tasks,
		scheduler: scheduler,
	}
}

func (c sessionSettlementCoordinator) taskAlreadyDone(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload) bool {
	if c.tasks == nil || !sessionTurnUsesJobLifecycle(env, payload) {
		return false
	}
	task, ok, err := c.tasks.Get(ctx, strings.TrimSpace(payload.JobID))
	if err != nil || !ok {
		return false
	}
	return isTerminalJobStatus(task.Status)
}

func (c sessionSettlementCoordinator) settle(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload, runErr error) error {
	if recordErr := c.record(ctx, env, payload, runErr); recordErr != nil {
		return actorlayer.TransientError(recordErr)
	}
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	if runErr == nil {
		return nil
	}
	// Contract: once task terminal failure is durably recorded, settle command without retry.
	if sessionTurnUsesJobLifecycle(env, payload) {
		return nil
	}
	return runErr
}

func (c sessionSettlementCoordinator) record(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload, runErr error) error {
	if c.scheduler != nil && strings.TrimSpace(payload.ScheduledJobID) != "" {
		if runErr == nil {
			if err := c.scheduler.MarkSuccess(ctx, payload.ScheduledJobID); err != nil {
				return fmt.Errorf("mark scheduled job %q success: %w", payload.ScheduledJobID, err)
			}
		} else {
			if err := c.scheduler.RecordExecutionFailure(ctx, payload.ScheduledJobID, runErr); err != nil {
				return fmt.Errorf("record scheduled job %q failure: %w", payload.ScheduledJobID, err)
			}
		}
	}
	if c.tasks == nil || !sessionTurnUsesJobLifecycle(env, payload) {
		return nil
	}
	if errors.Is(runErr, context.Canceled) {
		if err := c.tasks.MarkStatus(ctx, payload.JobID, baldastate.JobStatusCanceled, "session.actor", env.ID, runErr.Error(), map[string]any{
			"namespace": env.Namespace,
			"kind":      env.Kind,
		}); err != nil {
			return fmt.Errorf("mark session job %q canceled: %w", payload.JobID, err)
		}
		return nil
	}
	if runErr == nil {
		if err := c.tasks.MarkStatus(ctx, payload.JobID, baldastate.JobStatusCompleted, "session.actor", env.ID, "", map[string]any{
			"namespace": env.Namespace,
			"kind":      env.Kind,
		}); err != nil {
			return fmt.Errorf("mark session job %q completed: %w", payload.JobID, err)
		}
		return nil
	}
	if err := c.tasks.MarkStatus(ctx, payload.JobID, baldastate.JobStatusFailed, "session.actor", env.ID, runErr.Error(), map[string]any{
		"namespace": env.Namespace,
		"kind":      env.Kind,
	}); err != nil {
		return fmt.Errorf("mark session job %q failed: %w", payload.JobID, err)
	}
	return nil
}

func sessionTurnUsesJobLifecycle(env actorlayer.Envelope, payload SessionTurnPayload) bool {
	if strings.TrimSpace(payload.JobID) == "" {
		return false
	}
	if strings.TrimSpace(payload.ScheduledJobID) != "" {
		return true
	}
	switch {
	case strings.EqualFold(env.Namespace, baldaexecution.NamespaceWebhookInbound):
		return true
	case strings.EqualFold(env.Namespace, baldaexecution.NamespaceScheduleInbound):
		return true
	case strings.EqualFold(payload.Source, sessionTurnSourceWebhook):
		return true
	case strings.EqualFold(payload.Source, sessionTurnSourceSchedule):
		return true
	default:
		return false
	}
}
