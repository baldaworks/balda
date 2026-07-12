package actors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"go.uber.org/fx"
)

const (
	sessionTurnSourceTelegram = turncmd.SourceTelegram
	sessionTurnSourceWebhook  = turncmd.SourceWebhook
	sessionTurnSourceSchedule = turncmd.SourceSchedule
)

type SessionTurnPayload = turncmd.SessionTurnPayload
type SessionTurnRunner = appports.SessionTurnRunner
type ScheduledJobRecorder = appports.ScheduledJobRecorder

type sessionJobLifecycle interface {
	Get(ctx context.Context, jobID string) (baldastate.JobRecord, bool, error)
	MarkStatus(ctx context.Context, jobID string, status string, actor string, messageID string, reason string, payload any) error
}

func SessionTurnEnvelope(payload SessionTurnPayload) (actorlayer.Envelope, error) {
	return turncmd.SessionTurnEnvelope(payload)
}

type sessionActorExecutor struct {
	turns     appports.TurnQueue
	runner    appports.SessionTurnRunner
	tasks     sessionJobLifecycle
	scheduler appports.ScheduledJobRecorder
}

type sessionActorExecutorParams struct {
	fx.In

	Turns     appports.TurnQueue
	Runner    appports.SessionTurnRunner
	Tasks     sessionJobLifecycle           `optional:"true"`
	Scheduler appports.ScheduledJobRecorder `optional:"true"`
}

func (e *sessionActorExecutor) Address() string {
	return actorlayer.WildcardAddress(baldaexecution.ActorTypeSession)
}

func (e *sessionActorExecutor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	switch strings.TrimSpace(env.Namespace) {
	case baldaexecution.NamespaceHumanInbound, baldaexecution.NamespaceWebhookInbound, baldaexecution.NamespaceScheduleInbound, baldaexecution.NamespaceGoalkeeperCommand, baldaexecution.NamespaceJobControl:
		return e.enqueueTurn(ctx, env)
	default:
		return actorlayer.PolicyError(fmt.Errorf("unsupported session namespace %q", env.Namespace))
	}
}

func (e *sessionActorExecutor) enqueueTurn(ctx context.Context, env actorlayer.Envelope) error {
	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode session turn payload: %w", err))
	}
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		payload.Locator.SessionID = strings.TrimSpace(env.To.Key)
	}
	envelopeJobID := strings.TrimSpace(baldaexecution.EnvelopeJobID(env))
	payloadJobID := strings.TrimSpace(payload.JobID)
	switch {
	case envelopeJobID != "" && payloadJobID == "":
		return actorlayer.PolicyError(fmt.Errorf("session payload job id is required when envelope job scope is set"))
	case envelopeJobID != "" && payloadJobID != envelopeJobID:
		return actorlayer.PolicyError(fmt.Errorf("session job id mismatch: envelope=%q payload=%q", envelopeJobID, payloadJobID))
	}
	settlement := newSessionSettlementCoordinator(e.tasks, e.scheduler)
	if settlement.taskAlreadyDone(ctx, env, payload) {
		return nil
	}
	if e.turns == nil {
		return actorlayer.TransientError(fmt.Errorf("turn dispatcher is required"))
	}
	if env.Meta != nil && strings.TrimSpace(env.Meta["queue_mode"]) == baldaexecution.QueueModeInterrupt {
		_, _, err := e.turns.CancelSession(payload.Locator, true)
		if err != nil {
			return actorlayer.TransientError(fmt.Errorf("interrupt session turn: %w", err))
		}
	}
	if e.runner == nil {
		return actorlayer.TransientError(fmt.Errorf("session turn runner is required"))
	}

	result, _, err := e.turns.Enqueue(ctx, appports.TurnTask{
		SessionID: payload.Locator.SessionID,
		Run: func(runCtx context.Context) error {
			return e.runner.RunSessionTurnPayload(runCtx, payload)
		},
	})
	if err != nil {
		if errors.Is(err, ErrTurnQueueFull) {
			return actorlayer.TransientError(err)
		}
		return actorlayer.TransientError(fmt.Errorf("enqueue session actor turn: %w", err))
	}

	select {
	case err := <-result:
		return settlement.settle(ctx, env, payload, err)
	case <-ctx.Done():
		return actorlayer.TransientError(ctx.Err())
	}
}

func NormalizeSessionDeliveryOptions(payload SessionTurnPayload) deliveryfmt.Options {
	return turncmd.NormalizeSessionDeliveryOptions(payload)
}
