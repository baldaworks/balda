package actors

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

const (
	jobControlActionCancel       = controlcmd.ActionCancel
	jobControlActionCancelTurn   = controlcmd.ActionCancelTurn
	jobControlActionClearGoal    = controlcmd.ActionClearGoal
	jobControlActionScheduleWait = controlcmd.ActionScheduleWait
	scheduledJobOneShotSpec      = "@once"
)

type jobControlPayload = controlcmd.Payload

type JobControlService interface {
	CancelJob(ctx context.Context, payload controlcmd.Payload) error
	CancelSession(ctx context.Context, payload controlcmd.Payload) error
	CancelSessionTurn(ctx context.Context, payload controlcmd.Payload) error
	ClearGoal(ctx context.Context, payload controlcmd.Payload) error
	ScheduleWait(ctx context.Context, payload controlcmd.Payload) error
}

type JobControlActor struct {
	service JobControlService
}

func NewJobControlActor(service JobControlService) *JobControlActor {
	return &JobControlActor{
		service: service,
	}
}

func (a *JobControlActor) Address() string {
	return "system:control"
}

func (a *JobControlActor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	if strings.TrimSpace(env.Namespace) != baldaexecution.NamespaceJobControl {
		return actorlayer.PolicyError(fmt.Errorf("unsupported control namespace %q", env.Namespace))
	}
	var payload jobControlPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode control payload: %w", err))
	}
	switch strings.TrimSpace(payload.Action) {
	case jobControlActionCancel:
		if strings.TrimSpace(payload.JobID) != "" {
			return a.cancelJob(ctx, payload)
		}
		return a.cancelSession(ctx, payload)
	case jobControlActionCancelTurn:
		return a.cancelSessionTurn(ctx, payload)
	case jobControlActionClearGoal:
		return a.clearGoal(ctx, payload)
	case jobControlActionScheduleWait:
		return a.scheduleWait(ctx, payload)
	default:
		return actorlayer.PolicyError(fmt.Errorf("unsupported control action %q", payload.Action))
	}
}

func (a *JobControlActor) cancelJob(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.CancelJob(ctx, payload)
}

func (a *JobControlActor) cancelSession(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.CancelSession(ctx, payload)
}

func (a *JobControlActor) cancelSessionTurn(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.CancelSessionTurn(ctx, payload)
}

func (a *JobControlActor) clearGoal(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.ClearGoal(ctx, payload)
}

func (a *JobControlActor) scheduleWait(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.ScheduleWait(ctx, payload)
}

func ControlCancelEnvelope(locator baldasession.SessionLocator, jobID string, requestedBy string, reason string) (actorlayer.Envelope, error) {
	return ControlCancelEnvelopeWithNotify(locator, jobID, requestedBy, reason, true)
}

func ControlCancelEnvelopeWithNotify(locator baldasession.SessionLocator, jobID string, requestedBy string, reason string, notify bool) (actorlayer.Envelope, error) {
	return controlcmd.CancelEnvelopeWithNotify(locator, jobID, requestedBy, reason, notify)
}

func ControlCancelTurnEnvelopeWithNotify(locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) (actorlayer.Envelope, error) {
	return controlcmd.CancelTurnEnvelopeWithNotify(locator, requestedBy, reason, notify)
}

func ControlClearGoalEnvelopeWithNotify(locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) (actorlayer.Envelope, error) {
	return controlcmd.ClearGoalEnvelopeWithNotify(locator, requestedBy, reason, notify)
}

func ControlScheduleWaitEnvelope(locator baldasession.SessionLocator, jobID string, content string, delaySeconds int, requestedBy string, notify bool) (actorlayer.Envelope, error) {
	return controlcmd.ScheduleWaitEnvelope(locator, jobID, content, delaySeconds, requestedBy, notify)
}
