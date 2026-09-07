package presentation

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
)

func TestTelegramRenderGoalProgress(t *testing.T) {
	t.Parallel()

	started := goalkeepercmd.NewStartedProgress(10, "migrate database")
	gotStarted := RenderGoalProgress(started)
	if !strings.Contains(gotStarted, "**Goal run started**") || !strings.Contains(gotStarted, "- **Max iterations:** 10") || !strings.Contains(gotStarted, "- **Objective:** migrate database") {
		t.Fatalf("RenderGoalProgress(started) = %q", gotStarted)
	}

	step := goalkeepercmd.NewStepProgress(1, 10, "worker", "executing", "running goose up")
	gotStep := RenderGoalProgress(step)
	if !strings.Contains(gotStep, "**Goal iteration 1/10:** worker executing.") || !strings.Contains(gotStep, "running goose up") {
		t.Fatalf("RenderGoalProgress(step) = %q", gotStep)
	}

	status := goalkeepercmd.NewStatusProgress("Goal run canceled.")
	gotStatus := RenderGoalProgress(status)
	if gotStatus != "**Goal run canceled.**" {
		t.Fatalf("RenderGoalProgress(status) = %q", gotStatus)
	}
}

func TestTelegramRenderGoalOutcome(t *testing.T) {
	t.Parallel()

	outcome := goalkeepercmd.GoalOutcome{
		GoalReached:  true,
		ExportStatus: goalkeepercmd.GoalExportStatusExported,
		WhatWasDone:  "Applied 3 migrations.",
		Validation:   "verdict: pass",
		Verified:     "validator returned pass",
		NotVerified:  goalkeepercmd.DefaultNotVerifiedText,
		NextAction:   goalkeepercmd.DefaultExportedNextAction,
	}

	got := RenderGoalOutcome(outcome)
	if !strings.Contains(got, "**Result:** Goal completed.") {
		t.Fatalf("RenderGoalOutcome() = %q, want Result header", got)
	}
	if !strings.Contains(got, "Applied 3 migrations.") {
		t.Fatalf("RenderGoalOutcome() = %q, want what was done", got)
	}
	// Routine success should omit redundant validation, verified, not verified, next action
	for _, notWant := range []string{"**Validation:**", "**Verified:**", "**Not verified:**", "**Next action:**", "**Export:**"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("RenderGoalOutcome() = %q, should not contain %q", got, notWant)
		}
	}
}

func TestTelegramRenderGoalOutcomeFailure(t *testing.T) {
	t.Parallel()

	outcome := goalkeepercmd.GoalOutcome{
		GoalReached: false,
		WhatWasDone: "failed migration",
		Validation:  "verdict: fail\nEvidence: syntax error",
		Verified:    "validator returned feedback",
		NotVerified: "manual check required",
		NextAction:  "fix SQL file",
	}

	got := RenderGoalOutcome(outcome)
	for _, want := range []string{
		"**Result:** Goal not completed.",
		"**What was done:**\nfailed migration",
		"**Validation:**\nverdict: fail\nEvidence: syntax error",
		"**Verified:** validator returned feedback",
		"**Not verified:** manual check required",
		"**Next action:** fix SQL file",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderGoalOutcome() = %q, missing %q", got, want)
		}
	}
}
