package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

func TestTaskAgentActorHandleSkipsDuplicateRunningStep(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	payload, env := taskAgentCommandForTest(t, "task-running-duplicate", taskAgentRoleExecutor, 1)
	stepKey := taskAgentStepKey(payload)
	if _, _, err := tasks.ReserveAgentStep(ctx, baldastate.SwarmAgentStepRecord{
		ID:          "step-running-duplicate",
		StepKey:     stepKey,
		TaskID:      payload.TaskID,
		AgentName:   payload.AgentName,
		Role:        payload.Role,
		Iteration:   payload.Iteration,
		PayloadHash: hashTaskAgentCommandPayload(payload),
		Status:      baldastate.SwarmAgentStepStatusRunning,
	}); err != nil {
		t.Fatalf("ReserveAgentStep() error = %v", err)
	}

	actor := &taskAgentActor{tasks: tasks}
	err := actor.Handle(ctx, env)
	if err == nil {
		t.Fatal("Handle() error = nil, want duplicate running step")
	}
	if swarm.ClassifyError(err) != swarm.ErrorKindTransient {
		t.Fatalf("ClassifyError(%v) = %s, want transient", err, swarm.ClassifyError(err))
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("Handle() error = %v, want already running marker", err)
	}
}

func TestTaskAgentActorHandleReplaysStoredSucceededResult(t *testing.T) {
	ctx := context.Background()
	_, bus, coordinator, tasks, _ := newTaskActorSwarmServices(t, ctx)
	payload, env := taskAgentCommandForTest(t, "task-replay-succeeded", taskAgentRoleExecutor, 1)
	stepKey := taskAgentStepKey(payload)
	resultJSON, err := marshalTaskAgentResult(payload, "done", nil)
	if err != nil {
		t.Fatalf("marshalTaskAgentResult() error = %v", err)
	}
	if _, _, err := tasks.ReserveAgentStep(ctx, baldastate.SwarmAgentStepRecord{
		ID:          "step-replay-succeeded",
		StepKey:     stepKey,
		TaskID:      payload.TaskID,
		AgentName:   payload.AgentName,
		Role:        payload.Role,
		Iteration:   payload.Iteration,
		PayloadHash: hashTaskAgentCommandPayload(payload),
		Status:      baldastate.SwarmAgentStepStatusRunning,
	}); err != nil {
		t.Fatalf("ReserveAgentStep() error = %v", err)
	}
	if err := tasks.CompleteAgentStep(ctx, stepKey, resultJSON); err != nil {
		t.Fatalf("CompleteAgentStep() error = %v", err)
	}

	actor := &taskAgentActor{tasks: tasks, coordinator: coordinator}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	resultEnv := lastPublishedCommandTo(t, bus, swarm.ActorTypeTask, payload.TaskID)
	if resultEnv.DedupeKey != taskAgentResultDedupeKey(payload) {
		t.Fatalf("result dedupe key = %q, want %q", resultEnv.DedupeKey, taskAgentResultDedupeKey(payload))
	}
	if strings.TrimSpace(resultEnv.PayloadJSON) != strings.TrimSpace(resultJSON) {
		t.Fatalf("result payload mismatch = %q", resultEnv.PayloadJSON)
	}
}

func TestTaskAgentActorHandleReplaysStoredFailedResult(t *testing.T) {
	ctx := context.Background()
	_, bus, coordinator, tasks, _ := newTaskActorSwarmServices(t, ctx)
	payload, env := taskAgentCommandForTest(t, "task-replay-failed", taskAgentRoleReviewer, 2)
	stepKey := taskAgentStepKey(payload)
	resultJSON, err := marshalTaskAgentResult(payload, "", errors.New("agent failed"))
	if err != nil {
		t.Fatalf("marshalTaskAgentResult() error = %v", err)
	}
	if _, _, err := tasks.ReserveAgentStep(ctx, baldastate.SwarmAgentStepRecord{
		ID:          "step-replay-failed",
		StepKey:     stepKey,
		TaskID:      payload.TaskID,
		AgentName:   payload.AgentName,
		Role:        payload.Role,
		Iteration:   payload.Iteration,
		PayloadHash: hashTaskAgentCommandPayload(payload),
		Status:      baldastate.SwarmAgentStepStatusRunning,
	}); err != nil {
		t.Fatalf("ReserveAgentStep() error = %v", err)
	}
	if err := tasks.FailAgentStep(ctx, stepKey, resultJSON, "agent failed"); err != nil {
		t.Fatalf("FailAgentStep() error = %v", err)
	}

	actor := &taskAgentActor{tasks: tasks, coordinator: coordinator}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	resultEnv := lastPublishedCommandTo(t, bus, swarm.ActorTypeTask, payload.TaskID)
	if resultEnv.DedupeKey != taskAgentResultDedupeKey(payload) {
		t.Fatalf("result dedupe key = %q, want %q", resultEnv.DedupeKey, taskAgentResultDedupeKey(payload))
	}
}

func TestTaskAgentActorHandleRejectsStepPayloadHashMismatch(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	payload, env := taskAgentCommandForTest(t, "task-hash-mismatch", taskAgentRolePlanner, 1)
	stepKey := taskAgentStepKey(payload)
	if _, _, err := tasks.ReserveAgentStep(ctx, baldastate.SwarmAgentStepRecord{
		ID:          "step-hash-mismatch",
		StepKey:     stepKey,
		TaskID:      payload.TaskID,
		AgentName:   payload.AgentName,
		Role:        payload.Role,
		Iteration:   payload.Iteration,
		PayloadHash: "different",
		Status:      baldastate.SwarmAgentStepStatusRunning,
	}); err != nil {
		t.Fatalf("ReserveAgentStep() error = %v", err)
	}

	actor := &taskAgentActor{tasks: tasks}
	err := actor.Handle(ctx, env)
	if err == nil {
		t.Fatal("Handle() error = nil, want payload mismatch")
	}
	if swarm.ClassifyError(err) != swarm.ErrorKindPermanent {
		t.Fatalf("ClassifyError(%v) = %s, want permanent", err, swarm.ClassifyError(err))
	}
	if !strings.Contains(err.Error(), "different payload") {
		t.Fatalf("Handle() error = %v, want payload mismatch message", err)
	}
}

func taskAgentCommandForTest(t *testing.T, taskID string, role string, iteration int) (taskAgentCommandPayload, swarm.Envelope) {
	t.Helper()
	locator := taskActorTestLocator()
	payload := taskAgentCommandPayload{
		TaskID:          taskID,
		AgentName:       role,
		Role:            role,
		Iteration:       iteration,
		Locator:         locator,
		Objective:       "test objective",
		TransportUserID: "tg-101",
		MaxIterations:   3,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal(payload) error = %v", err)
	}
	env := swarm.Envelope{
		ID:            taskID + ":command:" + role,
		Namespace:     swarm.NamespaceAgentCommand,
		Kind:          swarm.KindGoal,
		From:          swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: taskID},
		To:            swarm.ActorAddress{Target: swarm.ActorTypeAgent, Key: role},
		SessionID:     locator.SessionID,
		TaskID:        taskID,
		CorrelationID: taskID,
		DedupeKey:     taskID + ":agent:" + role + ":" + role + ":" + strconv.Itoa(iteration),
		PayloadJSON:   string(data),
	}
	return payload, env
}
