package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func TestDurableCronRunRetriesWithSameExecutionKey(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceConfig,
		Enabled: true, DefinitionVersion: 1, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(-time.Minute)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	bus := &recordingHandlerCommandBus{commandErrs: []error{errors.New("broker unavailable")}}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), bus, now)
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.dispatchJob(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	key := "daily@" + job.NextRunAt.Format(time.RFC3339Nano)
	run, found, err := provider.ScheduleRuns().GetByTriggerKey(ctx, job.JobID, key)
	if err != nil || !found || run.DispatchState != state.ScheduleRunRetrying || run.Attempts != 1 ||
		run.SafeFailureCode != "dispatch_failed" {
		t.Fatalf("retry intent = %+v, %v", run, err)
	}
	current, _, _ := provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if !current.NextRunAt.Equal(job.NextRunAt) || current.LastDispatchKey != "" {
		t.Fatalf("failed publish moved cron cursor = %+v", current)
	}
	if err := scheduler.processPendingRuns(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	run, _, err = provider.ScheduleRuns().GetByTriggerKey(ctx, job.JobID, key)
	if err != nil || run.DispatchState != state.ScheduleRunDispatched || run.Attempts != 2 ||
		run.ExecutionJobID == "" || len(bus.commands) != 1 || bus.commands[0].DedupeKey != key {
		t.Fatalf("dispatched intent = %+v, commands=%d, %v", run, len(bus.commands), err)
	}
	current, _, _ = provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if current.LastDispatchKey != key || !current.NextRunAt.After(now) {
		t.Fatalf("cron cursor after publish = %+v", current)
	}
	if err := scheduler.dispatchJob(ctx, job, now); err != nil || len(bus.commands) != 1 {
		t.Fatalf("duplicate dispatch = %v, commands=%d", err, len(bus.commands))
	}
}

func TestManualIntentDispatchDoesNotMoveCronCursor(t *testing.T) {
	ctx := context.Background()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
		Enabled: false, DefinitionVersion: 3, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(time.Hour)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	run := state.ScheduleRunRecord{RunID: "manual-run", ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerManual, TriggerKey: "manual:nonce-123",
		DefinitionVersion: job.DefinitionVersion, RequestedAt: now,
		DispatchState: state.ScheduleRunPending, PayloadJSON: string(payload)}
	if created, err := provider.ScheduleRuns().Create(ctx, run); err != nil || !created {
		t.Fatalf("Create() = %v, %v", created, err)
	}
	bus := &recordingHandlerCommandBus{}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), bus, now)
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.processPendingRuns(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(bus.commands) != 1 || bus.commands[0].DedupeKey != run.TriggerKey {
		t.Fatalf("manual commands = %+v", bus.commands)
	}
	current, _, err := provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if err != nil || current.Enabled || !current.NextRunAt.Equal(job.NextRunAt) || current.LastDispatchKey != "" {
		t.Fatalf("manual moved cron cursor = %+v, %v", current, err)
	}
}

func TestPendingRunSurvivesRestartAndScheduleArchive(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "state.db")
	provider, err := state.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 1, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(time.Hour)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	run := state.ScheduleRunRecord{RunID: "pending-after-restart", ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerManual, TriggerKey: "manual:restart-123",
		DefinitionVersion: job.DefinitionVersion, RequestedAt: now,
		DispatchState: state.ScheduleRunPending, PayloadJSON: string(payload)}
	if created, err := provider.ScheduleRuns().Create(ctx, run); err != nil || !created {
		t.Fatalf("Create() = %v, %v", created, err)
	}
	job.Deleted, job.Enabled = true, false
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err = state.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	bus := &recordingHandlerCommandBus{}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), bus, now)
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.processPendingRuns(ctx, now); err != nil {
		t.Fatal(err)
	}
	stored, found, err := provider.ScheduleRuns().GetByID(ctx, run.RunID)
	if err != nil || !found || stored.DispatchState != state.ScheduleRunDispatched || len(bus.commands) != 1 {
		t.Fatalf("recovered run = %+v, commands=%d, %v", stored, len(bus.commands), err)
	}
}

func TestClaimedCronRunCannotUndoDisableBeforePublish(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 1, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(-time.Minute)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	bus := &recordingHandlerCommandBus{}
	bus.beforeDispatch = func() {
		key := "daily@" + job.NextRunAt.Format(time.RFC3339Nano)
		run, found, err := provider.ScheduleRuns().GetByTriggerKey(ctx, job.JobID, key)
		if err != nil || !found || run.DispatchState != state.ScheduleRunPublishing {
			t.Fatalf("run was not durably claimed before publish = %+v, %v", run, err)
		}
		job.Enabled = false
		job.DefinitionVersion++
		if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), bus, now)
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.dispatchJob(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	current, _, err := provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if err != nil || current.Enabled || current.DefinitionVersion != 2 ||
		!current.NextRunAt.Equal(job.NextRunAt) || len(bus.commands) != 1 {
		t.Fatalf("concurrent disable = %+v, commands=%d, %v", current, len(bus.commands), err)
	}
}

