package actors

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
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/agent"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	goalkeeperActorName = "goalkeeper.actor"

	goalkeeperMetadataEventKey = "norma.goalkeeper.event"
	goalkeeperMetadataStepKey  = "norma.goalkeeper.step"

	goalkeeperStepStarted   = "step_started"
	goalkeeperStepCompleted = "step_completed"
	goalkeeperStepFailed    = "step_failed"
	goalkeeperWorkerStep    = "worker"
	goalkeeperValidatorStep = "validator"
)

type goalkeeperRuntimeBuilder interface {
	BuildGoalkeeperRuntime(ctx context.Context, cfg baldaagent.GoalkeeperRuntimeConfig) (*baldaagent.GoalkeeperRuntime, error)
}

type goalkeeperActor struct {
	tasks          *swarm.TaskService
	coordinator    *swarm.Coordinator
	sessions       *baldasession.Manager
	runtimeBuilder goalkeeperRuntimeBuilder
	taskRuns       *TaskRunRegistry
	maxIters       int
	logger         zerolog.Logger
}

type goalkeeperActorParams struct {
	fx.In

	TaskService    *swarm.TaskService
	Coordinator    *swarm.Coordinator
	SessionManager *baldasession.Manager
	RuntimeManager *baldaagent.RuntimeManager
	TaskRuns       *TaskRunRegistry
	MaxIters       int `name:"balda_goal_max_iterations"`
	Logger         zerolog.Logger
}

func newGoalkeeperActor(params goalkeeperActorParams) swarm.Actor {
	return &goalkeeperActor{
		tasks:          params.TaskService,
		coordinator:    params.Coordinator,
		sessions:       params.SessionManager,
		runtimeBuilder: params.RuntimeManager,
		taskRuns:       params.TaskRuns,
		maxIters:       normalizeGoalMaxIterations(params.MaxIters),
		logger:         params.Logger.With().Str("component", "balda.goalkeeper_actor").Logger(),
	}
}

func (a *goalkeeperActor) Address() string {
	return swarm.WildcardAddress(swarm.ActorTypeGoalkeeper)
}

func (a *goalkeeperActor) Handle(ctx context.Context, envelope any) error {
	env, err := swarm.AssertEnvelope(envelope)
	if err != nil {
		return err
	}
	if strings.TrimSpace(env.Namespace) != swarm.NamespaceGoalCommand {
		return swarm.PolicyError(fmt.Errorf("unsupported goal namespace %q", env.Namespace))
	}
	var payload taskEnvelopePayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return swarm.PermanentError(fmt.Errorf("decode goalkeeper payload: %w", err))
	}
	if strings.TrimSpace(payload.Kind) != taskPayloadKindGoal || payload.Goal == nil {
		return swarm.PolicyError(fmt.Errorf("goal payload is required"))
	}
	return a.runGoal(ctx, env, *payload.Goal)
}

