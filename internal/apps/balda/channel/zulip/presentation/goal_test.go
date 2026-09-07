package presentation

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
)

func TestZulipRenderGoalProgress(t *testing.T) {
	t.Parallel()

	started := goalkeepercmd.NewStartedProgress(8, "verify zulip notifications")
	gotStarted := RenderGoalProgress(started)
	if !strings.Contains(gotStarted, "**Goal run started**") || !strings.Contains(gotStarted, "- **Max iterations:** 8") || !strings.Contains(gotStarted, "- **Objective:** verify zulip notifications") {
		t.Fatalf("RenderGoalProgress(started) = %q", gotStarted)
	}

	step := goalkeepercmd.NewStepProgress(3, 8, "validator", "checking", "stream message verified")
	gotStep := RenderGoalProgress(step)
	if !strings.Contains(gotStep, "**Goal iteration 3/8:** validator checking.") || !strings.Contains(gotStep, "stream message verified") {
		t.Fatalf("RenderGoalProgress(step) = %q", gotStep)
	}

	status := goalkeepercmd.NewStatusProgress("Goal finished.")
	gotStatus := RenderGoalProgress(status)
	if gotStatus != "**Goal finished.**" {
		t.Fatalf("RenderGoalProgress(status) = %q", gotStatus)
	}
}

func TestZulipRenderGoalOutcome(t *testing.T) {
	t.Parallel()

	outcome := goalkeepercmd.GoalOutcome{
		GoalReached:  true,
		ExportStatus: goalkeepercmd.GoalExportStatusExported,
		WhatWasDone:  "Zulip integration completed.",
		Validation:   "verdict: pass",
		Verified:     "validator returned pass",
		NotVerified:  goalkeepercmd.DefaultNotVerifiedText,
		NextAction:   goalkeepercmd.DefaultExportedNextAction,
	}

	got := RenderGoalOutcome(outcome)
	if !strings.Contains(got, "**Result:** Goal completed.") {
		t.Fatalf("RenderGoalOutcome() = %q, want Result header", got)
	}
	if !strings.Contains(got, "Zulip integration completed.") {
		t.Fatalf("RenderGoalOutcome() = %q, want what was done", got)
	}
	for _, notWant := range []string{"**Validation:**", "**Verified:**", "**Not verified:**", "**Next action:**", "**Export:**"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("RenderGoalOutcome() = %q, should not contain %q", got, notWant)
		}
	}
}

func TestZulipRenderGoalOutcomeFailure(t *testing.T) {
	t.Parallel()

	outcome := goalkeepercmd.GoalOutcome{
		GoalReached: false,
		WhatWasDone: "stream post failed",
		Validation:  "verdict: fail\nEvidence: unauthorized realm",
		Verified:    "validator returned feedback",
		NotVerified: "realm tokens unchecked",
		NextAction:  "check bot credentials",
	}

	got := RenderGoalOutcome(outcome)
	for _, want := range []string{
		"**Result:** Goal not completed.",
		"**What was done:**\nstream post failed",
		"**Validation:**\nverdict: fail\nEvidence: unauthorized realm",
		"**Verified:** validator returned feedback",
		"**Not verified:** realm tokens unchecked",
		"**Next action:** check bot credentials",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderGoalOutcome() = %q, missing %q", got, want)
		}
	}
}
