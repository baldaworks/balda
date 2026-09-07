package goalkeepercmd

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// GoalProgressKind categorizes a goal progress event.
type GoalProgressKind string

const (
	GoalProgressKindStarted GoalProgressKind = "started"
	GoalProgressKindStep    GoalProgressKind = "step"
	GoalProgressKindStatus  GoalProgressKind = "status"
)

// GoalProgress represents a transport-neutral progress event during goal execution.
type GoalProgress struct {
	Kind          GoalProgressKind `json:"kind"`
	Iteration     int              `json:"iteration,omitempty"`
	MaxIterations int              `json:"max_iterations,omitempty"`
	Objective     string           `json:"objective,omitempty"`
	Step          string           `json:"step,omitempty"`
	Action        string           `json:"action,omitempty"`
	Body          string           `json:"body,omitempty"`
	Text          string           `json:"text,omitempty"`
}

// GoalOutcome represents the terminal result and reviewable outcome of a goal run.
type GoalOutcome struct {
	JobID        string `json:"job_id,omitempty"`
	Objective    string `json:"objective,omitempty"`
	Iterations   int    `json:"iterations,omitempty"`
	GoalReached  bool   `json:"goal_reached"`
	WhatWasDone  string `json:"what_was_done,omitempty"`
	Validation   string `json:"validation,omitempty"`
	Verified     string `json:"verified,omitempty"`
	NotVerified  string `json:"not_verified,omitempty"`
	NextAction   string `json:"next_action,omitempty"`
	ExportStatus string `json:"export_status,omitempty"`
	ExportReason string `json:"export_reason,omitempty"`
	ExportError  string `json:"export_error,omitempty"`
}

var (
	// ProgressDescriptor identifies the versioned structured goal progress message.
	ProgressDescriptor = deliveryfmt.Descriptor[GoalProgress]{
		Type: deliveryfmt.MessageTypeGoalProgress,
	}

	// OutcomeDescriptor identifies the versioned structured goal outcome message.
	OutcomeDescriptor = deliveryfmt.Descriptor[GoalOutcome]{
		Type: deliveryfmt.MessageTypeGoalOutcome,
	}
)

// NewStartedProgress builds a GoalProgress for a started goal run.
func NewStartedProgress(maxIterations int, objective string) GoalProgress {
	return GoalProgress{
		Kind:          GoalProgressKindStarted,
		MaxIterations: maxIterations,
		Objective:     strings.TrimSpace(objective),
	}
}

// NewStepProgress builds a GoalProgress for an iteration step.
func NewStepProgress(iteration, maxIterations int, step, action, body string) GoalProgress {
	return GoalProgress{
		Kind:          GoalProgressKindStep,
		Iteration:     iteration,
		MaxIterations: maxIterations,
		Step:          strings.TrimSpace(step),
		Action:        strings.TrimSpace(action),
		Body:          strings.TrimSpace(body),
	}
}

// NewStatusProgress builds a GoalProgress for a status notification.
func NewStatusProgress(text string) GoalProgress {
	return GoalProgress{
		Kind: GoalProgressKindStatus,
		Text: strings.TrimSpace(text),
	}
}

// ProgressEnvelope wraps a GoalProgress in a structured envelope.
func ProgressEnvelope(progress GoalProgress) deliveryfmt.StructuredEnvelope[GoalProgress] {
	return deliveryfmt.StructuredEnvelope[GoalProgress]{
		Descriptor: ProgressDescriptor,
		Body:       progress,
	}
}

// OutcomeEnvelope wraps a GoalOutcome in a structured envelope.
func OutcomeEnvelope(outcome GoalOutcome) deliveryfmt.StructuredEnvelope[GoalOutcome] {
	return deliveryfmt.StructuredEnvelope[GoalOutcome]{
		Descriptor: OutcomeDescriptor,
		Body:       outcome,
	}
}