func GoalkeeperTaskEnvelope(
	locator baldasession.SessionLocator,
	objective string,
	transportUserID string,
	maxIterations int,
) (swarm.Envelope, error) {
	taskID := "goal-" + locator.SessionID + "-" + uuid.NewString()
	payload := taskEnvelopePayload{
		Kind: taskPayloadKindGoal,
		Goal: &goalTaskPayload{
			TaskID:          taskID,
			Locator:         locator,
			Objective:       strings.TrimSpace(objective),
			TransportUserID: strings.TrimSpace(transportUserID),
			MaxIterations:   normalizeGoalMaxIterations(maxIterations),
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return swarm.Envelope{}, fmt.Errorf("encode goal task payload: %w", err)
	}
	return swarm.Envelope{
		ID:          uuid.NewString(),
		Namespace:   swarm.NamespaceGoalCommand,
		Kind:        swarm.KindGoal,
		From:        swarm.ActorAddress{Target: "telegram", Key: firstNonEmpty(transportUserID, locator.AddressKey, "unknown")},
		To:          swarm.ActorAddress{Target: swarm.ActorTypeGoalkeeper, Key: taskID},
		SessionID:   locator.SessionID,
		TaskID:      taskID,
		Priority:    90,
		PayloadJSON: string(data),
	}, nil
}

func (a *goalkeeperActor) runGoal(ctx context.Context, env swarm.Envelope, payload goalTaskPayload) error {
	taskID := firstNonEmpty(payload.TaskID, env.TaskID, env.To.Key)
	objective := strings.TrimSpace(payload.Objective)
	if taskID == "" {
		return swarm.PolicyError(fmt.Errorf("task id is required"))
	}
	if objective == "" {
		return swarm.PolicyError(fmt.Errorf("goal objective is required"))
	}
	if a.taskStatusIs(ctx, taskID, baldastate.SwarmTaskStatusCompleted, baldastate.SwarmTaskStatusFailed, baldastate.SwarmTaskStatusCanceled, baldastate.SwarmTaskStatusDeadLettered) {
		return nil
	}
	maxIterations := normalizeGoalMaxIterations(payload.MaxIterations)
	if maxIterations == defaultGoalMaxIterations && a.maxIters != defaultGoalMaxIterations {
		maxIterations = a.maxIters
	}
	payload.TaskID = taskID
	payload.Objective = objective
	payload.MaxIterations = maxIterations

	if err := a.ensureGoalTask(ctx, payload); err != nil {
		return err
	}
	if err := a.tasks.SetPlan(ctx, taskID, goalkeeperActorName, map[string]any{
		"objective":      objective,
		"max_iterations": maxIterations,
		"workflow":       "norma.goalkeeper",
		"steps":          []string{"Run the worker agent.", "Run the validator agent.", "Repeat until validator passes or max iterations is reached."},
	}); err != nil {
		return swarm.TransientError(err)
	}
	ts, err := a.resolveSession(ctx, payload)
	if err != nil {
		return swarm.TransientError(err)
	}
	if err := a.tasks.MarkStatus(ctx, taskID, baldastate.SwarmTaskStatusRunning, goalkeeperActorName, env.ID, "", map[string]any{
		"objective": objective,
	}); err != nil {
		return swarm.TransientError(err)
	}
	if err := a.deliver(ctx, taskID, payload.Locator, fmt.Sprintf("Goal run started. Max iterations: %d.\n\nGoal: %s", maxIterations, objective), "started"); err != nil {
		return err
	}

	if a.runtimeBuilder == nil {
		return swarm.TransientError(fmt.Errorf("goalkeeper runtime builder is required"))
	}
	runtime, err := a.runtimeBuilder.BuildGoalkeeperRuntime(ctx, baldaagent.GoalkeeperRuntimeConfig{
		SessionID:     ts.GetAgentSessionID(),
		UserID:        ts.GetUserID(),
		BranchName:    ts.GetBranchName(),
		WorkspaceDir:  ts.GetWorkspaceDir(),
		MaxIterations: uint(maxIterations),
	})
	if err != nil {
		return swarm.TransientError(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			a.logger.Warn().Err(err).Str("task_id", taskID).Msg("failed to close goalkeeper runtime")
		}
	}()

	runCtx, cancel := context.WithCancel(ctx)
	runID := a.taskRuns.Register(taskID, cancel)
	defer a.taskRuns.Unregister(taskID, runID)
	defer cancel()

	result, err := a.runWorkflow(runCtx, runtime, ts.GetUserID(), ts.GetAgentSessionID(), payload)
	if err != nil {
		if errors.Is(runCtx.Err(), context.Canceled) {
			if setErr := a.tasks.SetResult(ctx, taskID, result.toTaskResult(false), baldastate.SwarmTaskStatusCanceled, goalkeeperActorName, "goal run canceled"); setErr != nil {
				return swarm.TransientError(setErr)
			}
			return a.deliver(ctx, taskID, payload.Locator, "Goal run canceled.", "canceled")
		}
		reason := redactSecrets(err.Error())
		if setErr := a.tasks.SetResult(ctx, taskID, result.toTaskResult(false), baldastate.SwarmTaskStatusFailed, goalkeeperActorName, reason); setErr != nil {
			return swarm.TransientError(setErr)
		}
		return a.deliver(ctx, taskID, payload.Locator, "Goal run failed: "+reason, "failed")
	}
	passed := reviewerPassed(result.validatorOutput)
	status := baldastate.SwarmTaskStatusCompleted
	reason := ""
	if !passed {
		status = baldastate.SwarmTaskStatusFailed
		reason = "max iterations reached"
	}
	taskResult := result.toTaskResult(passed)
	if err := a.tasks.SetResult(ctx, taskID, taskResult, status, goalkeeperActorName, reason); err != nil {
		return swarm.TransientError(err)
	}
	if passed {
		if err := a.enqueueTaskCompletionMemorySync(ctx, payload, taskResult); err != nil {
			return err
		}
		return a.deliver(ctx, taskID, payload.Locator, a.renderTaskOutcome(ctx, taskID, "Goal run completed."), "completed")
	}
	return a.deliver(ctx, taskID, payload.Locator, a.renderTaskOutcome(ctx, taskID, "Goal run reached max iterations without passing validation."), "max-iterations")
}

func (a *goalkeeperActor) ensureGoalTask(ctx context.Context, payload goalTaskPayload) error {
	if a.tasks == nil {
		return swarm.TransientError(fmt.Errorf("task service is required"))
	}
	record := baldastate.SwarmTaskRecord{
		ID:            strings.TrimSpace(payload.TaskID),
		SessionID:     strings.TrimSpace(payload.Locator.SessionID),
		Title:         goalTaskTitle(payload.Objective),
		Objective:     strings.TrimSpace(payload.Objective),
		Status:        baldastate.SwarmTaskStatusCreated,
		OwnerActor:    swarm.ActorTypeGoalkeeper + ":" + strings.TrimSpace(payload.TaskID),
		AssignedActor: swarm.ActorTypeGoalkeeper + ":" + strings.TrimSpace(payload.TaskID),
		Priority:      90,
		CreatedBy:     strings.TrimSpace(payload.TransportUserID),
		CreatedFrom:   "goal",
	}
	if _, err := a.tasks.Create(ctx, record, goalkeeperActorName, payload); err != nil {
		return swarm.TransientError(err)
	}
	task, ok, err := a.tasks.Get(ctx, payload.TaskID)
	if err != nil {
		return swarm.TransientError(err)
	}
	if !ok {
		return swarm.TransientError(fmt.Errorf("goal task %q was not persisted", payload.TaskID))
	}
	switch strings.TrimSpace(task.Status) {
	case "", baldastate.SwarmTaskStatusCreated, baldastate.SwarmTaskStatusQueued:
		return a.tasks.MarkStatus(ctx, payload.TaskID, baldastate.SwarmTaskStatusQueued, goalkeeperActorName, "", "", nil)
	default:
		return nil
	}
}

func (a *goalkeeperActor) resolveSession(ctx context.Context, payload goalTaskPayload) (*baldasession.TopicSession, error) {
	if a.sessions == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	ts, err := a.sessions.GetSession(payload.Locator)
	if err == nil {
		return ts, nil
	}
	userID := strings.TrimSpace(payload.TransportUserID)
	if userID == "" {
		return nil, fmt.Errorf("restore user id is required")
	}
	sessionCtx := baldasession.SessionContext{Locator: payload.Locator, UserID: userID}
	ts, err = a.sessions.RestoreSession(ctx, sessionCtx)
	if err == nil {
		return ts, nil
	}
	if !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, fmt.Errorf("restore session for goal: %w", err)
	}
	ts, err = a.sessions.EnsureSession(ctx, sessionCtx, ownerSessionLabel)
	if err != nil {
		return nil, fmt.Errorf("create session for goal: %w", err)
	}
	return ts, nil
}

