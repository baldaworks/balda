package goalkeeper

import (
	"encoding/json"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/goaldelivery"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestGoalOutcomeFromJobRecordStandardOutcome(t *testing.T) {
	t.Parallel()

	resultMap := map[string]any{
		"goal_reached": true,
		"iterations":   float64(4),
		"reviewable_outcome": map[string]any{
			"what_was_done":         "token=secret-token-123 done",
			"validation_output":     "verdict: pass",
			"what_was_verified":     "validator returned pass",
			"what_was_not_verified": "manual review",
			"next_action":           "ship it",
		},
		"export": map[string]any{
			"status": "exported",
			"reason": "clean",
			"error":  "",
		},
	}
	data, err := json.Marshal(resultMap)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	task := baldastate.JobRecord{
		ID:        "goal-task-1",
		Objective: "count lines",
		Status:    baldastate.JobStatusCompleted,
		Result:    string(data),
	}

	outcome := GoalOutcomeFromJobRecord(task)
	if outcome.JobID != "goal-task-1" {
		t.Fatalf("outcome.JobID = %q, want %q", outcome.JobID, "goal-task-1")
	}
	if outcome.Objective != "count lines" {
		t.Fatalf("outcome.Objective = %q, want %q", outcome.Objective, "count lines")
	}
	if outcome.Iterations != 4 {
		t.Fatalf("outcome.Iterations = %d, want 4", outcome.Iterations)
	}
	if !outcome.GoalReached {
		t.Fatalf("outcome.GoalReached = false, want true")
	}
	if outcome.WhatWasDone == "token=secret-token-123 done" {
		t.Fatalf("expected secrets in WhatWasDone to be redacted, got %q", outcome.WhatWasDone)
	}
	if outcome.Validation != "verdict: pass" {
		t.Fatalf("outcome.Validation = %q, want 'verdict: pass'", outcome.Validation)
	}
	if outcome.Verified != "validator returned pass" {
		t.Fatalf("outcome.Verified = %q, want 'validator returned pass'", outcome.Verified)
	}
	if outcome.NotVerified != "manual review" {
		t.Fatalf("outcome.NotVerified = %q, want 'manual review'", outcome.NotVerified)
	}
	if outcome.NextAction != "ship it" {
		t.Fatalf("outcome.NextAction = %q, want 'ship it'", outcome.NextAction)
	}
	if outcome.ExportStatus != "exported" {
		t.Fatalf("outcome.ExportStatus = %q, want 'exported'", outcome.ExportStatus)
	}
}

func TestGoalOutcomeFromJobRecordFallbackFields(t *testing.T) {
	t.Parallel()

	resultMap := map[string]any{
		"goal_reached":    "true",
		"executor_output": "compiled binary",
		"reviewer_output": "all checks pass",
	}
	data, err := json.Marshal(resultMap)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	task := baldastate.JobRecord{
		ID:        "goal-task-2",
		Objective: "compile code",
		Status:    baldastate.JobStatusCompleted,
		Result:    string(data),
	}

	outcome := GoalOutcomeFromJobRecord(task)
	if !outcome.GoalReached {
		t.Fatalf("outcome.GoalReached = false, want true")
	}
	if outcome.WhatWasDone != "compiled binary" {
		t.Fatalf("outcome.WhatWasDone = %q, want 'compiled binary'", outcome.WhatWasDone)
	}
	if outcome.Validation != "all checks pass" {
		t.Fatalf("outcome.Validation = %q, want 'all checks pass'", outcome.Validation)
	}
	if outcome.Verified != "validator returned feedback" {
		t.Fatalf("outcome.Verified = %q, want default", outcome.Verified)
	}
	if outcome.NotVerified != goaldelivery.DefaultNotVerifiedText {
		t.Fatalf("outcome.NotVerified = %q, want default", outcome.NotVerified)
	}
	if outcome.NextAction != goaldelivery.DefaultInspectNextAction {
		t.Fatalf("outcome.NextAction = %q, want default", outcome.NextAction)
	}
}

func TestGoalOutcomeFromJobRecordUnparseableResult(t *testing.T) {
	t.Parallel()

	task := baldastate.JobRecord{
		ID:        "goal-task-3",
		Objective: "fallback objective",
		Status:    baldastate.JobStatusFailed,
		Result:    "not a json",
	}

	outcome := GoalOutcomeFromJobRecord(task)
	if outcome.GoalReached {
		t.Fatalf("outcome.GoalReached = true, want false")
	}
	if outcome.WhatWasDone != "fallback objective" {
		t.Fatalf("outcome.WhatWasDone = %q, want 'fallback objective'", outcome.WhatWasDone)
	}
}
