package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	relaytelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	relaysession "github.com/normahq/balda/internal/apps/balda/session"
	relaystate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	defaultSchedulerPollInterval = 2 * time.Second
	defaultSchedulerDueBatchSize = 100
	defaultSchedulerMaxRetries   = 3
)

// JobLocatorAlias defines a config alias to a canonical session locator.
type JobLocatorAlias struct {
	ChannelType string
	AddressKey  string
	AddressJSON string
	SessionID   string
}

// ConfiguredScheduledJob defines a startup-managed recurring job.
type ConfiguredScheduledJob struct {
	ID     string
	Alias  string
	Cron   string
	Prompt string
}

// JobSchedulerConfig controls startup job reconciliation.
type JobSchedulerConfig struct {
	LocatorAliases map[string]JobLocatorAlias
	Jobs           []ConfiguredScheduledJob
}

type schedulerSessionManager interface {
	GetSession(locator relaysession.SessionLocator) (*relaysession.TopicSession, error)
	GetSessionInfo(ctx context.Context, sessionID string) (relaysession.TopicSessionInfo, error)
	RestoreSession(ctx context.Context, sessionCtx relaysession.SessionContext) (*relaysession.TopicSession, error)
}

type schedulerChannel interface {
	SendPlain(ctx context.Context, locator relaysession.SessionLocator, text string) error
	SendAgentReply(ctx context.Context, locator relaysession.SessionLocator, text string) error
}

type jobSchedulerParams struct {
	fx.In

	LC             fx.Lifecycle
	StateProvider  relaystate.Provider
	SessionManager *relaysession.Manager
	TurnDispatcher *TurnDispatcher
	Channel        *relaytelegram.Adapter
	Logger         zerolog.Logger
	Config         JobSchedulerConfig
}

// JobScheduler dispatches due locator-bound recurring jobs into the turn queue.
type JobScheduler struct {
	jobStore relaystate.ScheduledJobStore
	sessions schedulerSessionManager
	dispatch turnQueue
	channel  schedulerChannel
	logger   zerolog.Logger
	config   JobSchedulerConfig

	pollInterval time.Duration
	dueBatchSize int
	now          func() time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewJobScheduler(params jobSchedulerParams) (*JobScheduler, error) {
	if params.StateProvider == nil {
		return nil, fmt.Errorf("balda state provider is required")
	}
	if params.SessionManager == nil {
		return nil, fmt.Errorf("balda session manager is required")
	}
	if params.TurnDispatcher == nil {
		return nil, fmt.Errorf("balda turn dispatcher is required")
	}
	if params.Channel == nil {
		return nil, fmt.Errorf("balda channel adapter is required")
	}
	config, err := normalizeJobSchedulerConfig(params.Config)
	if err != nil {
		return nil, err
	}

	scheduler := &JobScheduler{
		jobStore:     params.StateProvider.ScheduledJobs(),
		sessions:     params.SessionManager,
		dispatch:     params.TurnDispatcher,
		channel:      params.Channel,
		logger:       params.Logger.With().Str("component", "balda.job_scheduler").Logger(),
		config:       config,
		pollInterval: defaultSchedulerPollInterval,
		dueBatchSize: defaultSchedulerDueBatchSize,
		now:          time.Now,
	}

	params.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := scheduler.reconcileConfiguredJobs(ctx); err != nil {
				return err
			}
			scheduler.start()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return scheduler.stop(ctx)
		},
	})

	return scheduler, nil
}

