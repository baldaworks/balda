package handlers

import (
	"context"
	"testing"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

func TestTaskControlActorCancelsSessionWork(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	locator := baldatelegram.NewLocator(9001, 0)
	_, err := tasks.Create(ctx, baldastate.SwarmTaskRecord{
		ID:        "task-session",
		SessionID: locator.SessionID,
		Objective: "active",
		Status:    baldastate.SwarmTaskStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	turns := &fakeTurnDispatcher{cancelHadInFlight: true, cancelDropped: 2}
	actor := &taskControlActor{
		turnDispatcher: turns,
		tasks:          tasks,
		taskRuns:       newTaskRunRegistry(),
		channel:        newBaldaTestTelegramAdapter(),
	}
	env, err := controlCancelEnvelope(locator, "", testTelegramUserID101, "session canceled by user")
	if err != nil {
		t.Fatalf("controlCancelEnvelope() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(turns.cancelCalls) != 1 || turns.cancelCalls[0].SessionID != locator.SessionID {
		t.Fatalf("CancelSession calls = %+v, want one session cancel", turns.cancelCalls)
	}
	task, ok, err := tasks.Get(ctx, "task-session")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.SwarmTaskStatusCanceled {
		t.Fatalf("task = %+v found=%v, want canceled", task, ok)
	}
}

func TestTaskControlActorCancelsTaskWork(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	locator := baldatelegram.NewLocator(9001, 0)
	_, err := tasks.Create(ctx, baldastate.SwarmTaskRecord{
		ID:        "task-one",
		SessionID: locator.SessionID,
		Objective: "active",
		Status:    baldastate.SwarmTaskStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	actor := &taskControlActor{
		turnDispatcher: &fakeTurnDispatcher{},
		tasks:          tasks,
		taskRuns:       newTaskRunRegistry(),
		channel:        newBaldaTestTelegramAdapter(),
	}
	env, err := controlCancelEnvelope(locator, "task-one", testTelegramUserID101, "task canceled by user")
	if err != nil {
		t.Fatalf("controlCancelEnvelope() error = %v", err)
	}
	if env.Namespace != swarm.NamespaceTaskControl || env.TaskID != "task-one" {
		t.Fatalf("control env = %+v, want task control for task-one", env)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	task, ok, err := tasks.Get(ctx, "task-one")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.SwarmTaskStatusCanceled {
		t.Fatalf("task = %+v found=%v, want canceled", task, ok)
	}
}
