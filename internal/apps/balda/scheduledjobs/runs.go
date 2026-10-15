package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/google/uuid"
)

const cronPublishLease = 30 * time.Second

func (s *ScheduledJobScheduler) dispatchRecurringRun(
	ctx context.Context, job state.ScheduledJobRecord, key string, now time.Time,
) error {
	if existing, found, err := s.runStore.GetByTriggerKey(ctx, job.JobID, key); err != nil {
		return fmt.Errorf("load existing cron run: %w", err)
	} else if found {
		return s.processRun(ctx, existing, now)
	}
	selected := job
	reportRef, resolutionErr := selectScheduleReport(ctx, s.getResolver(), &selected)
	if resolutionErr != nil && !errors.Is(resolutionErr, envelopetarget.ErrDestinationUnavailable) {
		return s.markFailureForJob(ctx, job, fmt.Errorf("report destination resolution unavailable"))
	}
	payload, err := json.Marshal(selected)
	if err != nil {
		return fmt.Errorf("encode schedule run snapshot: %w", err)
	}
	run := state.ScheduleRunRecord{RunID: uuid.NewString(), ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerCron, TriggerKey: key,
		DefinitionVersion: job.DefinitionVersion, Version: 1, RequestedAt: now.UTC(),
		DueAt: job.NextRunAt, DispatchState: state.ScheduleRunPending,
		PayloadJSON: string(payload), ReportLocatorRef: reportRef}
	if resolutionErr != nil {
		run.DispatchState = state.ScheduleRunFailed
		run.SafeFailureCode = runFailureReportAliasUnavailable
		run.Attempts = 1
	}
	created, err := s.runStore.CreateCron(ctx, run, job.NextRunAt)
	if err != nil {
		return fmt.Errorf("admit cron schedule run: %w", err)
	}
	if !created {
		run, created, err = s.runStore.GetByTriggerKey(ctx, job.JobID, key)
		if err != nil {
			return fmt.Errorf("load cron schedule run: %w", err)
		}
		if !created {
			return nil
		}
	}
	return s.processRun(ctx, run, now)
}

func selectScheduleReport(ctx context.Context, resolver envelopetarget.DestinationResolver,
	job *state.ScheduledJobRecord) (string, error) {
	if job == nil || !job.ReportToEnabled {
		return "", nil
	}
	var locator deliverycmd.Locator
	if job.ReportToTargetKind != "" {
		selected, err := envelopetarget.Resolve(ctx, resolver, envelopetarget.Target{
			Target: job.ReportToTargetKind, Key: job.ReportToTargetKey,
		})
		if err != nil {
			return "", err
		}
		locator = selected.Locator
	} else {
		var err error
		locator, err = deliverycmd.NewLocator(job.ReportToChannelType,
			job.ReportToAddressKey, job.ReportToAddressJSON, job.ReportToSessionID)
		if err != nil {
			return "", err
		}
	}
	job.ReportToSessionID = locator.SessionID
	job.ReportToChannelType = locator.ChannelType
	job.ReportToAddressKey = locator.AddressKey
	job.ReportToAddressJSON = locator.AddressJSON
	return locatorref.Format(locator), nil
}

func (s *ScheduledJobScheduler) processPendingRuns(ctx context.Context, now time.Time) error {
	if s.runStore == nil {
		return nil
	}
	runs, err := s.runStore.ListPending(ctx, now, s.dueBatchSize)
	if err != nil {
		return fmt.Errorf("list pending schedule runs: %w", err)
	}
	for _, run := range runs {
		if err := s.processRun(ctx, run, now); err != nil {
			s.logger.Warn().Err(err).Str("schedule_id", run.ScheduleID).Msg("schedule run processing failed")
		}
	}
	return nil
}

