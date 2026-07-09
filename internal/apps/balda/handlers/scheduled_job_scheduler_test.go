package handlers

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/auth"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

func TestScheduledJobSchedulerDispatchJob_PublishesCommandAndReschedules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := baldatelegram.NewLocator(9001, 77)
	now := time.Date(2026, time.May, 14, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Second)

	record := baldastate.ScheduledJobRecord{
		JobID:        "task-1",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Content:      "summarize repo health",
		ScheduleSpec: "@every 2s",
		Status:       baldastate.ScheduledJobStatusActive,
		MaxRetries:   3,
		NextRunAt:    dueAt,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	bus := &recordingHandlerCommandBus{}
	scheduler := newSchedulerForTest(t, store, bus, now)

	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() error = %v", err)
	}
	if got := len(bus.commands); got != 1 {
		t.Fatalf("published commands = %d, want 1", got)
	}
	command := bus.commands[0]
	if got, want := command.Namespace, baldaexecution.NamespaceScheduleInbound; got != want {
		t.Fatalf("namespace = %q, want %q", got, want)
	}
	if got, want := command.Kind, baldaexecution.KindScheduledJob; got != want {
		t.Fatalf("kind = %q, want %q", got, want)
	}
	if command.ID == "" {
		t.Fatal("command id is empty")
	}
	if got, want := command.SessionID, locator.SessionID; got != want {
		t.Fatalf("session_id = %q, want %q", got, want)
	}
	wantKey := record.JobID + "@" + dueAt.UTC().Format(time.RFC3339Nano)
	if got, want := command.DedupeKey, wantKey; got != want {
		t.Fatalf("dedupe_key = %q, want %q", got, want)
	}

	updated, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() = not found, want found")
	}
	if got, want := updated.NextRunAt, now.Add(2*time.Second); !got.Equal(want) {
		t.Fatalf("NextRunAt = %s, want %s", got, want)
	}
	if got := updated.LastDispatchKey; got != wantKey {
		t.Fatalf("LastDispatchKey = %q, want %q", got, wantKey)
	}
	if got := updated.RetryCount; got != 0 {
		t.Fatalf("RetryCount = %d, want 0", got)
	}
}

func TestScheduledJobSchedulerDispatchJob_PublishesWithoutRestoringSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := baldatelegram.NewLocator(9001, 88)
	now := time.Date(2026, time.May, 14, 12, 30, 0, 0, time.UTC)

	record := baldastate.ScheduledJobRecord{
		JobID:        "task-restore",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Content:      "restore and run",
		ScheduleSpec: "@every 10s",
		Status:       baldastate.ScheduledJobStatusActive,
		NextRunAt:    now.Add(-2 * time.Second),
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	bus := &recordingHandlerCommandBus{}
	scheduler := newSchedulerForTest(t, store, bus, now)

	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() error = %v", err)
	}
	if got := len(bus.commands); got != 1 {
		t.Fatalf("published commands = %d, want 1", got)
	}
}

func TestScheduledJobSchedulerReconcileConfiguredTasks_LocatorTarget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	now := time.Date(2026, time.May, 14, 15, 0, 0, 0, time.UTC)

	scheduler := &ScheduledJobScheduler{
		jobStore: store,
		owner:    newOwnerStoreForTest(t, 101, 9001),
		logger:   zerolog.Nop(),
		now:      func() time.Time { return now },
		config: ScheduledJobSchedulerConfig{
			Jobs: []ConfiguredScheduledJob{
				{
					ID:      "managed-task",
					Cron:    "@every 2s",
					Target:  "locator",
					Key:     "telegram:-1002667079342:8939",
					Content: "review queue",
					ReportTo: &ConfiguredScheduledJobTarget{
						Target: "locator",
						Key:    "telegram:9001:0",
					},
				},
			},
		},
	}

	if err := scheduler.reconcileConfiguredJobs(ctx); err != nil {
		t.Fatalf("reconcileConfiguredJobs() error = %v", err)
	}

	managed, ok, err := store.GetByID(ctx, "managed-task")
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() = not found, want found")
	}
	if got, want := managed.SessionID, testLocatorTopicSessionID; got != want {
		t.Fatalf("SessionID = %q, want %q", got, want)
	}
	if got, want := managed.AddressKey, "-1002667079342:8939"; got != want {
		t.Fatalf("AddressKey = %q, want %q", got, want)
	}
	if !managed.ReportToEnabled {
		t.Fatal("ReportToEnabled = false, want true")
	}
	if got, want := managed.ReportToAddressKey, "9001:0"; got != want {
		t.Fatalf("ReportToAddressKey = %q, want %q", got, want)
	}
}

