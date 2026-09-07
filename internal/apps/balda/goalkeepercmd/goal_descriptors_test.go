package goalkeepercmd

import (
	"encoding/json"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestGoalDescriptorsTypes(t *testing.T) {
	t.Parallel()

	if ProgressDescriptor.Type != deliveryfmt.MessageTypeGoalProgress {
		t.Fatalf("ProgressDescriptor.Type = %q, want %q", ProgressDescriptor.Type, deliveryfmt.MessageTypeGoalProgress)
	}
	if OutcomeDescriptor.Type != deliveryfmt.MessageTypeGoalOutcome {
		t.Fatalf("OutcomeDescriptor.Type = %q, want %q", OutcomeDescriptor.Type, deliveryfmt.MessageTypeGoalOutcome)
	}
}

func TestGoalProgressConstructorsAndEnvelope(t *testing.T) {
	t.Parallel()

	started := NewStartedProgress(25, "  build feature  ")
	if started.Kind != GoalProgressKindStarted {
		t.Fatalf("started.Kind = %q, want %q", started.Kind, GoalProgressKindStarted)
	}
	if started.MaxIterations != 25 {
		t.Fatalf("started.MaxIterations = %d, want 25", started.MaxIterations)
	}
	if started.Objective != "build feature" {
		t.Fatalf("started.Objective = %q, want %q", started.Objective, "build feature")
	}

	step := NewStepProgress(2, 25, "worker", "executing", "echo hello")
	if step.Kind != GoalProgressKindStep {
		t.Fatalf("step.Kind = %q, want %q", step.Kind, GoalProgressKindStep)
	}
	if step.Iteration != 2 || step.MaxIterations != 25 || step.Step != "worker" || step.Action != "executing" || step.Body != "echo hello" {
		t.Fatalf("unexpected step progress: %+v", step)
	}

	status := NewStatusProgress("  Goal run canceled.  ")
	if status.Kind != GoalProgressKindStatus || status.Text != "Goal run canceled." {
		t.Fatalf("unexpected status progress: %+v", status)
	}

	env := ProgressEnvelope(started)
	if env.Type() != deliveryfmt.MessageTypeGoalProgress {
		t.Fatalf("env.Type() = %q, want %q", env.Type(), deliveryfmt.MessageTypeGoalProgress)
	}
	if env.Body.Objective != "build feature" {
		t.Fatalf("env.Body.Objective = %q, want %q", env.Body.Objective, "build feature")
	}
}

func TestGoalOutcomeEnvelopeAndJSON(t *testing.T) {
	t.Parallel()

	outcome := GoalOutcome{
		JobID:        "goal-123",
		Objective:    "verify tests",
		Iterations:   3,
		GoalReached:  true,
		WhatWasDone:  "all tests pass",
		Validation:   "verdict: pass",
		Verified:     "validator returned pass",
		NotVerified:  "none",
		NextAction:   "done",
		ExportStatus: "exported",
		ExportReason: "clean",
		ExportError:  "",
	}

	env := OutcomeEnvelope(outcome)
	if env.Type() != deliveryfmt.MessageTypeGoalOutcome {
		t.Fatalf("env.Type() = %q, want %q", env.Type(), deliveryfmt.MessageTypeGoalOutcome)
	}
	if env.Body.JobID != "goal-123" || !env.Body.GoalReached {
		t.Fatalf("unexpected outcome envelope body: %+v", env.Body)
	}

	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal GoalOutcome: %v", err)
	}

	var decoded GoalOutcome
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal GoalOutcome: %v", err)
	}
	if decoded != outcome {
		t.Fatalf("decoded outcome = %+v, want %+v", decoded, outcome)
	}
}
