package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestTaskActorStartGoalDispatchesPlannerFirst(t *testing.T) {
	ctx := context.Background()
	_, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	exec := &taskActorExecutor{tasks: tasks, coordinator: coordinator, agents: allocator, maxIters: 3}
	locator := taskActorTestLocator()
	env, goal := taskActorGoalEnvelope(t, locator, "fix failing tests", 3)

	if err := exec.startGoalTask(ctx, env, goal); err != nil {
		t.Fatalf("startGoalTask() error = %v", err)
	}

	if len(bus.commands) != 3 {
		t.Fatalf("published commands = %d, want start delivery + planner-start delivery + planner", len(bus.commands))
	}
	planner := bus.commands[2]
	if planner.To.Target != swarm.ActorTypeAgent || planner.To.Key != swarm.AgentNamePlanner {
		t.Fatalf("planner command target = %+v, want agent:planner", planner.To)
	}
	var command taskAgentCommandPayload
	if err := json.Unmarshal([]byte(planner.PayloadJSON), &command); err != nil {
		t.Fatalf("decode planner command: %v", err)
	}
	if command.Role != taskAgentRolePlanner || command.AgentName != swarm.AgentNamePlanner {
		t.Fatalf("planner command role/name = %q/%q, want planner/planner", command.Role, command.AgentName)
	}
}

func TestTaskActorPlannerResultStoresPlanAndDispatchesExecutor(t *testing.T) {
	ctx := context.Background()
	_, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	exec := &taskActorExecutor{tasks: tasks, coordinator: coordinator, agents: allocator, maxIters: 3}
	locator := taskActorTestLocator()
	env, goal := taskActorGoalEnvelope(t, locator, "fix failing tests", 3)
	if err := exec.startGoalTask(ctx, env, goal); err != nil {
		t.Fatalf("startGoalTask() error = %v", err)
	}

	plannerText := "1. inspect tests\n2. patch code\n3. run go test ./..."
	resultEnv := swarm.Envelope{ID: "planner-result-1", Namespace: swarm.NamespaceAgentResult, Kind: swarm.KindGoal, From: swarm.ActorAddress{Target: swarm.ActorTypeAgent, Key: swarm.AgentNamePlanner}, To: swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: goal.TaskID}, SessionID: locator.SessionID, TaskID: goal.TaskID, PayloadJSON: `{}`}
	if err := exec.handleAgentResult(ctx, resultEnv, taskAgentResultPayload{
		TaskID: goal.TaskID, AgentName: swarm.AgentNamePlanner, Role: taskAgentRolePlanner, Iteration: 1, Locator: locator, Objective: goal.Objective, TransportUserID: goal.TransportUserID, Text: plannerText, MaxIterations: goal.MaxIterations,
	}); err != nil {
		t.Fatalf("handleAgentResult(planner) error = %v", err)
	}

	last := bus.commands[len(bus.commands)-1]
	if last.To.Target != swarm.ActorTypeAgent || last.To.Key != swarm.AgentNameExecutor {
		t.Fatalf("executor command target = %+v, want agent:executor", last.To)
	}
	var command taskAgentCommandPayload
	if err := json.Unmarshal([]byte(last.PayloadJSON), &command); err != nil {
		t.Fatalf("decode executor command: %v", err)
	}
	if command.Role != taskAgentRoleExecutor || command.Plan != plannerText || command.PlannerOutput != plannerText {
		t.Fatalf("executor command = %+v, want executor with planner text", command)
	}

	task, ok, err := tasks.Get(ctx, goal.TaskID)
	if err != nil {
		t.Fatalf("Get(task) error = %v", err)
	}
	if !ok || !strings.Contains(task.PlanJSON, "inspect tests") {
		t.Fatalf("task = %+v found=%v, want planner output", task, ok)
	}
}

func TestSubmitWebhookTaskUsesStableTaskAndDistinctDedupeKeys(t *testing.T) {
	ctx := context.Background()
	bus := &recordingHandlerCommandBus{}
	handler := &BaldaHandler{swarmCoordinator: swarm.NewCoordinator(bus, swarm.Config{Enabled: true})}
	locator := taskActorTestLocator()
	payload := sessionTurnPayload{
		Text:      "release event",
		Locator:   locator,
		UserID:    "tg-101",
		Source:    "webhook",
		DedupeKey: "webhook:release:req-1",
	}

	result, taskID, err := handler.submitWebhookTask(ctx, payload, "release", "req-1")
	if err != nil {
		t.Fatalf("submitWebhookTask() error = %v", err)
	}
	_, duplicateTaskID, err := handler.submitWebhookTask(ctx, payload, "release", "req-1")
	if err != nil {
		t.Fatalf("submitWebhookTask(duplicate) error = %v", err)
	}
	if taskID == "" || duplicateTaskID != taskID {
		t.Fatalf("task ids = %q/%q, want stable non-empty task id", taskID, duplicateTaskID)
	}
	if result.MsgID != "webhook:release:req-1:task" {
		t.Fatalf("msg id = %q, want task-scoped dedupe", result.MsgID)
	}
	if got := len(bus.commands); got != 2 {
		t.Fatalf("published commands = %d, want 2 recording bus calls", got)
	}
	parent := bus.commands[0]
	if parent.DedupeKey != "webhook:release:req-1:task" {
		t.Fatalf("parent dedupe = %q, want task-scoped key", parent.DedupeKey)
	}
	var body taskEnvelopePayload
	if err := json.Unmarshal([]byte(parent.PayloadJSON), &body); err != nil {
		t.Fatalf("decode parent payload: %v", err)
	}
	if body.SessionTurn == nil || body.SessionTurn.DedupeKey != "webhook:release:req-1:session" {
		t.Fatalf("child dedupe in payload = %+v, want session-scoped key", body.SessionTurn)
	}
}