func TestScheduledJobSchedulerDispatchJob_IdempotentForSameDueSlot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := baldatelegram.NewLocator(9001, 99)
	now := time.Date(2026, time.May, 14, 13, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Second)

	record := baldastate.ScheduledJobRecord{
		JobID:        "task-idempotent",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Content:      "same slot should dispatch once",
		ScheduleSpec: "@every 5s",
		Status:       baldastate.ScheduledJobStatusActive,
		NextRunAt:    dueAt,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	bus := &recordingHandlerCommandBus{}
	scheduler := newSchedulerForTest(t, store, bus, now)

	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() first call error = %v", err)
	}
	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() second call error = %v", err)
	}
	if got := len(bus.commands); got != 1 {
		t.Fatalf("published commands after duplicate dispatch = %d, want 1", got)
	}

	updated, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() = not found, want found")
	}
	wantKey := record.JobID + "@" + dueAt.UTC().Format(time.RFC3339Nano)
	if got, want := updated.LastDispatchKey, wantKey; got != want {
		t.Fatalf("LastDispatchKey = %q, want %q", got, want)
	}
}

func TestScheduledJobSchedulerMarkFailure_RetryThenPause(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := baldatelegram.NewLocator(9001, 101)
	start := time.Date(2026, time.May, 14, 14, 0, 0, 0, time.UTC)

	record := baldastate.ScheduledJobRecord{
		JobID:        "task-fail",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Content:      "will fail",
		ScheduleSpec: "@every 1m",
		Status:       baldastate.ScheduledJobStatusActive,
		MaxRetries:   1,
		NextRunAt:    start,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	clock := &schedulerClock{now: start}
	scheduler := &ScheduledJobScheduler{
		jobStore: store,
		logger:   zerolog.Nop(),
		now:      clock.Now,
	}

	firstCause := errors.New("boom one")
	if err := scheduler.markFailure(ctx, record.JobID, firstCause); !errors.Is(err, firstCause) {
		t.Fatalf("markFailure() error = %v, want %v", err, firstCause)
	}
	afterFirst, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() after first failure error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() after first failure = not found")
	}
	if got := afterFirst.RetryCount; got != 1 {
		t.Fatalf("RetryCount after first failure = %d, want 1", got)
	}
	if got := afterFirst.Status; got != baldastate.ScheduledJobStatusActive {
		t.Fatalf("Status after first failure = %q, want active", got)
	}
	if got, want := afterFirst.NextRunAt, start.Add(time.Second); !got.Equal(want) {
		t.Fatalf("NextRunAt after first failure = %s, want %s", got, want)
	}
	if !strings.Contains(afterFirst.LastError, "boom one") {
		t.Fatalf("LastError after first failure = %q, want boom one", afterFirst.LastError)
	}

	clock.now = start.Add(10 * time.Second)
	secondCause := errors.New("boom two")
	if err := scheduler.markFailure(ctx, record.JobID, secondCause); !errors.Is(err, secondCause) {
		t.Fatalf("markFailure() second error = %v, want %v", err, secondCause)
	}
	afterSecond, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() after second failure error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() after second failure = not found")
	}
	if got := afterSecond.RetryCount; got != 2 {
		t.Fatalf("RetryCount after second failure = %d, want 2", got)
	}
	if got := afterSecond.Status; got != baldastate.ScheduledJobStatusPaused {
		t.Fatalf("Status after second failure = %q, want paused", got)
	}
}