func (s *JobScheduler) start() {
	if s.cancel != nil {
		return
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ticker := time.NewTicker(s.pollInterval)
		defer ticker.Stop()

		for {
			if err := s.dispatchDue(runCtx, s.now().UTC()); err != nil {
				s.logger.Warn().Err(err).Msg("failed to dispatch due jobs")
			}

			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *JobScheduler) stop(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.wg.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *JobScheduler) reconcileConfiguredJobs(ctx context.Context) error {
	desired := make(map[string]struct{}, len(s.config.Jobs))
	now := s.now().UTC()

	for _, job := range s.config.Jobs {
		alias := s.config.LocatorAliases[job.Alias]
		nextRunAt, err := nextRunAtFromSpec(job.Cron, now)
		if err != nil {
			return fmt.Errorf("compute next run for scheduler job %q: %w", job.ID, err)
		}
		record := relaystate.ScheduledJobRecord{
			JobID:        job.ID,
			SessionID:    alias.SessionID,
			ChannelType:  alias.ChannelType,
			AddressKey:   alias.AddressKey,
			AddressJSON:  alias.AddressJSON,
			Prompt:       job.Prompt,
			ScheduleSpec: job.Cron,
			Timezone:     "UTC",
			Status:       relaystate.ScheduledJobStatusActive,
			MaxRetries:   defaultSchedulerMaxRetries,
			RetryCount:   0,
			NextRunAt:    nextRunAt,
		}
		if err := s.jobStore.Upsert(ctx, record); err != nil {
			return fmt.Errorf("upsert scheduler job %q: %w", job.ID, err)
		}
		desired[job.ID] = struct{}{}
	}

	currentJobs, err := s.jobStore.List(ctx)
	if err != nil {
		return fmt.Errorf("list persisted scheduler jobs: %w", err)
	}
	for _, existing := range currentJobs {
		jobID := strings.TrimSpace(existing.JobID)
		if _, ok := desired[jobID]; ok {
			continue
		}
		if err := s.jobStore.Delete(ctx, jobID); err != nil {
			return fmt.Errorf("delete unmanaged scheduler job %q: %w", jobID, err)
		}
	}

	return nil
}

func (s *JobScheduler) dispatchDue(ctx context.Context, now time.Time) error {
	due, err := s.jobStore.ListDue(ctx, now, s.dueBatchSize)
	if err != nil {
		return fmt.Errorf("list due jobs: %w", err)
	}

	for _, job := range due {
		if err := s.dispatchJob(ctx, job, now); err != nil {
			s.logger.Warn().Err(err).Str("job_id", job.JobID).Msg("failed to dispatch job")
		}
	}
	return nil
}

func (s *JobScheduler) dispatchJob(ctx context.Context, job relaystate.ScheduledJobRecord, now time.Time) error {
	jobID := strings.TrimSpace(job.JobID)
	if jobID == "" {
		return fmt.Errorf("job id is required")
	}

	current, ok, err := s.jobStore.GetByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load scheduled job %q: %w", jobID, err)
	}
	if !ok {
		return fmt.Errorf("scheduled job %q not found", jobID)
	}
	if strings.TrimSpace(current.Status) != relaystate.ScheduledJobStatusActive {
		return nil
	}
	if current.NextRunAt.After(now.UTC()) {
		return nil
	}

	dispatchKey := dispatchAttemptKey(jobID, current.NextRunAt)
	if strings.TrimSpace(current.LastDispatchKey) == dispatchKey {
		return nil
	}

	locator, err := relaysession.NewSessionLocator(current.ChannelType, current.AddressKey, current.AddressJSON, current.SessionID)
	if err != nil {
		return s.markFailure(ctx, jobID, fmt.Errorf("invalid job locator: %w", err))
	}

	ts, err := s.resolveTopicSession(ctx, locator, strings.TrimSpace(current.SessionID))
	if err != nil {
		return s.markFailure(ctx, jobID, fmt.Errorf("resolve session: %w", err))
	}

	nextRunAt, err := nextRunAtFromSpec(current.ScheduleSpec, now)
	if err != nil {
		return s.markFailure(ctx, jobID, fmt.Errorf("invalid schedule_spec: %w", err))
	}

	// Claim this due-slot before enqueue so duplicate stale due entries do not dispatch twice.
	current.LastDispatchKey = dispatchKey
	current.LastError = ""
	current.Status = relaystate.ScheduledJobStatusActive
	current.NextRunAt = nextRunAt
	if err := s.jobStore.Upsert(ctx, current); err != nil {
		return fmt.Errorf("update job %q before enqueue: %w", jobID, err)
	}

	prompt := strings.TrimSpace(current.Prompt)
	if _, err := s.dispatch.Enqueue(TurnTask{
		SessionID: ts.GetSessionID(),
		Run: func(runCtx context.Context) error {
			return s.executeJobTurn(runCtx, locator, jobID, prompt, ts)
		},
	}); err != nil {
		return s.markFailure(ctx, jobID, fmt.Errorf("enqueue scheduled job: %w", err))
	}

	return nil
}

func (s *JobScheduler) resolveTopicSession(
	ctx context.Context,
	locator relaysession.SessionLocator,
	sessionID string,
) (*relaysession.TopicSession, error) {
	ts, err := s.sessions.GetSession(locator)
	if err == nil {
		return ts, nil
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = strings.TrimSpace(locator.SessionID)
	}

	info, infoErr := s.sessions.GetSessionInfo(ctx, sessionID)
	if infoErr != nil {
		return nil, infoErr
	}
	userID := strings.TrimSpace(info.UserID)
	if userID == "" {
		return nil, fmt.Errorf("session %q has no user id for restore", sessionID)
	}

	return s.sessions.RestoreSession(ctx, relaysession.SessionContext{
		Locator: locator,
		UserID:  userID,
	})
}

func (s *JobScheduler) executeJobTurn(
	ctx context.Context,
	locator relaysession.SessionLocator,
	jobID string,
	prompt string,
	ts *relaysession.TopicSession,
) error {
	reply, err := runGoalIteration(ctx, ts.GetRunner(), ts.GetUserID(), ts.GetAgentSessionID(), prompt)
	if err != nil {
		_ = s.markFailure(context.Background(), jobID, fmt.Errorf("execute scheduled job: %w", err))
		_ = s.channel.SendPlain(context.Background(), locator, fmt.Sprintf("Scheduled job %s failed: %v", jobID, err))
		return err
	}

	if err := s.markSuccess(context.Background(), jobID); err != nil {
		s.logger.Warn().Err(err).Str("job_id", jobID).Msg("failed to mark scheduled job success")
	}
	if strings.TrimSpace(reply) != "" {
		return s.channel.SendAgentReply(ctx, locator, reply)
	}
	return s.channel.SendPlain(ctx, locator, fmt.Sprintf("Scheduled job %s completed.", jobID))
}

func (s *JobScheduler) markSuccess(ctx context.Context, jobID string) error {
	job, ok, err := s.jobStore.GetByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load scheduled job %q: %w", jobID, err)
	}
	if !ok {
		return fmt.Errorf("scheduled job %q not found", jobID)
	}
	job.LastRunAt = s.now().UTC()
	job.LastError = ""
	job.RetryCount = 0
	job.Status = relaystate.ScheduledJobStatusActive
	if err := s.jobStore.Upsert(ctx, job); err != nil {
		return fmt.Errorf("upsert scheduled job %q: %w", jobID, err)
	}
	return nil
}

