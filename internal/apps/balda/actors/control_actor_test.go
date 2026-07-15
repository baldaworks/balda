package actors

import (
	"context"
	"testing"
	"time"

	baldaslackagent "github.com/normahq/balda/internal/apps/balda/channel/slackagent"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/controlapp"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

func TestTaskControlActorCancelsSessionWork(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator
	locator := baldatelegram.NewLocator(9001, 0)
	_, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:        "task-session",
		SessionID: locator.SessionID,
		Objective: "active",
		Status:    baldastate.JobStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	turns := &fakeTurnDispatcher{}
	service := controlapp.New(turns, dispatcher, tasks, nil, NewJobRunRegistry(), zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: turns,
		dispatcher:     dispatcher,
		jobs:           tasks,
		jobRuns:        NewJobRunRegistry(),
		service:        service,
	}
	env, err := ControlCancelEnvelope(locator, "", testTelegramUserID101, "session canceled by user")
	if err != nil {
		t.Fatalf("ControlCancelEnvelope() error = %v", err)
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
	if !ok || task.Status != baldastate.JobStatusCanceled {
		t.Fatalf("task = %+v found=%v, want canceled", task, ok)
	}
}

func TestTaskControlActorCancelsSessionTurnOnly(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator
	locator := baldatelegram.NewLocator(9001, 0)
	_, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:            "goal-task",
		SessionID:     locator.SessionID,
		Objective:     "active goal",
		Status:        baldastate.JobStatusRunning,
		OwnerActor:    baldaexecution.ActorTypeGoalkeeper + ":goal-task",
		AssignedActor: baldaexecution.ActorTypeGoalkeeper + ":goal-task",
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	turns := &fakeTurnDispatcher{}
	service := controlapp.New(turns, dispatcher, tasks, nil, NewJobRunRegistry(), zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: turns,
		dispatcher:     dispatcher,
		jobs:           tasks,
		jobRuns:        NewJobRunRegistry(),
		service:        service,
	}
	env, err := ControlCancelTurnEnvelopeWithNotify(locator, testTelegramUserID101, "session turn canceled by user", true)
	if err != nil {
		t.Fatalf("ControlCancelTurnEnvelopeWithNotify() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(turns.cancelCalls) != 1 || turns.cancelCalls[0].SessionID != locator.SessionID {
		t.Fatalf("CancelSession calls = %+v, want one session cancel", turns.cancelCalls)
	}
	task, ok, err := tasks.Get(ctx, "goal-task")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.JobStatusRunning {
		t.Fatalf("task = %+v found=%v, want still running", task, ok)
	}
}

func TestTaskControlActorCancelsTaskWork(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator
	locator := baldatelegram.NewLocator(9001, 0)
	_, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:        "task-one",
		SessionID: locator.SessionID,
		Objective: "active",
		Status:    baldastate.JobStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	registry := NewJobRunRegistry()
	service := controlapp.New(&fakeTurnDispatcher{}, dispatcher, tasks, nil, registry, zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: &fakeTurnDispatcher{},
		dispatcher:     dispatcher,
		jobs:           tasks,
		jobRuns:        registry,
		service:        service,
	}
	env, err := ControlCancelEnvelope(locator, "task-one", testTelegramUserID101, "task canceled by user")
	if err != nil {
		t.Fatalf("ControlCancelEnvelope() error = %v", err)
	}
	if env.Namespace != baldaexecution.NamespaceJobControl || baldaexecution.EnvelopeJobID(env) != "task-one" {
		t.Fatalf("control env = %+v, want job control for task-one", env)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	task, ok, err := tasks.Get(ctx, "task-one")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.JobStatusCanceled {
		t.Fatalf("task = %+v found=%v, want canceled", task, ok)
	}
}

func TestTaskControlActorCancelsAllRegisteredTaskRuns(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator

	locator := baldatelegram.NewLocator(9001, 0)
	_, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:        "task-multi-run",
		SessionID: locator.SessionID,
		Objective: "active",
		Status:    baldastate.JobStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}

	registry := NewJobRunRegistry()
	runCtxOne, cancelOne := context.WithCancel(context.Background())
	defer cancelOne()
	runCtxTwo, cancelTwo := context.WithCancel(context.Background())
	defer cancelTwo()
	registry.Register("task-multi-run", cancelOne)
	registry.Register("task-multi-run", cancelTwo)

	service := controlapp.New(&fakeTurnDispatcher{}, dispatcher, tasks, nil, registry, zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: &fakeTurnDispatcher{},
		dispatcher:     dispatcher,
		jobs:           tasks,
		jobRuns:        registry,
		service:        service,
	}

	env, err := ControlCancelEnvelope(locator, "task-multi-run", testTelegramUserID101, "task canceled by user")
	if err != nil {
		t.Fatalf("ControlCancelEnvelope() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	waitCancelDone(t, runCtxOne, "run one")
	waitCancelDone(t, runCtxTwo, "run two")

	task, ok, err := tasks.Get(ctx, "task-multi-run")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.JobStatusCanceled {
		t.Fatalf("task = %+v found=%v, want canceled", task, ok)
	}
}

func TestTaskControlActorClearsGoalJobsOnly(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator

	locator := baldatelegram.NewLocator(9001, 0)
	for _, task := range []baldastate.JobRecord{
		{
			ID:            "goal-task",
			SessionID:     locator.SessionID,
			Objective:     "goal",
			Status:        baldastate.JobStatusRunning,
			OwnerActor:    baldaexecution.ActorTypeGoalkeeper + ":goal-task",
			AssignedActor: baldaexecution.ActorTypeGoalkeeper + ":goal-task",
		},
		{
			ID:            "non-goal-task",
			SessionID:     locator.SessionID,
			Objective:     "turn",
			Status:        baldastate.JobStatusRunning,
			OwnerActor:    baldaexecution.ActorTypeSession + ":non-goal-task",
			AssignedActor: baldaexecution.ActorTypeSession + ":non-goal-task",
		},
	} {
		if _, err := tasks.Create(ctx, task, "test", nil); err != nil {
			t.Fatalf("Create task %s: %v", task.ID, err)
		}
	}

	turns := &fakeTurnDispatcher{}
	service := controlapp.New(turns, dispatcher, tasks, nil, NewJobRunRegistry(), zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: turns,
		dispatcher:     dispatcher,
		jobs:           tasks,
		jobRuns:        NewJobRunRegistry(),
		service:        service,
	}
	env, err := ControlClearGoalEnvelopeWithNotify(locator, testTelegramUserID101, "goal cleared by user", true)
	if err != nil {
		t.Fatalf("ControlClearGoalEnvelopeWithNotify() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(turns.cancelCalls) != 0 {
		t.Fatalf("CancelSession calls = %+v, want 0", turns.cancelCalls)
	}

	goalJob, ok, err := tasks.Get(ctx, "goal-task")
	if err != nil {
		t.Fatalf("Get goal job: %v", err)
	}
	if !ok || goalJob.Status != baldastate.JobStatusCanceled {
		t.Fatalf("goal job = %+v found=%v, want canceled", goalJob, ok)
	}
	nonGoalJob, ok, err := tasks.Get(ctx, "non-goal-task")
	if err != nil {
		t.Fatalf("Get non-goal job: %v", err)
	}
	if !ok || nonGoalJob.Status != baldastate.JobStatusRunning {
		t.Fatalf("non-goal job = %+v found=%v, want still running", nonGoalJob, ok)
	}
}

func TestTaskControlActorSchedulesOneShotWait(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = bus
	_ = allocator
	_ = tasks
	locator := baldatelegram.NewLocator(9001, 0)
	store := provider.ScheduledJobs()
	registry := NewJobRunRegistry()
	service := controlapp.New(&fakeTurnDispatcher{}, dispatcher, nil, store, registry, zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: &fakeTurnDispatcher{},
		dispatcher:     dispatcher,
		scheduledJobs:  store,
		jobRuns:        registry,
		service:        service,
	}
	env, err := ControlScheduleWaitEnvelope(locator, "wait-1", "wake me", 60, testTelegramUserID101, false)
	if err != nil {
		t.Fatalf("ControlScheduleWaitEnvelope() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	job, ok, err := store.GetByID(ctx, "wait-1")
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() found = false, want true")
	}
	if got, want := job.ScheduleSpec, scheduledJobOneShotSpec; got != want {
		t.Fatalf("ScheduleSpec = %q, want %q", got, want)
	}
	if got, want := job.Status, baldastate.ScheduledJobStatusActive; got != want {
		t.Fatalf("Status = %q, want %q", got, want)
	}
	if got, want := job.Content, "wake me"; got != want {
		t.Fatalf("Content = %q, want %q", got, want)
	}
	if !job.ReportToEnabled {
		t.Fatal("ReportToEnabled = false, want true")
	}
	if job.NextRunAt.Before(time.Now().UTC().Add(50*time.Second)) || job.NextRunAt.After(time.Now().UTC().Add(70*time.Second)) {
		t.Fatalf("NextRunAt = %s, want about 60s in future", job.NextRunAt)
	}
}

func TestTaskControlActorSchedulesOneShotWaitForSlackAgentLocator(t *testing.T) {
	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = bus
	_ = allocator
	_ = tasks
	locator := baldaslackagent.NewThreadLocator("T123", "C456", "thread-789")
	store := provider.ScheduledJobs()
	registry := NewJobRunRegistry()
	service := controlapp.New(&fakeTurnDispatcher{}, dispatcher, nil, store, registry, zerolog.Nop())
	actor := &jobControlActor{
		turnDispatcher: &fakeTurnDispatcher{},
		dispatcher:     dispatcher,
		scheduledJobs:  store,
		jobRuns:        registry,
		service:        service,
	}
	env, err := ControlScheduleWaitEnvelope(locator, "wait-slack-agent-1", "wake slack agent", 60, "slack:T123:U456", false)
	if err != nil {
		t.Fatalf("ControlScheduleWaitEnvelope() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	job, ok, err := store.GetByID(ctx, "wait-slack-agent-1")
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() found = false, want true")
	}
	if got, want := job.ChannelType, locator.ChannelType; got != want {
		t.Fatalf("ChannelType = %q, want %q", got, want)
	}
	if got, want := job.AddressKey, locator.AddressKey; got != want {
		t.Fatalf("AddressKey = %q, want %q", got, want)
	}
	if got, want := job.ReportToChannelType, locator.ChannelType; got != want {
		t.Fatalf("ReportToChannelType = %q, want %q", got, want)
	}
	if got, want := job.ReportToAddressKey, locator.AddressKey; got != want {
		t.Fatalf("ReportToAddressKey = %q, want %q", got, want)
	}
}

func waitCancelDone(t *testing.T, runCtx context.Context, label string) {
	t.Helper()
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %s cancellation", label)
	}
}
