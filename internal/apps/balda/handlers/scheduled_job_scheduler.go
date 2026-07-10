package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/normahq/balda/internal/apps/balda/actors"
	"github.com/normahq/balda/internal/apps/balda/auth"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	defaultSchedulerPollInterval = 2 * time.Second
	defaultSchedulerDueBatchSize = 100
	defaultSchedulerMaxRetries   = 3
)

// ConfiguredScheduledJob defines a startup-managed recurring job.
type ConfiguredScheduledJob struct {
	ID       string
	Cron     string
	Target   string
	Key      string
	Content  string
	ReportTo *ConfiguredScheduledJobTarget
}

// ConfiguredScheduledJobTarget is a configured scheduler envelope address.
type ConfiguredScheduledJobTarget struct {
	Target string
	Key    string
}

// ScheduledJobSchedulerConfig controls startup job reconciliation.
type ScheduledJobSchedulerConfig struct {
	Jobs []ConfiguredScheduledJob
}

type scheduledJobSchedulerParams struct {
	fx.In

	JobStore   baldastate.ScheduledJobStore
	Dispatcher actortransport.Dispatcher
	OwnerStore *auth.OwnerStore
	Logger     zerolog.Logger
	Config     ScheduledJobSchedulerConfig
}

// ScheduledJobScheduler publishes due locator-bound recurring jobs as durable job commands.
type ScheduledJobScheduler struct {
	jobStore   baldastate.ScheduledJobStore
	dispatcher actortransport.Dispatcher
	owner      *auth.OwnerStore
	logger     zerolog.Logger
	config     ScheduledJobSchedulerConfig

	pollInterval time.Duration
	dueBatchSize int
	now          func() time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Start reconciles configured jobs before accepting scheduler work.
func (s *ScheduledJobScheduler) Start(ctx context.Context) error {
	if err := s.reconcileConfiguredJobs(ctx); err != nil {
		return err
	}
	s.start()
	return nil
}

// Stop waits for the scheduler loop to terminate.
func (s *ScheduledJobScheduler) Stop(ctx context.Context) error {
	return s.stop(ctx)
}

func (s *ScheduledJobScheduler) start() {
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

func (s *ScheduledJobScheduler) stop(ctx context.Context) error {
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

func (s *ScheduledJobScheduler) reconcileConfiguredJobs(ctx context.Context) error {
	desired := make(map[string]struct{}, len(s.config.Jobs))
	now := s.now().UTC()

	for _, job := range s.config.Jobs {
		target, err := resolveEnvelopeTarget(ctx, s.owner, envelopeTarget{Target: job.Target, Key: job.Key})
		if err != nil {
			return fmt.Errorf("resolve scheduler job %q target: %w", job.ID, err)
		}
		var reportTo *resolvedEnvelopeTarget
		if job.ReportTo != nil {
			resolved, err := resolveEnvelopeTarget(ctx, s.owner, envelopeTarget{Target: job.ReportTo.Target, Key: job.ReportTo.Key})
			if err != nil {
				return fmt.Errorf("resolve scheduler job %q report_to: %w", job.ID, err)
			}
			reportTo = &resolved
		}
		nextRunAt, err := nextRunAtFromSpec(job.Cron, now)
		if err != nil {
			return fmt.Errorf("compute next run for scheduler job %q: %w", job.ID, err)
		}
		record := baldastate.ScheduledJobRecord{
			JobID:        job.ID,
			SessionID:    target.Locator.SessionID,
			ChannelType:  target.Locator.ChannelType,
			AddressKey:   target.Locator.AddressKey,
			AddressJSON:  target.Locator.AddressJSON,
			Content:      job.Content,
			ScheduleSpec: job.Cron,
			Timezone:     "UTC",
			Status:       baldastate.ScheduledJobStatusActive,
			MaxRetries:   defaultSchedulerMaxRetries,
			RetryCount:   0,
			NextRunAt:    nextRunAt,
		}
		if reportTo != nil {
			record.ReportToEnabled = true
			record.ReportToSessionID = reportTo.Locator.SessionID
			record.ReportToChannelType = reportTo.Locator.ChannelType
			record.ReportToAddressKey = reportTo.Locator.AddressKey
			record.ReportToAddressJSON = reportTo.Locator.AddressJSON
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

func (s *ScheduledJobScheduler) dispatchDue(ctx context.Context, now time.Time) error {
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

func (s *ScheduledJobScheduler) dispatchJob(ctx context.Context, job baldastate.ScheduledJobRecord, now time.Time) error {
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
	if strings.TrimSpace(current.Status) != baldastate.ScheduledJobStatusActive {
		return nil
	}
	if current.NextRunAt.After(now.UTC()) {
		return nil
	}

	dispatchKey := fmt.Sprintf("%s@%s", jobID, current.NextRunAt.UTC().Format(time.RFC3339Nano))
	if strings.TrimSpace(current.LastDispatchKey) == dispatchKey {
		return nil
	}

	target, err := s.resolveScheduledJobTarget(ctx, current)
	if err != nil {
		return s.markFailure(ctx, jobID, fmt.Errorf("resolve scheduler target: %w", err))
	}
	locator := target.Locator

	nextRunAt, err := nextRunAtFromSpec(current.ScheduleSpec, now)
	if err != nil {
		return s.markFailure(ctx, jobID, fmt.Errorf("invalid schedule_spec: %w", err))
	}

	content := strings.TrimSpace(current.Content)
	if err := s.dispatchScheduledJob(ctx, current, target, content, dispatchKey); err != nil {
		return err
	}

	// Mark the slot only after durable command dispatch succeeds.
	current.LastDispatchKey = dispatchKey
	current.LastError = ""
	current.Status = baldastate.ScheduledJobStatusActive
	current.NextRunAt = nextRunAt
	current.SessionID = locator.SessionID
	current.ChannelType = locator.ChannelType
	current.AddressKey = locator.AddressKey
	current.AddressJSON = locator.AddressJSON
	if err := s.jobStore.Upsert(ctx, current); err != nil {
		return fmt.Errorf("update scheduled job %q after publish: %w", jobID, err)
	}

	return nil
}

func (s *ScheduledJobScheduler) resolveScheduledJobTarget(ctx context.Context, job baldastate.ScheduledJobRecord) (resolvedEnvelopeTarget, error) {
	locator, err := baldasession.NewSessionLocator(job.ChannelType, job.AddressKey, job.AddressJSON, job.SessionID)
	if err != nil {
		return resolveEnvelopeTarget(ctx, s.owner, envelopeTarget{Target: envelopeTargetAlias, Key: envelopeAliasOwner})
	}
	target := resolvedEnvelopeTarget{Locator: locator}
	if address, ok, decodeErr := baldatelegram.DecodeLocator(locator); decodeErr != nil {
		return resolvedEnvelopeTarget{}, decodeErr
	} else if ok {
		target.TopicID = address.TopicID
	}
	if s.owner != nil {
		if owner := s.owner.GetOwner(); owner != nil && owner.UserID != 0 {
			target.UserID = baldatelegram.UserID(owner.UserID)
		}
	}
	return target, nil
}

func (s *ScheduledJobScheduler) dispatchScheduledJob(
	ctx context.Context,
	job baldastate.ScheduledJobRecord,
	target resolvedEnvelopeTarget,
	content string,
	dispatchKey string,
) error {
	var reportTo *baldasession.SessionLocator
	if job.ReportToEnabled {
		locator, err := baldasession.NewSessionLocator(job.ReportToChannelType, job.ReportToAddressKey, job.ReportToAddressJSON, job.ReportToSessionID)
		if err != nil {
			return s.markFailure(ctx, job.JobID, fmt.Errorf("resolve report_to locator: %w", err))
		}
		reportTo = &locator
	}
	env, err := actors.ScheduledJobEnvelope(job.JobID, content, target.Locator, reportTo, target.UserID, target.TopicID, dispatchKey)
	if err != nil {
		return s.markFailure(ctx, job.JobID, err)
	}
	if _, err := s.dispatcher.Dispatch(ctx, env); err != nil {
		return s.markFailure(ctx, job.JobID, fmt.Errorf("publish scheduled job command: %w", err))
	}
	return nil
}

func (s *ScheduledJobScheduler) MarkSuccess(ctx context.Context, jobID string) error {
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
	job.Status = baldastate.ScheduledJobStatusActive
	if err := s.jobStore.Upsert(ctx, job); err != nil {
		return fmt.Errorf("upsert scheduled job %q: %w", jobID, err)
	}
	return nil
}

func (s *ScheduledJobScheduler) markFailure(ctx context.Context, jobID string, cause error) error {
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
		job.Status = baldastate.ScheduledJobStatusPaused
	} else {
		job.Status = baldastate.ScheduledJobStatusActive
		retryCount := job.RetryCount
		if retryCount < 1 {
			retryCount = 1
		}
		delay := time.Duration(retryCount) * time.Second
		if delay > 60*time.Second {
			delay = 60 * time.Second
		}
		job.NextRunAt = now.Add(delay)
	}
	if err := s.jobStore.Upsert(ctx, job); err != nil {
		return fmt.Errorf("upsert scheduled job %q: %w", jobID, err)
	}
	return cause
}

func (s *ScheduledJobScheduler) RecordExecutionFailure(ctx context.Context, jobID string, cause error) error {
	job, ok, err := s.jobStore.GetByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load scheduled job %q: %w", jobID, err)
	}
	if !ok {
		return fmt.Errorf("scheduled job %q not found", jobID)
	}
	now := s.now().UTC()
	job.LastError = strings.TrimSpace(cause.Error())
	job.LastRunAt = now
	job.Status = baldastate.ScheduledJobStatusActive
	if err := s.jobStore.Upsert(ctx, job); err != nil {
		return fmt.Errorf("upsert scheduled job %q execution failure: %w", jobID, err)
	}
	return cause
}

func normalizeScheduledJobSchedulerConfig(raw ScheduledJobSchedulerConfig) (ScheduledJobSchedulerConfig, error) {
	cfg := ScheduledJobSchedulerConfig{
		Jobs: make([]ConfiguredScheduledJob, 0, len(raw.Jobs)),
	}

	seenJobIDs := make(map[string]struct{}, len(raw.Jobs))
	for idx, rawJob := range raw.Jobs {
		jobID := strings.TrimSpace(rawJob.ID)
		if jobID == "" {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].id is required", idx)
		}
		if _, exists := seenJobIDs[jobID]; exists {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("duplicate balda.scheduler.jobs id %q", jobID)
		}
		seenJobIDs[jobID] = struct{}{}

		cronSpec := strings.TrimSpace(rawJob.Cron)
		if cronSpec == "" {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].cron is required", idx)
		}
		if _, err := parseScheduleSpec(cronSpec); err != nil {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("invalid balda.scheduler.jobs[%d].cron: %w", idx, err)
		}

		target := strings.TrimSpace(rawJob.Target)
		if target == "" {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].envelope.target is required", idx)
		}
		key := strings.TrimSpace(rawJob.Key)
		if key == "" {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].envelope.key is required", idx)
		}
		content := strings.TrimSpace(rawJob.Content)
		if content == "" {
			return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].envelope.content is required", idx)
		}
		var reportTo *ConfiguredScheduledJobTarget
		if rawJob.ReportTo != nil {
			reportTo = &ConfiguredScheduledJobTarget{
				Target: strings.TrimSpace(rawJob.ReportTo.Target),
				Key:    strings.TrimSpace(rawJob.ReportTo.Key),
			}
			if reportTo.Target == "" {
				return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].envelope.report_to.target is required", idx)
			}
			if reportTo.Key == "" {
				return ScheduledJobSchedulerConfig{}, fmt.Errorf("balda.scheduler.jobs[%d].envelope.report_to.key is required", idx)
			}
		}

		cfg.Jobs = append(cfg.Jobs, ConfiguredScheduledJob{
			ID:       jobID,
			Cron:     cronSpec,
			Target:   target,
			Key:      key,
			Content:  content,
			ReportTo: reportTo,
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