func TestScheduledJobSchedulerRecordExecutionFailureDoesNotScheduleRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := baldatelegram.NewLocator(9001, 102)
	start := time.Date(2026, time.May, 14, 14, 30, 0, 0, time.UTC)
	nextRun := start.Add(30 * time.Minute)

	record := baldastate.ScheduledJobRecord{
		JobID:        "task-exec-fail",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Content:      "will fail in actor",
		ScheduleSpec: "@every 1h",
		Status:       baldastate.ScheduledJobStatusActive,
		MaxRetries:   1,
		RetryCount:   1,
		NextRunAt:    nextRun,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	clock := &schedulerClock{now: start}
	scheduler := &ScheduledJobScheduler{
		jobStore: store,
		logger:   zerolog.Nop(),
		now:      clock.Now,
	}

	cause := errors.New("actor execution failed")
	if err := scheduler.RecordExecutionFailure(ctx, record.JobID, cause); !errors.Is(err, cause) {
		t.Fatalf("RecordExecutionFailure() error = %v, want %v", err, cause)
	}
	got, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() = not found, want found")
	}
	if got.RetryCount != record.RetryCount {
		t.Fatalf("RetryCount = %d, want unchanged %d", got.RetryCount, record.RetryCount)
	}
	if !got.NextRunAt.Equal(nextRun) {
		t.Fatalf("NextRunAt = %s, want unchanged %s", got.NextRunAt, nextRun)
	}
	if got.Status != baldastate.ScheduledJobStatusActive || !strings.Contains(got.LastError, "actor execution failed") {
		t.Fatalf("task after execution failure = %+v, want active with last error", got)
	}
}

func TestScheduledJobSchedulerReconcileConfiguredTasks_UpsertsAndDeletes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	now := time.Date(2026, time.May, 14, 16, 0, 0, 0, time.UTC)
	locator := baldatelegram.NewLocator(9001, 222)

	orphaned := baldastate.ScheduledJobRecord{
		JobID:        "orphaned-task",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Content:      "remove me",
		ScheduleSpec: "@every 10s",
		Status:       baldastate.ScheduledJobStatusActive,
		MaxRetries:   3,
		NextRunAt:    now.Add(10 * time.Second),
	}
	if err := store.Upsert(ctx, orphaned); err != nil {
		t.Fatalf("Upsert(orphaned) error = %v", err)
	}

	scheduler := &ScheduledJobScheduler{
		jobStore: store,
		owner:    newOwnerStoreForTest(t, 101, 9001),
		logger:   zerolog.Nop(),
		now:      func() time.Time { return now },
		config: ScheduledJobSchedulerConfig{
			Jobs: []ConfiguredScheduledJob{
				{
					ID:      "managed-task",
					Cron:    "@every 2s",
					Target:  "alias",
					Key:     "owner",
					Content: "review queue",
					ReportTo: &ConfiguredScheduledJobTarget{
						Target: "alias",
						Key:    "owner",
					},
				},
			},
		},
	}

	if err := scheduler.reconcileConfiguredJobs(ctx); err != nil {
		t.Fatalf("reconcileConfiguredJobs() error = %v", err)
	}

	managed, ok, err := store.GetByID(ctx, "managed-task")
	if err != nil {
		t.Fatalf("GetByID(managed) error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID(managed) = not found, want found")
	}
	if got, want := managed.ScheduleSpec, "@every 2s"; got != want {
		t.Fatalf("ScheduleSpec = %q, want %q", got, want)
	}
	if got, want := managed.Content, "review queue"; got != want {
		t.Fatalf("Content = %q, want %q", got, want)
	}
	if !managed.ReportToEnabled {
		t.Fatal("ReportToEnabled = false, want true")
	}
	if got, want := managed.ReportToAddressKey, "9001:0"; got != want {
		t.Fatalf("ReportToAddressKey = %q, want %q", got, want)
	}
	if got, want := managed.Status, baldastate.ScheduledJobStatusActive; got != want {
		t.Fatalf("Status = %q, want %q", got, want)
	}
	if got, want := managed.MaxRetries, defaultSchedulerMaxRetries; got != want {
		t.Fatalf("MaxRetries = %d, want %d", got, want)
	}
	if got, want := managed.NextRunAt, now.Add(2*time.Second); !got.Equal(want) {
		t.Fatalf("NextRunAt = %s, want %s", got, want)
	}

	_, orphanedExists, err := store.GetByID(ctx, orphaned.JobID)
	if err != nil {
		t.Fatalf("GetByID(orphaned) error = %v", err)
	}
	if orphanedExists {
		t.Fatal("orphaned task still exists after reconcile")
	}
}

