package handlers

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	relaytelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	relaysession "github.com/normahq/balda/internal/apps/balda/session"
	relaystate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"google.golang.org/adk/runner"
)

func TestJobSchedulerDispatchJob_EnqueuesAndReschedules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := relaytelegram.NewLocator(9001, 77)
	now := time.Date(2026, time.May, 14, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Second)

	record := relaystate.ScheduledJobRecord{
		JobID:        "job-1",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Prompt:       "summarize repo health",
		ScheduleSpec: "@every 2s",
		Status:       relaystate.ScheduledJobStatusActive,
		MaxRetries:   3,
		NextRunAt:    dueAt,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	ts := newSchedulerTopicSession(t, locator, "tg-101", "adk-1", nil)
	sessions := &fakeSchedulerSessionManager{session: ts}
	queue := &fakeSchedulerTurnQueue{}
	channel := &fakeSchedulerChannel{}
	scheduler := newSchedulerForTest(store, sessions, queue, channel, now)

	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() error = %v", err)
	}
	if got := len(queue.tasks); got != 1 {
		t.Fatalf("queued tasks = %d, want 1", got)
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
	wantKey := dispatchAttemptKey(record.JobID, dueAt)
	if got := updated.LastDispatchKey; got != wantKey {
		t.Fatalf("LastDispatchKey = %q, want %q", got, wantKey)
	}
	if got := updated.RetryCount; got != 0 {
		t.Fatalf("RetryCount = %d, want 0", got)
	}
}

func TestJobSchedulerDispatchJob_RestoresSessionWhenNotInMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := relaytelegram.NewLocator(9001, 88)
	now := time.Date(2026, time.May, 14, 12, 30, 0, 0, time.UTC)

	record := relaystate.ScheduledJobRecord{
		JobID:        "job-restore",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Prompt:       "restore and run",
		ScheduleSpec: "@every 10s",
		Status:       relaystate.ScheduledJobStatusActive,
		NextRunAt:    now.Add(-2 * time.Second),
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	restoreTS := newSchedulerTopicSession(t, locator, "tg-202", "adk-2", nil)
	sessions := &fakeSchedulerSessionManager{
		getErr:  errors.New("not found"),
		info:    relaysession.TopicSessionInfo{UserID: "tg-202"},
		restore: restoreTS,
	}
	queue := &fakeSchedulerTurnQueue{}
	scheduler := newSchedulerForTest(store, sessions, queue, &fakeSchedulerChannel{}, now)

	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() error = %v", err)
	}
	if got := sessions.restoreCalls; got != 1 {
		t.Fatalf("restore calls = %d, want 1", got)
	}
	if got := len(queue.tasks); got != 1 {
		t.Fatalf("queued tasks = %d, want 1", got)
	}
}

func TestJobSchedulerDispatchJob_IdempotentForSameDueSlot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := relaytelegram.NewLocator(9001, 99)
	now := time.Date(2026, time.May, 14, 13, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Second)

	record := relaystate.ScheduledJobRecord{
		JobID:        "job-idempotent",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Prompt:       "same slot should dispatch once",
		ScheduleSpec: "@every 5s",
		Status:       relaystate.ScheduledJobStatusActive,
		NextRunAt:    dueAt,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	sessions := &fakeSchedulerSessionManager{
		session: newSchedulerTopicSession(t, locator, "tg-303", "adk-3", nil),
	}
	queue := &fakeSchedulerTurnQueue{}
	scheduler := newSchedulerForTest(store, sessions, queue, &fakeSchedulerChannel{}, now)

	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() first call error = %v", err)
	}
	if err := scheduler.dispatchJob(ctx, record, now); err != nil {
		t.Fatalf("dispatchJob() second call error = %v", err)
	}
	if got := len(queue.tasks); got != 1 {
		t.Fatalf("queued tasks after duplicate dispatch = %d, want 1", got)
	}

	updated, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() = not found, want found")
	}
	if got, want := updated.LastDispatchKey, dispatchAttemptKey(record.JobID, dueAt); got != want {
		t.Fatalf("LastDispatchKey = %q, want %q", got, want)
	}
}

