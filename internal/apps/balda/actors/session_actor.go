package actors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

const (
	sessionTurnSourceTelegram = turncmd.SourceTelegram
	sessionTurnSourceWebhook  = turncmd.SourceWebhook
	sessionTurnSourceSchedule = turncmd.SourceSchedule
)

type SessionTurnPayload = turncmd.SessionTurnPayload
type SessionTurnRunner = appports.SessionTurnRunner
type ScheduledJobRecorder = appports.ScheduledJobRecorder

type SessionJobLifecycle interface {
	Get(ctx context.Context, jobID string) (baldastate.JobRecord, bool, error)
	MarkStatus(ctx context.Context, jobID string, status string, actor string, messageID string, reason string, payload any) error
}

type sessionJobLifecycle = SessionJobLifecycle

func SessionTurnEnvelope(payload SessionTurnPayload) (actorlayer.Envelope, error) {
	return turncmd.SessionTurnEnvelope(payload)
}

type SessionRuntimeStateUpdater interface {
	UpdateRuntimeState(ctx context.Context, locator baldasession.SessionLocator, state map[string]any) error
}

type SessionActorConfig struct {
	Turns      appports.TurnQueue
	Runner     appports.SessionTurnRunner
	Tasks      SessionJobLifecycle
	Scheduler  appports.ScheduledJobRecorder
	Dispatcher actortransport.Dispatcher
	Questions  *questions.Service
	Sessions   SessionRuntimeStateUpdater
}

type SessionActorExecutor struct {
	turns      appports.TurnQueue
	runner     appports.SessionTurnRunner
	tasks      SessionJobLifecycle
	scheduler  appports.ScheduledJobRecorder
	dispatcher actortransport.Dispatcher
	questions  *questions.Service
	sessions   SessionRuntimeStateUpdater
}

type sessionActorExecutor = SessionActorExecutor

func NewSessionActor(cfg SessionActorConfig) *SessionActorExecutor {
	return &SessionActorExecutor{
		turns:      cfg.Turns,
		runner:     cfg.Runner,
		tasks:      cfg.Tasks,
		scheduler:  cfg.Scheduler,
		dispatcher: cfg.Dispatcher,
		questions:  cfg.Questions,
		sessions:   cfg.Sessions,
	}
}

func (e *SessionActorExecutor) Address() string {
	return actorlayer.WildcardAddress(baldaexecution.ActorTypeSession)
}

func (e *SessionActorExecutor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	switch strings.TrimSpace(env.Namespace) {
	case baldaexecution.NamespaceHumanInbound, baldaexecution.NamespaceWebhookInbound, baldaexecution.NamespaceScheduleInbound, baldaexecution.NamespaceGoalkeeperCommand, baldaexecution.NamespaceJobControl:
		return e.enqueueTurn(ctx, env)
	case baldaexecution.NamespaceAutoModeCommand:
		return e.updateAutoModeState(ctx, env)
	default:
		return actorlayer.PolicyError(fmt.Errorf("unsupported session namespace %q", env.Namespace))
	}
}

func (e *SessionActorExecutor) updateAutoModeState(ctx context.Context, env actorlayer.Envelope) error {
	if e == nil || e.sessions == nil {
		return actorlayer.TransientError(fmt.Errorf("session runtime state updater is required"))
	}
	var payload automodecmd.Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode auto mode payload: %w", err))
	}
	if err := e.sessions.UpdateRuntimeState(ctx, payload.Locator, payload.State); err != nil {
		return actorlayer.TransientError(fmt.Errorf("update auto mode runtime state: %w", err))
	}
	return nil
}

func (e *SessionActorExecutor) enqueueTurn(ctx context.Context, env actorlayer.Envelope) error {
	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode session turn payload: %w", err))
	}
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		payload.Locator.SessionID = strings.TrimSpace(env.To.Key)
	}
	if strings.TrimSpace(payload.DedupeKey) == "" {
		if strings.TrimSpace(env.DedupeKey) != "" {
			payload.DedupeKey = strings.TrimSpace(env.DedupeKey)
		} else if strings.TrimSpace(env.ID) != "" {
			payload.DedupeKey = strings.TrimSpace(env.ID)
		}
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
	if handled, err := e.handleScheduledQuestionTimeout(ctx, env, payload); handled {
		return settlement.settle(ctx, env, payload, err)
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

	payloadCopy := payload
	result, _, err := e.turns.Enqueue(ctx, appports.TurnTask{
		SessionID:   payload.Locator.SessionID,
		SessionTurn: &payloadCopy,
		Run: func(runCtx context.Context) error {
			return e.runner.RunSessionTurnPayload(runCtx, payloadCopy)
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

func (e *SessionActorExecutor) handleScheduledQuestionTimeout(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload) (bool, error) {
	if e == nil || e.questions == nil || e.dispatcher == nil {
		return false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Source), sessionTurnSourceSchedule) && !strings.EqualFold(strings.TrimSpace(env.Namespace), baldaexecution.NamespaceScheduleInbound) {
		return false, nil
	}
	questionID, ok := questioncmd.ParseTimeoutScheduledContent(payload.Text)
	if !ok {
		return false, nil
	}
	record, settled, err := e.questions.Timeout(ctx, questionID, time.Now().UTC())
	if err != nil || !settled {
		return true, err
	}
	var interaction questioncmd.InteractionContext
	if err := json.Unmarshal([]byte(record.InteractionJSON), &interaction); err != nil {
		return true, fmt.Errorf("decode timed out question interaction: %w", err)
	}
	var resume questioncmd.ResumeTarget
	if err := json.Unmarshal([]byte(record.ResumeJSON), &resume); err != nil {
		return true, fmt.Errorf("decode timed out question resume: %w", err)
	}
	timeoutEnv, err := questioncmd.TimedOutEnvelope(resume, interaction, record.QuestionID, record.AnsweredAt)
	if err != nil {
		return true, err
	}
	_, err = e.dispatcher.Dispatch(ctx, timeoutEnv)
	return true, err
}