type goalkeeperRunResult struct {
	payload         goalTaskPayload
	iterations      int
	workerOutput    string
	validatorOutput string
	finalText       string
}

func (a *goalkeeperActor) runWorkflow(
	ctx context.Context,
	runtime *baldaagent.GoalkeeperRuntime,
	userID string,
	agentSessionID string,
	payload goalTaskPayload,
) (goalkeeperRunResult, error) {
	result := goalkeeperRunResult{payload: payload}
	if runtime == nil || runtime.Runner == nil {
		return result, fmt.Errorf("goalkeeper runner is required")
	}
	userContent := genai.NewContentFromText("Goal:\n"+strings.TrimSpace(payload.Objective), genai.RoleUser)
	currentStep := ""
	sawTurnComplete := false
	for ev, err := range runtime.Runner.Run(ctx, userID, agentSessionID, userContent, adkagent.RunConfig{}) {
		if err != nil {
			return result, fmt.Errorf("run goalkeeper workflow: %w", err)
		}
		if ev == nil {
			continue
		}
		if step, eventType, ok := goalkeeperStepMetadata(ev); ok {
			switch eventType {
			case goalkeeperStepStarted:
				currentStep = step
				if err := a.recordStepStarted(ctx, payload, step, result.iterations+1); err != nil {
					return result, err
				}
			case goalkeeperStepCompleted:
				if step == goalkeeperValidatorStep {
					result.iterations++
				}
				if err := a.recordStepCompleted(ctx, payload, step, result.iterations); err != nil {
					return result, err
				}
				currentStep = ""
			case goalkeeperStepFailed:
				return result, fmt.Errorf("%s step failed", step)
			}
		}
		text := visibleEventText(ev)
		if text != "" {
			result.finalText = appendVisibleText(result.finalText, text)
			switch currentStep {
			case goalkeeperWorkerStep:
				result.workerOutput = appendVisibleText(result.workerOutput, text)
			case goalkeeperValidatorStep:
				result.validatorOutput = appendVisibleText(result.validatorOutput, text)
			}
		}
		if ev.TurnComplete {
			sawTurnComplete = true
		}
	}
	if result.iterations == 0 {
		result.iterations = 1
	}
	if !sawTurnComplete {
		return result, fmt.Errorf("goalkeeper workflow ended without completion")
	}
	return result, nil
}

