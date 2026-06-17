package goalkeeper

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

func TestRenderGoalStartedMessagePlainMatchesLegacyText(t *testing.T) {
	t.Parallel()

	got := renderGoalStartedMessage(deliverycmd.Profile{}, 25, "count total go lines")
	want := "Goal run started. Max iterations: 25.\n\nObjective: count total go lines"
	if got != want {
		t.Fatalf("renderGoalStartedMessage() = %q, want %q", got, want)
	}
}

func TestRenderGoalStepMessageMarkdownFormatsHeaderAndPreservesBody(t *testing.T) {
	t.Parallel()

	body := "worker update\n---\n![bad](http://invalid/image.png)"
	got := renderGoalStepMessage(
		deliverycmd.Profile{FormattingMode: "rich_markdown"},
		1,
		25,
		"worker",
		"update",
		body,
	)
	wantPrefix := "**Goal iteration 1/25:** worker update."
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("renderGoalStepMessage() = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.Contains(got, "\n\n"+body) {
		t.Fatalf("renderGoalStepMessage() = %q, want unchanged body %q", got, body)
	}
}

func TestRenderGoalStartedMessageHTMLEscapesSystemFields(t *testing.T) {
	t.Parallel()

	got := renderGoalStartedMessage(
		deliverycmd.Profile{FormattingMode: "rich_html"},
		3,
		"ship <release> & verify",
	)
	for _, want := range []string{
		"<b>Goal run started</b>",
		"<b>Objective:</b> ship &lt;release&gt; &amp; verify",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderGoalStartedMessage() = %q, want %q", got, want)
		}
	}
}

func TestRenderGoalStartedMessageMarkdownUsesBlockSafeLayout(t *testing.T) {
	t.Parallel()

	got := renderGoalStartedMessage(deliverycmd.Profile{FormattingMode: "rich_markdown"}, 25, "count total go lines")
	for _, want := range []string{
		"**Goal run started**",
		"- **Max iterations:** 25",
		"- **Objective:** count total go lines",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderGoalStartedMessage() = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "25 **Objective:**") {
		t.Fatalf("renderGoalStartedMessage() = %q, objective collapsed onto max iterations", got)
	}
}

func TestRenderGoalStepMessageHTMLPreservesBody(t *testing.T) {
	t.Parallel()

	body := "<b>validator</b>\n---\nplain"
	got := renderGoalStepMessage(
		deliverycmd.Profile{FormattingMode: "html"},
		2,
		5,
		"validator",
		"completed",
		body,
	)
	if !strings.HasPrefix(got, "<b>Goal iteration 2/5:</b> validator completed.") {
		t.Fatalf("renderGoalStepMessage() = %q, want HTML header", got)
	}
	if !strings.Contains(got, "\n\n"+body) {
		t.Fatalf("renderGoalStepMessage() = %q, want unchanged body %q", got, body)
	}
}

func TestRenderGoalStatusMessageUnknownModeFallsBackToPlain(t *testing.T) {
	t.Parallel()

	got := renderGoalStatusMessage(deliverycmd.Profile{FormattingMode: "unknown"}, "Goal run canceled.")
	if got != "Goal run canceled." {
		t.Fatalf("renderGoalStatusMessage() = %q, want plain fallback", got)
	}
}

func TestRenderReviewableOutcomeOmitsSuccessfulExportDefaults(t *testing.T) {
	t.Parallel()

	got := renderReviewableOutcomeWithProfile(deliverycmd.Profile{}, taskRecordWithResult(t, true, goalExportStatusExported, "", "", defaultNotVerifiedText, defaultExportedNextAction), taskArtifactSnapshot{})
	for _, notWant := range []string{
		"Not verified:",
		"Next action:",
		defaultNotVerifiedText,
		defaultExportedNextAction,
	} {
		if strings.Contains(got, notWant) {
			t.Fatalf("renderReviewableOutcomeWithProfile() = %q, did not want %q", got, notWant)
		}
	}
	if !strings.Contains(got, "Result: Goal completed.") {
		t.Fatalf("renderReviewableOutcomeWithProfile() = %q, want result", got)
	}
}

func TestRenderReviewableOutcomeKeepsActionableNextActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		goalReached  bool
		exportStatus string
		nextAction   string
	}{
		{
			name:         "export failed",
			goalReached:  true,
			exportStatus: goalExportStatusFailed,
			nextAction:   "Inspect the preserved goal workspace and retry export after resolving the base-branch issue.",
		},
		{
			name:         "not exported",
			goalReached:  true,
			exportStatus: goalExportStatusNotExported,
			nextAction:   "Review the direct working directory changes and commit or follow up manually if needed.",
		},
		{
			name:        "not reached",
			goalReached: false,
			nextAction:  "Review failure evidence and rerun /goal or assign a narrower follow-up task.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := renderReviewableOutcomeWithProfile(deliverycmd.Profile{}, taskRecordWithResult(t, tt.goalReached, tt.exportStatus, "", "", defaultNotVerifiedText, tt.nextAction), taskArtifactSnapshot{})
			if !strings.Contains(got, "Next action: "+tt.nextAction) {
				t.Fatalf("renderReviewableOutcomeWithProfile() = %q, want next action %q", got, tt.nextAction)
			}
		})
	}
}

func TestRenderReviewableOutcomeKeepsExplicitNotVerified(t *testing.T) {
	t.Parallel()

	got := renderReviewableOutcomeWithProfile(deliverycmd.Profile{}, taskRecordWithResult(t, true, goalExportStatusExported, "logs were not inspected", "", "manual log review remains", defaultExportedNextAction), taskArtifactSnapshot{})
	if !strings.Contains(got, "Not verified: manual log review remains") {
		t.Fatalf("renderReviewableOutcomeWithProfile() = %q, want explicit not verified", got)
	}
}

func taskRecordWithResult(t *testing.T, goalReached bool, exportStatus string, notVerified string, exportReason string, outcomeNotVerified string, nextAction string) baldastate.SwarmTaskRecord {
	t.Helper()

	result := map[string]any{
		"goal_reached": goalReached,
		"reviewable_outcome": map[string]any{
			"what_was_done":         "work completed",
			"validation_output":     "verdict: pass",
			"what_was_verified":     "validator returned pass",
			"what_was_not_verified": firstNonEmpty(outcomeNotVerified, notVerified),
			"next_action":           nextAction,
		},
	}
	if exportStatus != "" {
		result["export"] = map[string]any{
			"status": exportStatus,
			"reason": exportReason,
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	status := baldastate.SwarmTaskStatusCompleted
	if !goalReached {
		status = baldastate.SwarmTaskStatusFailed
	}
	return baldastate.SwarmTaskRecord{
		Status:     status,
		Objective:  "objective",
		ResultJSON: string(data),
	}
}
