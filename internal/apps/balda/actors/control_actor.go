package actors

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/controlapp"
	"github.com/normahq/balda/internal/apps/balda/controlcmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	jobControlActionCancel       = controlcmd.ActionCancel
	jobControlActionCancelTurn   = controlcmd.ActionCancelTurn
	jobControlActionClearGoal    = controlcmd.ActionClearGoal
	jobControlActionScheduleWait = controlcmd.ActionScheduleWait
	scheduledJobOneShotSpec      = "@once"
)

type jobControlPayload = controlcmd.Payload

type jobControlService interface {
	CancelJob(ctx context.Context, payload controlcmd.Payload) error
	CancelSession(ctx context.Context, payload controlcmd.Payload) error
	CancelSessionTurn(ctx context.Context, payload controlcmd.Payload) error
	ClearGoal(ctx context.Context, payload controlcmd.Payload) error
	ScheduleWait(ctx context.Context, payload controlcmd.Payload) error
}

type jobControlActor struct {
	turnDispatcher appports.TurnQueue
	dispatcher     actortransport.Dispatcher
	jobs           controlapp.JobLifecycle
	scheduledJobs  controlapp.ScheduledJobs
	jobRuns        controlapp.JobRuns
	logger         zerolog.Logger
	service        jobControlService
}

type jobControlActorParams struct {
	fx.In

	TurnDispatcher *TurnDispatcher
	Dispatcher     actortransport.Dispatcher
	JobLifecycle   controlapp.JobLifecycle
	ScheduledJobs  controlapp.ScheduledJobs `optional:"true"`
	JobRuns        *JobRunRegistry
	Service        jobControlService
	Logger         zerolog.Logger
}

func (a *jobControlActor) Address() string {
	return "system:control"
}

func (a *jobControlActor) Handle(ctx context.Context, env actorlayer.Envelope) error {
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

func (a *jobControlActor) cancelJob(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.CancelJob(ctx, payload)
}

func (a *jobControlActor) cancelSession(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.CancelSession(ctx, payload)
}

func (a *jobControlActor) cancelSessionTurn(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.CancelSessionTurn(ctx, payload)
}

func (a *jobControlActor) clearGoal(ctx context.Context, payload jobControlPayload) error {
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is required"))
	}
	return a.service.ClearGoal(ctx, payload)
}

func (a *jobControlActor) scheduleWait(ctx context.Context, payload jobControlPayload) error {
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