func TestTaskActorDispatchSessionTurnKeepsTaskRunningUntilSessionCompletes(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = coordinator
	_ = allocator
	exec := &taskActorExecutor{tasks: tasks, coordinator: swarm.NewCoordinator(bus, swarm.Config{Enabled: true})}
	locator := taskActorTestLocator()
	taskID := "webhook-release-abc"
	payload := sessionTurnPayload{
		Text:      "release event",
		Locator:   locator,
		UserID:    "tg-101",
		Source:    "webhook",
		DedupeKey: "webhook:release:req-1:session",
	}
	env := swarm.Envelope{
		ID:          "task-command-1",
		Namespace:   swarm.NamespaceWebhookInbound,
		Kind:        swarm.KindWebhookEvent,
		From:        swarm.ActorAddress{Target: "webhook", Key: "release"},
		To:          swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: taskID},
		SessionID:   locator.SessionID,
		TaskID:      taskID,
		PayloadJSON: `{}`,
	}

	if err := exec.dispatchSessionTurn(ctx, env, payload); err != nil {
		t.Fatalf("dispatchSessionTurn() error = %v", err)
	}
	task, ok, err := tasks.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get(task) error = %v", err)
	}
	if !ok || task.Status != baldastate.SwarmTaskStatusRunning {
		t.Fatalf("task = %+v found=%v, want running until SessionActor completes it", task, ok)
	}
	last := bus.commands[len(bus.commands)-1]
	if last.To.Target != swarm.ActorTypeSession || last.DedupeKey != payload.DedupeKey {
		t.Fatalf("child session command = %+v, want session command with child dedupe", last)
	}
}

func TestSessionActorCompletesTaskAfterTurnSuccess(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	taskID := "webhook-release-complete"
	_, err := tasks.Create(ctx, baldastate.SwarmTaskRecord{
		ID:          taskID,
		SessionID:   "tg-9001-99",
		Title:       "Webhook task",
		Objective:   "release event",
		Status:      baldastate.SwarmTaskStatusRunning,
		CreatedFrom: "webhook",
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	exec := &sessionActorExecutor{tasks: tasks}
	exec.recordSessionTaskResult(ctx, swarm.Envelope{
		ID:        "session-command-1",
		Namespace: swarm.NamespaceWebhookInbound,
		Kind:      swarm.KindWebhookEvent,
		TaskID:    taskID,
	}, nil)

	task, ok, err := tasks.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get(task) error = %v", err)
	}
	if !ok || task.Status != baldastate.SwarmTaskStatusCompleted {
		t.Fatalf("task = %+v found=%v, want completed", task, ok)
	}
}

func newTaskActorSwarmServices(t *testing.T, ctx context.Context) (baldastate.Provider, *recordingHandlerCommandBus, *swarm.Coordinator, *swarm.TaskService, *swarm.AgentAllocator) {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	bus := &recordingHandlerCommandBus{}
	cfg, err := (swarm.Config{Enabled: true}).Normalized()
	if err != nil {
		t.Fatalf("Normalize swarm config: %v", err)
	}
	var coordinator *swarm.Coordinator
	var tasks *swarm.TaskService
	var allocator *swarm.AgentAllocator
	app := fxtest.New(t,
		fx.Supply(fx.Annotate(provider, fx.As(new(baldastate.Provider))), fx.Annotate(bus, fx.As(new(swarm.CommandBus))), cfg),
		fx.Provide(swarm.NewTaskService, swarm.NewAgentRegistry, swarm.NewAgentAllocator, swarm.NewCoordinator),
		fx.Populate(&coordinator, &tasks, &allocator),
	)
	app.RequireStart()
	t.Cleanup(func() { app.RequireStop() })
	return provider, bus, coordinator, tasks, allocator
}

func taskActorTestLocator() baldasession.SessionLocator {
	return baldasession.SessionLocator{SessionID: "tg-9001-99", ChannelType: "telegram", AddressKey: "9001:99"}
}

func taskActorGoalEnvelope(t *testing.T, locator baldasession.SessionLocator, objective string, maxIterations int) (swarm.Envelope, goalTaskPayload) {
	t.Helper()
	env, err := goalTaskEnvelope(locator, objective, "tg-101", maxIterations)
	if err != nil {
		t.Fatalf("goalTaskEnvelope() error = %v", err)
	}
	var payload taskEnvelopePayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode goal payload: %v", err)
	}
	if payload.Goal == nil {
		t.Fatal("goal payload is nil")
	}
	return env, *payload.Goal
}
