package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"go.uber.org/fx"
)

const (
	taskPayloadKindGoal         = "goal"
	taskPayloadKindScheduledJob = "scheduled_job"
)

type taskEnvelopePayload struct {
	Kind         string                   `json:"kind"`
	Goal         *goalTaskPayload         `json:"goal,omitempty"`
	ScheduledJob *scheduledJobTaskPayload `json:"scheduled_job,omitempty"`
}

type goalTaskPayload struct {
	Locator         baldasession.SessionLocator `json:"locator"`
	Objective       string                      `json:"objective"`
	TransportUserID string                      `json:"transport_user_id"`
}

type scheduledJobTaskPayload struct {
	JobID   string                      `json:"job_id"`
	Prompt  string                      `json:"prompt"`
	Locator baldasession.SessionLocator `json:"locator"`
	UserID  string                      `json:"user_id"`
	TopicID int                         `json:"topic_id,omitempty"`
}

func (h *CommandHandler) submitGoalTask(ctx context.Context, locator baldasession.SessionLocator, objective string, transportUserID string) (bool, error) {
	if h.swarmCoordinator == nil || !h.swarmCoordinator.Enabled() {
		return h.goalRunner.Start(ctx, locator, objective, transportUserID)
	}
	taskID := "goal-" + locator.SessionID + "-" + uuid.NewString()
	payload := taskEnvelopePayload{
		Kind: taskPayloadKindGoal,
		Goal: &goalTaskPayload{
			Locator:         locator,
			Objective:       strings.TrimSpace(objective),
			TransportUserID: strings.TrimSpace(transportUserID),
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("encode goal task payload: %w", err)
	}
	_, err = h.swarmCoordinator.Submit(ctx, swarm.Envelope{
		Namespace:   swarm.NamespaceAgentCommand,
		Kind:        swarm.KindGoal,
		From:        swarm.ActorAddress{Target: "telegram", Key: firstNonEmpty(transportUserID, locator.AddressKey, "unknown")},
		To:          swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: taskID},
		SessionID:   locator.SessionID,
		TaskID:      taskID,
		PayloadJSON: string(data),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

type taskActorExecutor struct {
	goalRunner *GoalRunner
	scheduler  *JobScheduler
}

type taskActorExecutorParams struct {
	fx.In

	GoalRunner *GoalRunner
	Scheduler  *JobScheduler `optional:"true"`
}

func newTaskActorExecutor(params taskActorExecutorParams) swarm.Actor {
	return &taskActorExecutor{goalRunner: params.GoalRunner, scheduler: params.Scheduler}
}

func (e *taskActorExecutor) Address() string {
	return swarm.WildcardAddress(swarm.ActorTypeTask)
}

func (e *taskActorExecutor) Handle(ctx context.Context, env swarm.Envelope) error {
	var payload taskEnvelopePayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return swarm.PermanentError(fmt.Errorf("decode task payload: %w", err))
	}
	switch strings.TrimSpace(payload.Kind) {
	case taskPayloadKindGoal:
		if payload.Goal == nil {
			return swarm.PolicyError(fmt.Errorf("goal task payload is required"))
		}
		return e.executeGoal(ctx, *payload.Goal)
	case taskPayloadKindScheduledJob:
		if payload.ScheduledJob == nil {
			return swarm.PolicyError(fmt.Errorf("scheduled job task payload is required"))
		}
		if e.scheduler == nil {
			return swarm.TransientError(fmt.Errorf("job scheduler is required"))
		}
		return e.scheduler.executeScheduledJobTask(ctx, *payload.ScheduledJob)
	default:
		return swarm.PolicyError(fmt.Errorf("unsupported task payload kind %q", payload.Kind))
	}
}

func (e *taskActorExecutor) executeGoal(ctx context.Context, payload goalTaskPayload) error {
	started, err := e.goalRunner.Start(ctx, payload.Locator, payload.Objective, payload.TransportUserID)
	if err != nil {
		return swarm.TransientError(err)
	}
	if !started {
		return e.goalRunner.channel.SendAgentReply(ctx, payload.Locator, "A goal run is already active for this session.")
	}
	return nil
}
