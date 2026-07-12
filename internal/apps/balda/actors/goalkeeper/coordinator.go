package goalkeeper

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/goaldelivery"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

// coordinator.go owns goal feature orchestration. The actor entrypoint should
// stay thin and delegate here after envelope decode/validation.
type coordinator struct {
	jobs            jobLifecycle
	events          jobEvents
	dispatcher      actortransport.Dispatcher
	sessionAccessor sessionAccessor
	goalRunPreparer GoalRunPreparer
	jobRuns         JobRuns
	maxIters        int
	logger          zerolog.Logger
}

func newCoordinator(params ActorParams) *coordinator {
	return &coordinator{
		jobs:            params.JobLifecycle,
		events:          params.JobEvents,
		dispatcher:      params.Dispatcher,
		sessionAccessor: newSessionAccessor(params.SessionManager),
		goalRunPreparer: params.GoalRunPreparer,
		jobRuns:         params.JobRuns,
		maxIters:        normalizeGoalMaxIterations(params.MaxIterations),
		logger:          params.Logger.With().Str("component", "balda.goalkeeper_actor").Logger(),
	}
}

func (c *coordinator) execute(ctx context.Context, env actorlayer.Envelope, payload goalJobPayload) error {
	progressEmitter := newGoalProgressEmitter(c.jobs, c.events, c.dispatcher)
	outcomes := newGoalOutcomeAssembler(c.jobs)
	jobID := strings.TrimSpace(payload.JobID)
	envelopeJobID := strings.TrimSpace(envelopeJobID(env))
	objective := strings.TrimSpace(payload.Objective)
	if jobID == "" {
		return actorlayer.PolicyError(fmt.Errorf("job id is required"))
	}
	if envelopeJobID != "" && envelopeJobID != jobID {
		return actorlayer.PolicyError(fmt.Errorf("goal job id mismatch: envelope=%q payload=%q", envelopeJobID, jobID))
	}
	if objective == "" {
		return actorlayer.PolicyError(fmt.Errorf("goal objective is required"))
	}
	if c.jobStatusIs(ctx, jobID, baldastate.JobStatusCompleted, baldastate.JobStatusFailed, baldastate.JobStatusCanceled, baldastate.JobStatusDeadLettered) {
		return nil
	}
	maxIterations := normalizeGoalMaxIterations(payload.MaxIterations)
	if maxIterations == defaultGoalMaxIterations && c.maxIters != defaultGoalMaxIterations {
		maxIterations = c.maxIters
	}
	payload.JobID = jobID
	payload.Objective = objective
	payload.MaxIterations = maxIterations

	if err := c.ensureGoalJob(ctx, payload); err != nil {
		return err
	}
	skip, err := c.ensureNoOtherActiveGoal(ctx, jobID, payload)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	ts, err := c.resolveSession(ctx, payload)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	if err := c.jobs.MarkStatus(ctx, jobID, baldastate.JobStatusRunning, actorName, env.ID, "", map[string]any{
		"objective": objective,
	}); err != nil {
		return actorlayer.TransientError(err)
	}
	if err := progressEmitter.deliver(ctx, jobID, payload, goaldelivery.RenderStartedMessage(goalDeliveryProfile(payload), maxIterations, objective), "started"); err != nil {
		return err
	}

	if c.goalRunPreparer == nil {
		return actorlayer.TransientError(fmt.Errorf("goal run preparer is required"))
	}
	goalRun, err := c.goalRunPreparer.PrepareGoalRun(ctx, GoalRunConfig{
		SourceSessionID: payload.Locator.SessionID,
		JobID:           jobID,
		UserID:          ts.GetUserID(),
		MaxIterations:   uint(maxIterations),
	})
	if err != nil {
		return actorlayer.TransientError(err)
	}
	defer func() {
		if err := goalRun.Close(); err != nil {
			c.logger.Warn().Err(err).Str("job_id", jobID).Msg("failed to close goal run")
		}
	}()

	runCtx, cancel := context.WithCancel(ctx)
	runID := ""
	if c.jobRuns != nil {
		runID = c.jobRuns.Register(jobID, cancel)
		defer c.jobRuns.Unregister(jobID, runID)
	}
	defer cancel()

	result, err := c.runWorkflow(runCtx, goalRun, ts.GetUserID(), goalRun.SessionID(), payload)
	artifacts := snapshotGoalRunArtifacts(ctx, goalRun)
	if err != nil {
		if errors.Is(runCtx.Err(), context.Canceled) {
			if cleanupErr := goalRun.CleanupResources(ctx); cleanupErr != nil {
				c.logger.Warn().Err(cleanupErr).Str("job_id", jobID).Msg("failed to cleanup canceled goal run")
			}
			if setErr := c.jobs.SetResult(ctx, jobID, outcomes.toJobResult(result, false, artifacts, &goalExportResultV1{Status: "canceled"}), baldastate.JobStatusCanceled, actorName, "goal run canceled"); setErr != nil {
				return actorlayer.TransientError(setErr)
			}
			return progressEmitter.deliver(ctx, jobID, payload, goaldelivery.RenderStatusMessage(goalDeliveryProfile(payload), "Goal run canceled."), "canceled")
		}
		reason := redactSecrets(err.Error())
		if cleanupErr := goalRun.CleanupResources(ctx); cleanupErr != nil {
			c.logger.Warn().Err(cleanupErr).Str("job_id", jobID).Msg("failed to cleanup failed goal run")
		}
		if setErr := c.jobs.SetResult(ctx, jobID, outcomes.toJobResult(result, false, artifacts, &goalExportResultV1{Status: "failed", Error: reason}), baldastate.JobStatusFailed, actorName, reason); setErr != nil {
			return actorlayer.TransientError(setErr)
		}
		return progressEmitter.deliver(ctx, jobID, payload, goaldelivery.RenderStatusMessage(goalDeliveryProfile(payload), "Goal run failed: "+reason), "failed")
	}
	if reviewerPassed(result.latestValidatorOutput) {
		finalization, exportErr := goalRun.Finalize(ctx, payload.Objective, result.latestWorkerOutput, result.latestValidatorOutput)
		exportSummary := finalization.toJobExportResult()
		if exportErr != nil || strings.TrimSpace(exportSummary.Status) == goalExportStatusFailed {
			if exportSummary.Status == "" {
				exportSummary.Status = goalExportStatusFailed
			}
			if exportSummary.Error == "" && exportErr != nil {
				exportSummary.Error = redactSecrets(exportErr.Error())
			}
			jobResult := outcomes.toJobResult(result, true, artifacts, exportSummary)
			if setErr := c.jobs.SetResult(ctx, jobID, jobResult, baldastate.JobStatusFailed, actorName, exportSummary.Error); setErr != nil {
				return actorlayer.TransientError(setErr)
			}
			return progressEmitter.deliver(ctx, jobID, payload, outcomes.renderJobOutcome(ctx, jobID, goalDeliveryProfile(payload), "Goal validation passed, but export failed."), "export-failed")
		}
		jobResult := outcomes.toJobResult(result, true, artifacts, exportSummary)
		if err := c.jobs.SetResult(ctx, jobID, jobResult, baldastate.JobStatusCompleted, actorName, ""); err != nil {
			return actorlayer.TransientError(err)
		}
		if cleanupErr := goalRun.CleanupResources(ctx); cleanupErr != nil {
			c.logger.Warn().Err(cleanupErr).Str("job_id", jobID).Msg("failed to cleanup completed goal run")
		}
		return progressEmitter.deliver(ctx, jobID, payload, outcomes.renderJobOutcome(ctx, jobID, goalDeliveryProfile(payload), "Goal run completed."), "completed")
	}
	if cleanupErr := goalRun.CleanupResources(ctx); cleanupErr != nil {
		c.logger.Warn().Err(cleanupErr).Str("job_id", jobID).Msg("failed to cleanup max-iteration goal run")
	}
	jobResult := outcomes.toJobResult(result, false, artifacts, &goalExportResultV1{Status: goalExportStatusNotExported})
	if err := c.jobs.SetResult(ctx, jobID, jobResult, baldastate.JobStatusFailed, actorName, "max iterations reached"); err != nil {
		return actorlayer.TransientError(err)
	}
	return progressEmitter.deliver(ctx, jobID, payload, outcomes.renderJobOutcome(ctx, jobID, goalDeliveryProfile(payload), "Goal run reached max iterations without passing validation."), "max-iterations")
}

func (c *coordinator) jobStatusIs(ctx context.Context, jobID string, statuses ...string) bool {
	if c == nil || c.jobs == nil || strings.TrimSpace(jobID) == "" {
		return false
	}
	task, ok, err := c.jobs.Get(ctx, jobID)
	if err != nil || !ok {
		return false
	}
	for _, status := range statuses {
		if strings.TrimSpace(task.Status) == strings.TrimSpace(status) {
			return true
		}
	}
	return false
}