func goalkeeperStepMetadata(ev *adksession.Event) (step string, eventType string, ok bool) {
	if ev == nil || len(ev.CustomMetadata) == 0 {
		return "", "", false
	}
	eventType, _ = ev.CustomMetadata[goalkeeperMetadataEventKey].(string)
	step, _ = ev.CustomMetadata[goalkeeperMetadataStepKey].(string)
	if strings.TrimSpace(eventType) == "" || strings.TrimSpace(step) == "" {
		return "", "", false
	}
	return strings.TrimSpace(step), strings.TrimSpace(eventType), true
}

func visibleEventText(ev *adksession.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	var parts []string
	for _, part := range ev.Content.Parts {
		if part != nil && !part.Thought && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func appendVisibleText(existing string, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "\n\n" + next
}

func (a *goalkeeperActor) recordStepStarted(ctx context.Context, payload goalTaskPayload, step string, iteration int) error {
	status := baldastate.SwarmTaskStatusWaitingForAgent
	if step == goalkeeperValidatorStep {
		status = baldastate.SwarmTaskStatusValidating
	}
	if err := a.tasks.MarkStatus(ctx, payload.TaskID, status, goalkeeperActorName, "", "", map[string]any{
		"step":      step,
		"iteration": iteration,
	}); err != nil {
		return swarm.TransientError(err)
	}
	if err := a.tasks.AppendEvent(ctx, payload.TaskID, swarm.TaskEventAgentStarted, goalkeeperActorName, "", map[string]any{
		"step":      step,
		"iteration": iteration,
	}); err != nil {
		return swarm.TransientError(err)
	}
	return a.deliver(ctx, payload.TaskID, payload.Locator, fmt.Sprintf("Goal iteration %d/%d: %s started.", iteration, normalizeGoalMaxIterations(payload.MaxIterations), step), "started:"+step+":"+strconv.Itoa(iteration))
}

func (a *goalkeeperActor) recordStepCompleted(ctx context.Context, payload goalTaskPayload, step string, iteration int) error {
	if iteration <= 0 {
		iteration = 1
	}
	if err := a.tasks.AppendEvent(ctx, payload.TaskID, swarm.TaskEventAgentResult, goalkeeperActorName, "", map[string]any{
		"step":      step,
		"iteration": iteration,
	}); err != nil {
		return swarm.TransientError(err)
	}
	return nil
}

func (r goalkeeperRunResult) toTaskResult(goalReached bool) taskResultPayloadV1 {
	workerOutput := redactSecrets(strings.TrimSpace(r.workerOutput))
	validatorOutput := redactSecrets(strings.TrimSpace(r.validatorOutput))
	finalText := redactSecrets(strings.TrimSpace(r.finalText))
	whatWasDone := firstNonEmpty(workerOutput, finalText, strings.TrimSpace(r.payload.Objective))
	validation := firstNonEmpty(validatorOutput, finalText)
	verified := "validator returned feedback"
	if reviewerPassed(validatorOutput) {
		verified = "validator returned pass"
	}
	nextAction := "Inspect events and decide whether to continue, cancel, or ask a human."
	if goalReached {
		nextAction = "Review workspace changes and run balda.workspace.export when ready."
	} else if r.payload.MaxIterations > 0 && r.iterations >= r.payload.MaxIterations {
		nextAction = "Review failure evidence and rerun /goal or assign a narrower follow-up task."
	}
	return taskResultPayloadV1{
		SchemaVersion:  taskResultSchemaVersionV1,
		GoalReached:    goalReached,
		Iterations:     r.iterations,
		ExecutorOutput: workerOutput,
		ReviewerOutput: validatorOutput,
		ReviewerNotes:  validatorOutput,
		ReviewableOutcome: taskReviewableOutcomeV1{
			SchemaVersion: taskReviewableOutcomeSchemaVersion,
			WhatWasDone:   whatWasDone,
			Validation:    validation,
			Verified:      verified,
			NotVerified:   "manual review still required",
			NextAction:    nextAction,
		},
	}
}

func (a *goalkeeperActor) enqueueTaskCompletionMemorySync(ctx context.Context, payload goalTaskPayload, result taskResultPayloadV1) error {
	if a == nil || a.coordinator == nil || !a.coordinator.RuntimeEnabled() {
		return swarm.TransientError(fmt.Errorf("swarm coordinator is required"))
	}
	commands := []taskMemorySyncPayload{
		{
			Operation: taskMemoryOperationSummary,
			Scope:     taskMemoryScopeCompleted,
			TaskID:    strings.TrimSpace(payload.TaskID),
			SessionID: strings.TrimSpace(payload.Locator.SessionID),
			Content: strings.TrimSpace(strings.Join([]string{
				"Objective: " + strings.TrimSpace(payload.Objective),
				"What was done: " + strings.TrimSpace(result.ReviewableOutcome.WhatWasDone),
				"Validation: " + strings.TrimSpace(firstNonEmpty(result.ReviewableOutcome.Validation, result.ReviewerOutput)),
				"Next action: " + strings.TrimSpace(result.ReviewableOutcome.NextAction),
			}, "\n")),
		},
		{
			Operation: taskMemoryOperationFacts,
			Scope:     taskMemoryScopeCompleted,
			TaskID:    strings.TrimSpace(payload.TaskID),
			SessionID: strings.TrimSpace(payload.Locator.SessionID),
			Content: strings.TrimSpace(strings.Join([]string{
				"fact: " + strings.TrimSpace(result.ReviewableOutcome.WhatWasDone),
				"fact: " + strings.TrimSpace(result.ReviewableOutcome.Verified),
				"fact: " + strings.TrimSpace(result.ReviewableOutcome.NextAction),
			}, "\n")),
		},
		{
			Operation: taskMemoryOperationContext,
			Scope:     taskMemoryScopeCompleted,
			TaskID:    strings.TrimSpace(payload.TaskID),
			SessionID: strings.TrimSpace(payload.Locator.SessionID),
			Content: strings.TrimSpace(strings.Join([]string{
				"task_id=" + strings.TrimSpace(payload.TaskID),
				"session_id=" + strings.TrimSpace(payload.Locator.SessionID),
				"iteration=" + strconv.Itoa(result.Iterations),
				"goal_reached=true",
			}, "\n")),
		},
	}
	for _, command := range commands {
		if strings.TrimSpace(command.Content) == "" {
			continue
		}
		commandJSON, err := json.Marshal(command)
		if err != nil {
			return swarm.PermanentError(fmt.Errorf("encode memory sync command: %w", err))
		}
		dedupeKey := strings.TrimSpace(payload.TaskID) + ":memory:" + command.Operation + ":" + strconv.Itoa(result.Iterations)
		env := swarm.Envelope{
			ID:            dedupeKey,
			Namespace:     swarm.NamespaceMemorySync,
			Kind:          command.Operation,
			From:          swarm.ActorAddress{Target: swarm.ActorTypeGoalkeeper, Key: strings.TrimSpace(payload.TaskID)},
			To:            swarm.ActorAddress{Target: swarm.ActorTypeMemory, Key: taskMemoryActorKeyGlobal},
			SessionID:     strings.TrimSpace(payload.Locator.SessionID),
			TaskID:        strings.TrimSpace(payload.TaskID),
			CorrelationID: strings.TrimSpace(payload.TaskID),
			Priority:      60,
			DedupeKey:     dedupeKey,
			PayloadJSON:   string(commandJSON),
		}
		if _, err := a.coordinator.Dispatch(ctx, env); err != nil {
			return swarm.TransientError(err)
		}
	}
	return nil
}

func (a *goalkeeperActor) taskStatusIs(ctx context.Context, taskID string, statuses ...string) bool {
	if a == nil || a.tasks == nil || strings.TrimSpace(taskID) == "" {
		return false
	}
	task, ok, err := a.tasks.Get(ctx, taskID)
	if err != nil || !ok {
		return false
	}
	for _, status := range statuses {
		if strings.TrimSpace(task.Status) == strings.TrimSpace(status) {
			return true
		}
	}
	return false
}

func (a *goalkeeperActor) renderTaskOutcome(ctx context.Context, taskID string, fallback string) string {
	if a == nil || a.tasks == nil {
		return fallback
	}
	task, ok, err := a.tasks.Get(ctx, taskID)
	if err != nil || !ok {
		return fallback
	}
	return renderReviewableOutcome(task, taskArtifactsFromSessionProvider(ctx, a.sessions, task))
}

func (a *goalkeeperActor) deliver(
	ctx context.Context,
	taskID string,
	locator baldasession.SessionLocator,
	text string,
	dedupeSuffix string,
) error {
	if a.coordinator == nil || !a.coordinator.RuntimeEnabled() {
		return swarm.TransientError(fmt.Errorf("swarm coordinator is required"))
	}
	message := redactSecrets(strings.TrimSpace(text))
	if message == "" {
		return nil
	}
	payload := taskDeliveryPayload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Text:    message,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return swarm.PermanentError(fmt.Errorf("encode task delivery payload: %w", err))
	}
	dedupeKey := strings.TrimSpace(taskID)
	if dedupeKey == "" {
		dedupeKey = "delivery:" + shortTaskHash(strings.Join([]string{
			strings.TrimSpace(locator.SessionID),
			strings.TrimSpace(locator.AddressKey),
			message,
		}, "|"))
	}
	if suffix := strings.TrimSpace(dedupeSuffix); suffix != "" {
		dedupeKey += ":delivery:" + suffix
	}
	env := swarm.Envelope{
		ID:            dedupeKey,
		Namespace:     swarm.NamespaceAgentResult,
		Kind:          taskPayloadKindDelivery,
		From:          swarm.ActorAddress{Target: swarm.ActorTypeGoalkeeper, Key: taskID},
		To:            swarm.ActorAddress{Target: swarm.ActorTypeDelivery, Key: firstNonEmpty(locator.AddressKey, locator.SessionID, "telegram")},
		SessionID:     locator.SessionID,
		TaskID:        taskID,
		CorrelationID: taskID,
		Priority:      70,
		DedupeKey:     dedupeKey,
		PayloadJSON:   string(data),
	}
	if _, err := a.coordinator.Dispatch(ctx, env); err != nil {
		return swarm.TransientError(err)
	}
	return nil
}
