package presentation

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
)

func TestSlackRenderGoalProgress(t *testing.T) {
	t.Parallel()

	started := goalkeepercmd.NewStartedProgress(15, "run benchmark")
	gotStarted := RenderGoalProgress(started)
	if !strings.Contains(gotStarted, "*Goal run started*") || !strings.Contains(gotStarted, "• *Max iterations:* 15") || !strings.Contains(gotStarted, "• *Objective:* run benchmark") {
		t.Fatalf("RenderGoalProgress(started) = %q", gotStarted)
	}

	step := goalkeepercmd.NewStepProgress(2, 15, "worker", "executing", "benchmark finished")
	gotStep := RenderGoalProgress(step)
	if !strings.Contains(gotStep, "*Goal iteration 2/15:* worker executing.") || !strings.Contains(gotStep, "benchmark finished") {
		t.Fatalf("RenderGoalProgress(step) = %q", gotStep)
	}

	status := goalkeepercmd.NewStatusProgress("Goal run paused.")
	gotStatus := RenderGoalProgress(status)
	if gotStatus != "*Goal run paused.*" {
		t.Fatalf("RenderGoalProgress(status) = %q", gotStatus)
	}
}

func TestSlackRenderGoalOutcome(t *testing.T) {
	t.Parallel()

	outcome := goalkeepercmd.GoalOutcome{
		GoalReached:  true,
		ExportStatus: goalkeepercmd.GoalExportStatusExported,
		WhatWasDone:  "Benchmarked 10 endpoints.",
		Validation:   "verdict: pass",
		Verified:     "validator returned pass",
		NotVerified:  goalkeepercmd.DefaultNotVerifiedText,
		NextAction:   goalkeepercmd.DefaultExportedNextAction,
	}

	got := RenderGoalOutcome(outcome)
	if !strings.Contains(got, "*Result:* Goal completed.") {
		t.Fatalf("RenderGoalOutcome() = %q, want Result header", got)
	}
	if !strings.Contains(got, "Benchmarked 10 endpoints.") {
		t.Fatalf("RenderGoalOutcome() = %q, want what was done", got)
	}
	for _, notWant := range []string{"*Validation:*", "*Verified:*", "*Not verified:*", "*Next action:*", "*Export:*"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("RenderGoalOutcome() = %q, should not contain %q", got, notWant)
		}
	}
}

func TestSlackRenderGoalOutcomeFailure(t *testing.T) {
	t.Parallel()

	outcome := goalkeepercmd.GoalOutcome{
		GoalReached: false,
		WhatWasDone: "failed run",
		Validation:  "verdict: fail\nEvidence: high latency",
		Verified:    "validator returned feedback",
		NotVerified: "unverified profile",
		NextAction:  "optimize handlers",
	}

	got := RenderGoalOutcome(outcome)
	for _, want := range []string{
		"*Result:* Goal not completed.",
		"*What was done:*\nfailed run",
		"*Validation:*\nverdict: fail\nEvidence: high latency",
		"*Verified:* validator returned feedback",
		"*Not verified:* unverified profile",
		"*Next action:* optimize handlers",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderGoalOutcome() = %q, missing %q", got, want)
		}
	}
}
