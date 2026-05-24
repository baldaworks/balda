package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type taskAgentRuntimeBuilder interface {
	BuildTaskAgentRuntime(ctx context.Context, cfg baldaagent.TaskAgentRuntimeConfig) (*baldaagent.TaskAgentRuntime, error)
}

type taskAgentActor struct {
	sessions       *baldasession.Manager
	runtimeBuilder taskAgentRuntimeBuilder
	coordinator    *swarm.Coordinator
	taskRuns       *taskRunRegistry
	logger         zerolog.Logger
}

type taskAgentActorParams struct {
	fx.In

	SessionManager *baldasession.Manager
	RuntimeManager *baldaagent.RuntimeManager
	Coordinator    *swarm.Coordinator
	TaskRuns       *taskRunRegistry
	Logger         zerolog.Logger
}

func newTaskAgentActor(params taskAgentActorParams) swarm.Actor {
	return &taskAgentActor{
		sessions:       params.SessionManager,
		runtimeBuilder: params.RuntimeManager,
		coordinator:    params.Coordinator,
		taskRuns:       params.TaskRuns,
		logger:         params.Logger.With().Str("component", "balda.task_agent_actor").Logger(),
	}
}

func (a *taskAgentActor) Address() string {
	return swarm.WildcardAddress(swarm.ActorTypeAgent)
}

func (a *taskAgentActor) Handle(ctx context.Context, env swarm.Envelope) error {
	if strings.TrimSpace(env.Namespace) != swarm.NamespaceAgentCommand {
		return swarm.PolicyError(fmt.Errorf("unsupported agent namespace %q", env.Namespace))
	}
	var payload taskAgentCommandPayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return swarm.PermanentError(fmt.Errorf("decode task agent command: %w", err))
	}
	payload.TaskID = firstNonEmpty(payload.TaskID, env.TaskID)
	payload.Role = normalizeTaskAgentRole(payload.Role)
	if payload.TaskID == "" {
		return swarm.PolicyError(fmt.Errorf("task id is required"))
	}
	if payload.Role == "" {
		return swarm.PolicyError(fmt.Errorf("task agent role is required"))
	}
	if payload.Iteration <= 0 {
		payload.Iteration = 1
	}
	if strings.TrimSpace(payload.Objective) == "" {
		return swarm.PolicyError(fmt.Errorf("task objective is required"))
	}

	ts, err := a.resolveSession(ctx, payload)
	if err != nil {
		return swarm.TransientError(err)
	}
	if a.runtimeBuilder == nil {
		return swarm.TransientError(fmt.Errorf("task agent runtime builder is required"))
	}
	runtime, err := a.runtimeBuilder.BuildTaskAgentRuntime(ctx, baldaagent.TaskAgentRuntimeConfig{
		SessionID:    ts.GetSessionID(),
		BranchName:   ts.GetBranchName(),
		WorkspaceDir: ts.GetWorkspaceDir(),
		Role:         payload.Role,
	})
	if err != nil {
		return swarm.TransientError(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			a.logger.Warn().Err(err).Str("task_id", payload.TaskID).Str("role", payload.Role).Msg("failed to close task agent runtime")
		}
	}()

	runCtx, cancel := context.WithCancel(ctx)
	a.taskRuns.register(payload.TaskID, cancel)
	defer a.taskRuns.unregister(payload.TaskID)
	defer cancel()

	prompt := formatTaskAgentPrompt(payload)
	text, err := runAgentTurn(runCtx, runtime.Runner, ts.GetUserID(), ts.GetAgentSessionID(), prompt)
	if err != nil {
		if errors.Is(runCtx.Err(), context.Canceled) {
			return a.publishResult(ctx, env, payload, "", fmt.Errorf("goal run canceled"))
		}
		return a.publishResult(ctx, env, payload, text, err)
	}
	return a.publishResult(ctx, env, payload, text, nil)
}

func (a *taskAgentActor) resolveSession(ctx context.Context, payload taskAgentCommandPayload) (*baldasession.TopicSession, error) {
	if a.sessions == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	ts, err := a.sessions.GetSession(payload.Locator)
	if err == nil {
		return ts, nil
	}
	userID := strings.TrimSpace(payload.TransportUserID)
	if userID == "" {
		return nil, fmt.Errorf("transport user id is required")
	}
	return a.sessions.RestoreSession(ctx, baldasession.SessionContext{
		Locator: payload.Locator,
		UserID:  userID,
	})
}