func (s *JobScheduler) markFailure(ctx context.Context, jobID string, cause error) error {
	job, ok, err := s.jobStore.GetByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load scheduled job %q: %w", jobID, err)
	}
	if !ok {
		return fmt.Errorf("scheduled job %q not found", jobID)
	}
	now := s.now().UTC()
	job.RetryCount++
	job.LastError = strings.TrimSpace(cause.Error())
	job.LastRunAt = now

	maxRetries := job.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if job.RetryCount > maxRetries {
		job.Status = relaystate.ScheduledJobStatusPaused
	} else {
		job.Status = relaystate.ScheduledJobStatusActive
		job.NextRunAt = now.Add(retryDelay(job.RetryCount))
	}
	if err := s.jobStore.Upsert(ctx, job); err != nil {
		return fmt.Errorf("upsert scheduled job %q: %w", jobID, err)
	}
	return cause
}

func normalizeJobSchedulerConfig(raw JobSchedulerConfig) (JobSchedulerConfig, error) {
	cfg := JobSchedulerConfig{
		LocatorAliases: make(map[string]JobLocatorAlias, len(raw.LocatorAliases)),
		Jobs:           make([]ConfiguredScheduledJob, 0, len(raw.Jobs)),
	}

	for rawAlias, rawLocator := range raw.LocatorAliases {
		alias := strings.TrimSpace(rawAlias)
		if alias == "" {
			return JobSchedulerConfig{}, fmt.Errorf("balda.locators alias is required")
		}
		if _, exists := cfg.LocatorAliases[alias]; exists {
			return JobSchedulerConfig{}, fmt.Errorf("duplicate balda.locators alias %q", alias)
		}

		locator, err := relaysession.NewSessionLocator(
			strings.TrimSpace(rawLocator.ChannelType),
			strings.TrimSpace(rawLocator.AddressKey),
			strings.TrimSpace(rawLocator.AddressJSON),
			strings.TrimSpace(rawLocator.SessionID),
		)
		if err != nil {
			return JobSchedulerConfig{}, fmt.Errorf("invalid balda.locators.%s: %w", alias, err)
		}
		cfg.LocatorAliases[alias] = JobLocatorAlias{
			ChannelType: locator.ChannelType,
			AddressKey:  locator.AddressKey,
			AddressJSON: locator.AddressJSON,
			SessionID:   locator.SessionID,
		}
	}

	seenJobIDs := make(map[string]struct{}, len(raw.Jobs))
	for idx, rawJob := range raw.Jobs {
		jobID := strings.TrimSpace(rawJob.ID)
		if jobID == "" {
			return JobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].id is required", idx)
		}
		if _, exists := seenJobIDs[jobID]; exists {
			return JobSchedulerConfig{}, fmt.Errorf("duplicate balda.scheduler.jobs id %q", jobID)
		}
		seenJobIDs[jobID] = struct{}{}

		alias := strings.TrimSpace(rawJob.Alias)
		if alias == "" {
			return JobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].alias is required", idx)
		}
		if _, ok := cfg.LocatorAliases[alias]; !ok {
			return JobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].alias %q references undefined balda.locators key", idx, alias)
		}

		cronSpec := strings.TrimSpace(rawJob.Cron)
		if cronSpec == "" {
			return JobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].cron is required", idx)
		}
		if _, err := parseScheduleSpec(cronSpec); err != nil {
			return JobSchedulerConfig{}, fmt.Errorf("invalid balda.scheduler.jobs[%d].cron: %w", idx, err)
		}

		prompt := strings.TrimSpace(rawJob.Prompt)
		if prompt == "" {
			return JobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].prompt is required", idx)
		}

		cfg.Jobs = append(cfg.Jobs, ConfiguredScheduledJob{
			ID:     jobID,
			Alias:  alias,
			Cron:   cronSpec,
			Prompt: prompt,
		})
	}

	sort.Slice(cfg.Jobs, func(i, j int) bool {
		return cfg.Jobs[i].ID < cfg.Jobs[j].ID
	})
	return cfg, nil
}

func nextRunAtFromSpec(spec string, now time.Time) (time.Time, error) {
	schedule, err := parseScheduleSpec(spec)
	if err != nil {
		return time.Time{}, err
	}
	nextRunAt := schedule.Next(now.UTC())
	if nextRunAt.IsZero() {
		return time.Time{}, fmt.Errorf("schedule has no next run")
	}
	return nextRunAt.UTC(), nil
}

func parseScheduleSpec(spec string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, fmt.Errorf("schedule spec is required")
	}

	schedule, err := cron.ParseStandard(trimmed)
	if err != nil {
		return nil, fmt.Errorf("unsupported schedule spec %q", spec)
	}
	return schedule, nil
}

func retryDelay(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	delay := time.Duration(retryCount) * time.Second
	if delay > 60*time.Second {
		return 60 * time.Second
	}
	return delay
}

func dispatchAttemptKey(jobID string, dueAt time.Time) string {
	return fmt.Sprintf("%s@%s", strings.TrimSpace(jobID), dueAt.UTC().Format(time.RFC3339Nano))
}