func TestJobSchedulerMarkFailure_RetryThenPause(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := relaytelegram.NewLocator(9001, 101)
	start := time.Date(2026, time.May, 14, 14, 0, 0, 0, time.UTC)

	record := relaystate.ScheduledJobRecord{
		JobID:        "job-fail",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Prompt:       "will fail",
		ScheduleSpec: "@every 1m",
		Status:       relaystate.ScheduledJobStatusActive,
		MaxRetries:   1,
		NextRunAt:    start,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	clock := &schedulerClock{now: start}
	scheduler := &JobScheduler{
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
	if got := afterFirst.Status; got != relaystate.ScheduledJobStatusActive {
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
	if got := afterSecond.Status; got != relaystate.ScheduledJobStatusPaused {
		t.Fatalf("Status after second failure = %q, want paused", got)
	}
}

func TestJobSchedulerReconcileConfiguredJobs_UpsertsAndDeletes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	now := time.Date(2026, time.May, 14, 16, 0, 0, 0, time.UTC)
	locator := relaytelegram.NewLocator(9001, 222)

	stale := relaystate.ScheduledJobRecord{
		JobID:        "stale-job",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Prompt:       "remove me",
		ScheduleSpec: "@every 10s",
		Status:       relaystate.ScheduledJobStatusActive,
		MaxRetries:   3,
		NextRunAt:    now.Add(10 * time.Second),
	}
	if err := store.Upsert(ctx, stale); err != nil {
		t.Fatalf("Upsert(stale) error = %v", err)
	}

	scheduler := &JobScheduler{
		jobStore: store,
		logger:   zerolog.Nop(),
		now:      func() time.Time { return now },
		config: JobSchedulerConfig{
			LocatorAliases: map[string]JobLocatorAlias{
				"owner": {
					ChannelType: locator.ChannelType,
					AddressKey:  locator.AddressKey,
					AddressJSON: locator.AddressJSON,
					SessionID:   locator.SessionID,
				},
			},
			Jobs: []ConfiguredScheduledJob{
				{
					ID:     "managed-job",
					Alias:  "owner",
					Cron:   "@every 2s",
					Prompt: "review queue",
				},
			},
		},
	}

	if err := scheduler.reconcileConfiguredJobs(ctx); err != nil {
		t.Fatalf("reconcileConfiguredJobs() error = %v", err)
	}

	managed, ok, err := store.GetByID(ctx, "managed-job")
	if err != nil {
		t.Fatalf("GetByID(managed) error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID(managed) = not found, want found")
	}
	if got, want := managed.ScheduleSpec, "@every 2s"; got != want {
		t.Fatalf("ScheduleSpec = %q, want %q", got, want)
	}
	if got, want := managed.Prompt, "review queue"; got != want {
		t.Fatalf("Prompt = %q, want %q", got, want)
	}
	if got, want := managed.Status, relaystate.ScheduledJobStatusActive; got != want {
		t.Fatalf("Status = %q, want %q", got, want)
	}
	if got, want := managed.MaxRetries, defaultSchedulerMaxRetries; got != want {
		t.Fatalf("MaxRetries = %d, want %d", got, want)
	}
	if got, want := managed.NextRunAt, now.Add(2*time.Second); !got.Equal(want) {
		t.Fatalf("NextRunAt = %s, want %s", got, want)
	}

	_, staleExists, err := store.GetByID(ctx, stale.JobID)
	if err != nil {
		t.Fatalf("GetByID(stale) error = %v", err)
	}
	if staleExists {
		t.Fatal("stale job still exists after reconcile")
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

func TestNormalizeJobSchedulerConfig_RejectsMissingAlias(t *testing.T) {
	t.Parallel()

	_, err := normalizeJobSchedulerConfig(JobSchedulerConfig{
		Jobs: []ConfiguredScheduledJob{
			{
				ID:     "job-1",
				Alias:  "missing",
				Cron:   "@every 1m",
				Prompt: "check",
			},
		},
	})
	if err == nil {
		t.Fatal("normalizeJobSchedulerConfig() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "references undefined balda.locators key") {
		t.Fatalf("normalizeJobSchedulerConfig() error = %v", err)
	}
}

func TestJobSchedulerExecuteJobTurn_SuccessResetsRetryAndSendsReply(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSchedulerJobStore(t)
	locator := relaytelegram.NewLocator(9001, 111)
	now := time.Date(2026, time.May, 14, 15, 0, 0, 0, time.UTC)

	record := relaystate.ScheduledJobRecord{
		JobID:        "job-success",
		SessionID:    locator.SessionID,
		ChannelType:  locator.ChannelType,
		AddressKey:   locator.AddressKey,
		AddressJSON:  locator.AddressJSON,
		Prompt:       "run once",
		ScheduleSpec: "@every 30s",
		Status:       relaystate.ScheduledJobStatusActive,
		RetryCount:   2,
		NextRunAt:    now.Add(30 * time.Second),
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	adkRunner, adkSessionID := newRelayRunTurnTestRunner(t)
	ts := newSchedulerTopicSession(t, locator, "tg-101", adkSessionID, adkRunner)
	channel := &fakeSchedulerChannel{}
	scheduler := &JobScheduler{
		jobStore: store,
		channel:  channel,
		logger:   zerolog.Nop(),
		now: func() time.Time {
			return now
		},
	}

	if err := scheduler.executeJobTurn(ctx, locator, record.JobID, "ship it", ts); err != nil {
		t.Fatalf("executeJobTurn() error = %v", err)
	}
	if got := len(channel.agentReplies); got != 1 {
		t.Fatalf("agent reply sends = %d, want 1", got)
	}

	updated, ok, err := store.GetByID(ctx, record.JobID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() = not found")
	}
	if got := updated.RetryCount; got != 0 {
		t.Fatalf("RetryCount after success = %d, want 0", got)
	}
	if got := updated.Status; got != relaystate.ScheduledJobStatusActive {
		t.Fatalf("Status after success = %q, want active", got)
	}
	if got := updated.LastRunAt; !got.Equal(now) {
		t.Fatalf("LastRunAt after success = %s, want %s", got, now)
	}
}

type fakeSchedulerSessionManager struct {
	session      *relaysession.TopicSession
	getErr       error
	info         relaysession.TopicSessionInfo
	infoErr      error
	restore      *relaysession.TopicSession
	restoreErr   error
	restoreCalls int
}

func (f *fakeSchedulerSessionManager) GetSession(_ relaysession.SessionLocator) (*relaysession.TopicSession, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.session == nil {
		return nil, errors.New("session not found")
	}
	return f.session, nil
}

func (f *fakeSchedulerSessionManager) GetSessionInfo(_ context.Context, _ string) (relaysession.TopicSessionInfo, error) {
	if f.infoErr != nil {
		return relaysession.TopicSessionInfo{}, f.infoErr
	}
	return f.info, nil
}

func (f *fakeSchedulerSessionManager) RestoreSession(_ context.Context, _ relaysession.SessionContext) (*relaysession.TopicSession, error) {
	f.restoreCalls++
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	if f.restore == nil {
		return nil, errors.New("restore unavailable")
	}
	return f.restore, nil
}

type fakeSchedulerTurnQueue struct {
	tasks []TurnTask
}

func (f *fakeSchedulerTurnQueue) Enqueue(task TurnTask) (int, error) {
	f.tasks = append(f.tasks, task)
	return len(f.tasks) - 1, nil
}

func (*fakeSchedulerTurnQueue) CancelSession(relaysession.SessionLocator, bool) (bool, int, error) {
	return false, 0, nil
}

type fakeSchedulerChannel struct {
	plainTexts   []string
	agentReplies []string
}

func (f *fakeSchedulerChannel) SendPlain(_ context.Context, _ relaysession.SessionLocator, text string) error {
	f.plainTexts = append(f.plainTexts, text)
	return nil
}

func (f *fakeSchedulerChannel) SendAgentReply(_ context.Context, _ relaysession.SessionLocator, text string) error {
	f.agentReplies = append(f.agentReplies, text)
	return nil
}

type schedulerClock struct {
	now time.Time
}

func (c *schedulerClock) Now() time.Time {
	return c.now
}

func newSchedulerForTest(
	store relaystate.ScheduledJobStore,
	sessions schedulerSessionManager,
	queue turnQueue,
	channel schedulerChannel,
	now time.Time,
) *JobScheduler {
	return &JobScheduler{
		jobStore:     store,
		sessions:     sessions,
		dispatch:     queue,
		channel:      channel,
		logger:       zerolog.Nop(),
		pollInterval: defaultSchedulerPollInterval,
		dueBatchSize: defaultSchedulerDueBatchSize,
		now:          func() time.Time { return now },
	}
}

func newSchedulerJobStore(t *testing.T) relaystate.ScheduledJobStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "balda.db")
	provider, err := relaystate.NewSQLiteProvider(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
	})
	return provider.ScheduledJobs()
}

func newSchedulerTopicSession(
	t *testing.T,
	locator relaysession.SessionLocator,
	userID string,
	agentSessionID string,
	adkRunner *runner.Runner,
) *relaysession.TopicSession {
	t.Helper()

	ts := &relaysession.TopicSession{}
	setUnexportedField(t, ts, "sessionID", locator.SessionID)
	setUnexportedField(t, ts, "locator", locator)
	setUnexportedField(t, ts, "userID", userID)
	setUnexportedField(t, ts, "agentSessionID", agentSessionID)
	setUnexportedField(t, ts, "runner", adkRunner)
	return ts
}