func (a *taskAgentActor) publishResult(
	ctx context.Context,
	cause swarm.Envelope,
	command taskAgentCommandPayload,
	text string,
	runErr error,
) error {
	if a.coordinator == nil || !a.coordinator.RuntimeEnabled() {
		return swarm.TransientError(fmt.Errorf("swarm coordinator is required"))
	}
	result := taskAgentResultPayload{
		TaskID:           command.TaskID,
		Role:             command.Role,
		Iteration:        command.Iteration,
		Locator:          command.Locator,
		Objective:        command.Objective,
		Plan:             command.Plan,
		TransportUserID:  command.TransportUserID,
		ExecutorOutput:   command.ExecutorOutput,
		ReviewerFeedback: command.ReviewerFeedback,
		Text:             strings.TrimSpace(text),
		MaxIterations:    command.MaxIterations,
	}
	if runErr != nil {
		result.Error = strings.TrimSpace(runErr.Error())
	}
	payload := taskEnvelopePayload{
		Kind:        taskPayloadKindAgentResult,
		AgentResult: &result,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return swarm.PermanentError(fmt.Errorf("encode task agent result: %w", err))
	}
	env := swarm.Envelope{
		ID:            uuid.NewString(),
		Namespace:     swarm.NamespaceAgentResult,
		Kind:          swarm.KindGoal,
		From:          swarm.ActorAddress{Target: swarm.ActorTypeAgent, Key: command.Role},
		To:            swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: command.TaskID},
		SessionID:     command.Locator.SessionID,
		TaskID:        command.TaskID,
		CorrelationID: firstNonEmpty(cause.CorrelationID, command.TaskID),
		CausationID:   cause.ID,
		Priority:      75,
		DedupeKey:     command.TaskID + ":result:" + command.Role + ":" + strconv.Itoa(command.Iteration),
		PayloadJSON:   string(data),
	}
	if _, err := a.coordinator.Submit(ctx, env); err != nil {
		return swarm.TransientError(err)
	}
	return nil
}

func formatTaskAgentPrompt(payload taskAgentCommandPayload) string {
	switch normalizeTaskAgentRole(payload.Role) {
	case taskAgentRoleReviewer:
		return formatTaskReviewerPrompt(payload)
	default:
		return formatTaskExecutorPrompt(payload)
	}
}

func formatTaskExecutorPrompt(payload taskAgentCommandPayload) string {
	var out strings.Builder
	out.WriteString("Task objective:\n")
	out.WriteString(strings.TrimSpace(payload.Objective))
	out.WriteString("\n\nIteration: ")
	out.WriteString(strconv.Itoa(payload.Iteration))
	out.WriteString("/")
	out.WriteString(strconv.Itoa(normalizeGoalMaxIterations(payload.MaxIterations)))
	if plan := strings.TrimSpace(payload.Plan); plan != "" {
		out.WriteString("\n\nCurrent plan:\n")
		out.WriteString(plan)
	}
	if feedback := strings.TrimSpace(payload.ReviewerFeedback); feedback != "" {
		out.WriteString("\n\nReviewer feedback from previous iteration:\n")
		out.WriteString(feedback)
	}
	out.WriteString("\n\nDo the work now. Return a concise summary with changed files and verification evidence.")
	return out.String()
}

func formatTaskReviewerPrompt(payload taskAgentCommandPayload) string {
	var out strings.Builder
	out.WriteString("Task objective:\n")
	out.WriteString(strings.TrimSpace(payload.Objective))
	out.WriteString("\n\nIteration: ")
	out.WriteString(strconv.Itoa(payload.Iteration))
	out.WriteString("/")
	out.WriteString(strconv.Itoa(normalizeGoalMaxIterations(payload.MaxIterations)))
	if plan := strings.TrimSpace(payload.Plan); plan != "" {
		out.WriteString("\n\nCurrent plan:\n")
		out.WriteString(plan)
	}
	out.WriteString("\n\nExecutor result:\n")
	if executorOutput := strings.TrimSpace(payload.ExecutorOutput); executorOutput != "" {
		out.WriteString(executorOutput)
	} else {
		out.WriteString("(none)")
	}
	out.WriteString("\n\nValidate the result. Start with exactly `verdict: pass` or `verdict: fail`, then provide evidence.")
	return out.String()
}