func TestNextRunAtFromSpec_ParsesCronExpression(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 14, 16, 3, 10, 0, time.UTC)
	nextRunAt, err := nextRunAtFromSpec("*/5 * * * *", now)
	if err != nil {
		t.Fatalf("nextRunAtFromSpec() error = %v", err)
	}
	want := time.Date(2026, time.May, 14, 16, 5, 0, 0, time.UTC)
	if !nextRunAt.Equal(want) {
		t.Fatalf("nextRunAt = %s, want %s", nextRunAt, want)
	}
}

func TestNormalizeScheduledJobSchedulerConfig_RequiresEnvelopeTarget(t *testing.T) {
	t.Parallel()

	_, err := normalizeScheduledJobSchedulerConfig(ScheduledJobSchedulerConfig{
		Jobs: []ConfiguredScheduledJob{
			{
				ID:      "task-1",
				Cron:    "@every 1m",
				Content: "check",
			},
		},
	})
	if err == nil {
		t.Fatal("normalizeScheduledJobSchedulerConfig() error = nil, want missing target")
	}
	if !strings.Contains(err.Error(), "envelope.target") {
		t.Fatalf("normalizeScheduledJobSchedulerConfig() error = %v, want envelope.target", err)
	}
}

func TestNormalizeScheduledJobSchedulerConfig_TrimsEnvelope(t *testing.T) {
	t.Parallel()

	got, err := normalizeScheduledJobSchedulerConfig(ScheduledJobSchedulerConfig{
		Jobs: []ConfiguredScheduledJob{
			{
				ID:      " task-1 ",
				Cron:    " @every 1m ",
				Target:  " alias ",
				Key:     " owner ",
				Content: " check ",
				ReportTo: &ConfiguredScheduledJobTarget{
					Target: " alias ",
					Key:    " owner ",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeScheduledJobSchedulerConfig() error = %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("tasks = %d, want 1", len(got.Jobs))
	}
	task := got.Jobs[0]
	if task.ID != "task-1" || task.Target != "alias" || task.Key != "owner" || task.Content != "check" {
		t.Fatalf("task = %+v, want trimmed envelope", task)
	}
	if task.ReportTo == nil || task.ReportTo.Target != "alias" || task.ReportTo.Key != "owner" {
		t.Fatalf("report_to = %+v, want trimmed alias/owner", task.ReportTo)
	}
}

type schedulerClock struct {
	now time.Time
}

func (c *schedulerClock) Now() time.Time {
	return c.now
}

func newSchedulerForTest(
	t *testing.T,
	store baldastate.ScheduledJobStore,
	bus *recordingHandlerCommandBus,
	now time.Time,
) *ScheduledJobScheduler {
	t.Helper()
	if bus == nil {
		bus = &recordingHandlerCommandBus{}
	}
	return &ScheduledJobScheduler{
		jobStore:     store,
		dispatcher:   bus,
		owner:        newOwnerStoreForTest(t, 101, 9001),
		logger:       zerolog.Nop(),
		pollInterval: defaultSchedulerPollInterval,
		dueBatchSize: defaultSchedulerDueBatchSize,
		now:          func() time.Time { return now },
	}
}

func newOwnerStoreForTest(t *testing.T, userID int64, chatID int64) *auth.OwnerStore {
	t.Helper()

	store, err := auth.NewOwnerStore(&fakeOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := store.RegisterOwner(userID, chatID); err != nil {
		t.Fatalf("RegisterOwner() error = %v", err)
	}
	return store
}

func newSchedulerJobStore(t *testing.T) baldastate.ScheduledJobStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "state.db")
	provider, err := baldastate.NewSQLiteProvider(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
	})
	return provider.ScheduledJobs()
}
