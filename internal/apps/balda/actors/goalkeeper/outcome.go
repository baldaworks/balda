package goalkeeper

import (
	"context"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/goaldelivery"
	"github.com/normahq/balda/internal/git"
	adksession "google.golang.org/adk/v2/session"
)

// outcome.go owns final goal result assembly and reviewable outcome rendering.
type goalOutcomeAssembler struct {
	jobs jobLifecycle
}

func newGoalOutcomeAssembler(jobs jobLifecycle) goalOutcomeAssembler {
	return goalOutcomeAssembler{jobs: jobs}
}

func (a goalOutcomeAssembler) toJobResult(r goalRunResult, goalReached bool, artifacts goalArtifactSnapshot, export *goalExportResultV1) goalResultPayloadV1 {
	workerOutput := redactSecrets(strings.TrimSpace(r.workerOutput))
	validatorOutput := redactSecrets(strings.TrimSpace(r.validatorOutput))
	latestWorkerOutput := redactSecrets(strings.TrimSpace(r.latestWorkerOutput))
	latestValidatorOutput := redactSecrets(strings.TrimSpace(r.latestValidatorOutput))
	finalText := redactSecrets(strings.TrimSpace(r.finalText))
	whatWasDone := firstNonEmpty(latestWorkerOutput, workerOutput, finalText, strings.TrimSpace(r.payload.Objective))
	validation := firstNonEmpty(latestValidatorOutput, validatorOutput, finalText)
	verified := "validator returned feedback"
	if reviewerPassed(latestValidatorOutput) {
		verified = "validator returned pass"
	}
	nextAction := goaldelivery.DefaultInspectNextAction
	if goalReached {
		nextAction = goaldelivery.DefaultExportedNextAction
		if export != nil {
			switch strings.TrimSpace(export.Status) {
			case goalExportStatusFailed:
				nextAction = "Inspect the preserved goal workspace and retry export after resolving the base-branch issue."
			case goalExportStatusNotExported:
				nextAction = "Review the direct working directory changes and commit or follow up manually if needed."
			}
		}
	} else if r.payload.MaxIterations > 0 && r.iterations >= r.payload.MaxIterations {
		nextAction = "Review failure evidence and rerun /goalkeeper or assign a narrower follow-up task."
	}
	artifactResult := &goalArtifactResultV1{
		WorkspaceDir: strings.TrimSpace(artifacts.WorkspaceDir),
		BranchName:   strings.TrimSpace(artifacts.BranchName),
		Commit:       strings.TrimSpace(artifacts.Commit),
		ChangedFiles: append([]string(nil), artifacts.ChangedFiles...),
		GitError:     strings.TrimSpace(artifacts.GitError),
	}
	return goalResultPayloadV1{
		SchemaVersion:  jobResultSchemaVersionV1,
		GoalReached:    goalReached,
		Iterations:     r.iterations,
		ExecutorOutput: workerOutput,
		ReviewerOutput: validatorOutput,
		ReviewerNotes:  validatorOutput,
		Artifacts:      artifactResult,
		Export:         export,
		ReviewableOutcome: goalReviewableOutcomeV1{
			SchemaVersion: jobReviewableOutcomeSchemaV1,
			WhatWasDone:   whatWasDone,
			Validation:    validation,
			Verified:      verified,
			NotVerified:   goaldelivery.DefaultNotVerifiedText,
			NextAction:    nextAction,
		},
	}
}

func (a goalOutcomeAssembler) renderJobOutcome(ctx context.Context, jobID string, profile deliverycmd.Profile, fallback string) string {
	if a.jobs == nil {
		return goaldelivery.RenderStatusMessage(profile, fallback)
	}
	task, ok, err := a.jobs.Get(ctx, jobID)
	if err != nil || !ok {
		return goaldelivery.RenderStatusMessage(profile, fallback)
	}
	return goaldelivery.RenderReviewableOutcome(profile, task)
}

func (r GoalFinalizationResult) toJobExportResult() *goalExportResultV1 {
	status := strings.TrimSpace(r.Status)
	if status == "" {
		status = goalExportStatusNotExported
	}
	return &goalExportResultV1{
		Status:        status,
		CommitMessage: redactSecrets(strings.TrimSpace(r.CommitMessage)),
		Reason:        redactSecrets(strings.TrimSpace(r.Reason)),
		Error:         redactSecrets(strings.TrimSpace(r.Error)),
	}
}

func snapshotGoalRunArtifacts(ctx context.Context, runtime GoalRun) goalArtifactSnapshot {
	if runtime == nil {
		return goalArtifactSnapshot{}
	}
	artifacts := goalArtifactSnapshot{
		WorkspaceDir: strings.TrimSpace(runtime.WorkspaceDir()),
		BranchName:   strings.TrimSpace(runtime.BranchName()),
	}
	if artifacts.WorkspaceDir == "" {
		return artifacts
	}
	if !git.Available(ctx, artifacts.WorkspaceDir) {
		artifacts.GitError = "workspace is not a git repository"
		return artifacts
	}
	status, err := git.GitRunCmdOutput(ctx, artifacts.WorkspaceDir, "git", "status", "--short")
	if err != nil {
		artifacts.GitError = err.Error()
	} else {
		for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				artifacts.ChangedFiles = append(artifacts.ChangedFiles, trimmed)
			}
		}
	}
	commit, err := git.GitRunCmdOutput(ctx, artifacts.WorkspaceDir, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		if artifacts.GitError == "" {
			artifacts.GitError = err.Error()
		}
	} else {
		artifacts.Commit = strings.TrimSpace(commit)
	}
	return artifacts
}

func appendVisibleText(existing string, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "\n\n" + next
}

func resetLatestStepOutput(result *goalRunResult, step string) {
	if result == nil {
		return
	}
	switch strings.TrimSpace(step) {
	case WorkerStep:
		result.latestWorkerOutput = ""
	case ValidatorStep:
		result.latestValidatorOutput = ""
	}
}

func visibleText(ev *adksession.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	content := ev.Content
	var parts []string
	for _, part := range content.Parts {
		if part != nil && !part.Thought && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func reviewerPassed(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "verdict: pass")
}