func TestPendingCronRunIsCanceledAfterDisable(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 1, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(-time.Minute)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	run := state.ScheduleRunRecord{RunID: "cron-before-disable", ScheduleID: job.JobID,
		Trigger:           state.ScheduleRunTriggerCron,
		TriggerKey:        "daily@" + job.NextRunAt.Format(time.RFC3339Nano),
		DefinitionVersion: job.DefinitionVersion, RequestedAt: now, DueAt: job.NextRunAt,
		DispatchState: state.ScheduleRunPending, PayloadJSON: string(payload)}
	if created, err := provider.ScheduleRuns().CreateCron(ctx, run, job.NextRunAt); err != nil || !created {
		t.Fatalf("CreateCron() = %v, %v", created, err)
	}
	job.Enabled = false
	job.DefinitionVersion++
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	bus := &recordingHandlerCommandBus{}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), bus, now)
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.processPendingRuns(ctx, now); err != nil {
		t.Fatal(err)
	}
	stored, found, err := provider.ScheduleRuns().GetByID(ctx, run.RunID)
	if err != nil || !found || stored.DispatchState != state.ScheduleRunCanceled || len(bus.commands) != 0 {
		t.Fatalf("stale cron run = %+v, commands=%d, %v", stored, len(bus.commands), err)
	}
}

func TestLateCronSettlementCannotChangeNewerSelection(t *testing.T) {
	const newKey = "new-key"
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 2, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "updated review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, LastDispatchKey: newKey,
		NextRunAt: now.Add(time.Hour)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	run := state.ScheduleRunRecord{RunID: "old-run", ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerCron, TriggerKey: "old-key",
		DefinitionVersion: 1, RequestedAt: now.Add(-time.Hour), DueAt: now.Add(-time.Hour),
		DispatchState: state.ScheduleRunDispatched, DispatchedAt: now.Add(-time.Hour),
		PayloadJSON: `{}`}
	if created, err := provider.ScheduleRuns().Create(ctx, run); err != nil || !created {
		t.Fatalf("Create(old) = %v, %v", created, err)
	}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), &recordingHandlerCommandBus{}, now)
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.RecordExecution(ctx, job.JobID, "old-key:session", errors.New("stale failure")); err != nil {
		t.Fatal(err)
	}
	current, _, err := provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if err != nil || current.LastError != "" || current.LastDispatchKey != newKey {
		t.Fatalf("late settlement changed selection = %+v, %v", current, err)
	}
	run.RunID, run.TriggerKey, run.DefinitionVersion = "new-run", newKey, 2
	if created, err := provider.ScheduleRuns().Create(ctx, run); err != nil || !created {
		t.Fatalf("Create(new) = %v, %v", created, err)
	}
	if err := scheduler.RecordExecution(ctx, job.JobID, newKey+":session", errors.New("current failure")); err != nil {
		t.Fatal(err)
	}
	current, _, err = provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if err != nil || current.LastError != "current failure" || current.LastDispatchKey != newKey {
		t.Fatalf("current settlement = %+v, %v", current, err)
	}
	if err := scheduler.RecordExecution(ctx, job.JobID, "manual:nonce-123:session", nil); err != nil {
		t.Fatal(err)
	}
	current, _, err = provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if err != nil || current.LastError != "current failure" {
		t.Fatalf("manual settlement changed cron = %+v, %v", current, err)
	}
}

func TestPublishedCronClaimRecoversAfterDisableAndCrash(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 77)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 1, SessionID: locator.SessionID,
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, Content: "review", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(-time.Minute)}
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	key := "daily@" + job.NextRunAt.Format(time.RFC3339Nano)
	run := state.ScheduleRunRecord{RunID: "claimed-before-crash", ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerCron, TriggerKey: key,
		DefinitionVersion: job.DefinitionVersion, RequestedAt: now, DueAt: job.NextRunAt,
		DispatchState: state.ScheduleRunPending, PayloadJSON: string(payload)}
	if created, err := provider.ScheduleRuns().CreateCron(ctx, run, job.NextRunAt); err != nil || !created {
		t.Fatalf("CreateCron() = %v, %v", created, err)
	}
	run, _, err = provider.ScheduleRuns().GetByID(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := provider.ScheduleRuns().ClaimCron(ctx, run, now, now.Add(cronPublishLease)); err != nil || !claimed {
		t.Fatalf("ClaimCron() = %v, %v", claimed, err)
	}
	bus := &recordingHandlerCommandBus{}
	env, err := turncmd.ScheduledJobEnvelope(job.JobID, job.Content, locator, nil, "101", 0, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Dispatch(ctx, env); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash before the run ledger was marked dispatched.
	job.Enabled = false
	job.DefinitionVersion++
	if err := provider.ScheduledJobs().Upsert(ctx, job); err != nil {
		t.Fatal(err)
	}
	scheduler := newSchedulerForTest(t, provider.ScheduledJobs(), bus, now.Add(cronPublishLease+time.Second))
	scheduler.runStore = provider.ScheduleRuns()
	if err := scheduler.processPendingRuns(ctx, now.Add(cronPublishLease+time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, found, err := provider.ScheduleRuns().GetByID(ctx, run.RunID)
	if err != nil || !found || stored.DispatchState != state.ScheduleRunDispatched || len(bus.commands) != 2 ||
		bus.commands[0].DedupeKey != bus.commands[1].DedupeKey ||
		actorcmd.EnvelopeJobID(bus.commands[0]) != actorcmd.EnvelopeJobID(bus.commands[1]) {
		t.Fatalf("recovered published run = %+v, commands=%d, %v", stored, len(bus.commands), err)
	}
	current, _, err := provider.ScheduledJobs().GetByID(ctx, job.JobID)
	if err != nil || current.Enabled || current.DefinitionVersion != 2 {
		t.Fatalf("recovery changed disabled schedule = %+v, %v", current, err)
	}
}