func (s *ScheduledJobScheduler) processRun(ctx context.Context, run state.ScheduleRunRecord, now time.Time) error {
	if run.DispatchState == state.ScheduleRunDispatched {
		return s.settleCronRun(ctx, run, now)
	}
	if run.DispatchState == state.ScheduleRunFailed {
		if run.SafeFailureCode == runFailureReportAliasUnavailable {
			return s.settleMissingAliasCronRun(ctx, run, now)
		}
		return s.pauseFailedCronRun(ctx, run)
	}
	if run.DispatchState != state.ScheduleRunPending && run.DispatchState != state.ScheduleRunRetrying &&
		run.DispatchState != state.ScheduleRunPublishing {
		return nil
	}
	if !run.NextAttemptAt.IsZero() && run.NextAttemptAt.After(now) {
		return nil
	}
	var job state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &job); err != nil || job.JobID != run.ScheduleID ||
		job.DefinitionVersion != run.DefinitionVersion {
		return s.failRun(ctx, run, now, "invalid_snapshot", false)
	}
	if run.Trigger == state.ScheduleRunTriggerCron {
		claimed, err := s.runStore.ClaimCron(ctx, run, now, now.Add(cronPublishLease))
		if err != nil {
			return fmt.Errorf("claim cron schedule publication: %w", err)
		}
		if !claimed {
			current, found, err := s.jobStore.GetByID(ctx, run.ScheduleID)
			if err != nil {
				return fmt.Errorf("check current cron selection: %w", err)
			}
			if !found || current.Source != job.Source || current.DefinitionVersion != run.DefinitionVersion ||
				!current.NextRunAt.Equal(run.DueAt) || !current.Enabled || current.Deleted ||
				current.Status != state.ScheduledJobStatusActive || current.LastDispatchKey == run.TriggerKey {
				return s.cancelRun(ctx, run)
			}
			return nil
		}
		run.Version++
		run.DispatchState = state.ScheduleRunPublishing
		run.NextAttemptAt = now.Add(cronPublishLease)
	}
	target, err := s.resolveScheduledJobTarget(ctx, job)
	if err != nil {
		return s.failRun(ctx, run, now, "destination_unavailable", true)
	}
	var reportTo *deliverycmd.Locator
	if job.ReportToEnabled {
		locator, err := deliverycmd.NewLocator(job.ReportToChannelType,
			job.ReportToAddressKey, job.ReportToAddressJSON, job.ReportToSessionID)
		if err != nil {
			return s.failRun(ctx, run, now, "invalid_snapshot", false)
		}
		if run.ReportLocatorRef != "" && locatorref.Format(locator) != run.ReportLocatorRef {
			return s.failRun(ctx, run, now, "invalid_snapshot", false)
		}
		reportTo = &locator
	}
	env, err := turncmd.ScheduledJobEnvelope(job.JobID, strings.TrimSpace(job.Content),
		target.Locator, reportTo, target.UserID(), 0, run.TriggerKey)
	if err != nil {
		return s.failRun(ctx, run, now, "invalid_snapshot", false)
	}
	if _, err := s.dispatcher.Dispatch(ctx, env); err != nil {
		return s.failRun(ctx, run, now, "dispatch_failed", true)
	}
	run.DispatchState = state.ScheduleRunDispatched
	run.Attempts++
	run.NextAttemptAt = time.Time{}
	run.SafeFailureCode = ""
	run.DispatchedAt = now.UTC()
	run.ExecutionJobID = actorcmd.EnvelopeJobID(env)
	updated, err := s.runStore.Update(ctx, run, run.Version)
	if err != nil {
		return fmt.Errorf("record dispatched schedule run: %w", err)
	}
	if !updated {
		return nil
	}
	return s.settleCronRun(ctx, run, now)
}

func (s *ScheduledJobScheduler) settleMissingAliasCronRun(ctx context.Context,
	run state.ScheduleRunRecord, now time.Time) error {
	if run.Trigger != state.ScheduleRunTriggerCron {
		return nil
	}
	var selected state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &selected); err != nil {
		return fmt.Errorf("decode unavailable cron snapshot: %w", err)
	}
	next, err := nextRunAtFromSpec(selected.ScheduleSpec, now.UTC())
	if err != nil {
		return fmt.Errorf("compute next cron slot: %w", err)
	}
	current := selected
	current.LastDispatchKey = run.TriggerKey
	current.LastError = runFailureReportAliasUnavailable
	current.LastRunAt = run.RequestedAt
	current.Status = state.ScheduledJobStatusActive
	current.RetryCount = 0
	current.NextRunAt = next
	if _, err := s.jobStore.UpdateRuntime(ctx, runtimeUpdate(selected, current)); err != nil {
		return fmt.Errorf("settle unavailable cron slot: %w", err)
	}
	return nil
}

func (s *ScheduledJobScheduler) cancelRun(ctx context.Context, run state.ScheduleRunRecord) error {
	run.DispatchState = state.ScheduleRunCanceled
	run.NextAttemptAt = time.Time{}
	run.SafeFailureCode = "selection_changed"
	if _, err := s.runStore.Update(ctx, run, run.Version); err != nil {
		return fmt.Errorf("cancel stale cron schedule run: %w", err)
	}
	return nil
}

func (s *ScheduledJobScheduler) failRun(ctx context.Context, run state.ScheduleRunRecord,
	now time.Time, code string, retry bool) error {
	run.Attempts++
	run.SafeFailureCode = code
	if retry && run.Attempts <= defaultSchedulerMaxRetries {
		run.DispatchState = state.ScheduleRunRetrying
		run.NextAttemptAt = now.Add(time.Duration(run.Attempts) * time.Second)
	} else {
		run.DispatchState = state.ScheduleRunFailed
		run.NextAttemptAt = time.Time{}
	}
	updated, err := s.runStore.Update(ctx, run, run.Version)
	if err != nil {
		return fmt.Errorf("record failed schedule run: %w", err)
	}
	if updated && run.DispatchState == state.ScheduleRunFailed {
		return s.pauseFailedCronRun(ctx, run)
	}
	return nil
}

func (s *ScheduledJobScheduler) settleCronRun(ctx context.Context, run state.ScheduleRunRecord, now time.Time) error {
	if run.Trigger != state.ScheduleRunTriggerCron {
		return nil
	}
	var selected state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &selected); err != nil {
		return fmt.Errorf("decode cron schedule snapshot: %w", err)
	}
	next, err := nextRunAtFromSpec(selected.ScheduleSpec, now.UTC())
	if err != nil {
		return fmt.Errorf("compute next cron slot: %w", err)
	}
	current := selected
	current.LastDispatchKey = run.TriggerKey
	current.LastError = ""
	current.LastRunAt = run.DispatchedAt
	current.RetryCount = 0
	current.Status = state.ScheduledJobStatusActive
	current.NextRunAt = next
	if _, err := s.jobStore.UpdateRuntime(ctx, runtimeUpdate(selected, current)); err != nil {
		return fmt.Errorf("settle cron schedule slot: %w", err)
	}
	return nil
}

func (s *ScheduledJobScheduler) pauseFailedCronRun(ctx context.Context, run state.ScheduleRunRecord) error {
	if run.Trigger != state.ScheduleRunTriggerCron {
		return nil
	}
	var selected state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &selected); err != nil {
		return nil
	}
	current := selected
	current.Status = state.ScheduledJobStatusPaused
	current.LastError = run.SafeFailureCode
	current.RetryCount = run.Attempts
	if _, err := s.jobStore.UpdateRuntime(ctx, runtimeUpdate(selected, current)); err != nil {
		return fmt.Errorf("pause failed cron schedule slot: %w", err)
	}
	return nil
}

// RecordExecution updates recurrence diagnostics only for the run still selected.
// Legacy in-flight commands are accepted when their dispatch key remains current.
func (s *ScheduledJobScheduler) RecordExecution(ctx context.Context, jobID, dispatchKey string, cause error) error {
	key := strings.TrimSuffix(strings.TrimSpace(dispatchKey), ":session")
	if key == "" || strings.HasPrefix(key, "manual:") {
		return nil
	}
	var run state.ScheduleRunRecord
	var found bool
	if s.runStore != nil {
		var err error
		run, found, err = s.runStore.GetByTriggerKey(ctx, jobID, key)
		if err != nil {
			return fmt.Errorf("load scheduled execution run: %w", err)
		}
		if found && run.Trigger != state.ScheduleRunTriggerCron {
			return nil
		}
	}
	job, exists, err := s.jobStore.GetByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load scheduled execution definition: %w", err)
	}
	if !exists || job.Deleted || !job.Enabled || job.LastDispatchKey != key ||
		found && job.DefinitionVersion != run.DefinitionVersion {
		return nil
	}
	previous := job
	job.LastRunAt = s.now().UTC()
	job.Status = state.ScheduledJobStatusActive
	if isOneShotScheduleSpec(job.ScheduleSpec) {
		job.Status = state.ScheduledJobStatusPaused
	}
	if cause == nil {
		job.LastError = ""
		job.RetryCount = 0
	} else {
		job.LastError = strings.TrimSpace(cause.Error())
	}
	if _, err := s.jobStore.UpdateRuntime(ctx, runtimeUpdate(previous, job)); err != nil {
		return fmt.Errorf("record scheduled execution completion: %w", err)
	}
	return nil
}
