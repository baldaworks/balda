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

const (
	DefaultNotVerifiedText       = "manual review still required"
	DefaultInspectNextAction     = "Inspect events and decide whether to continue, cancel, or ask a human."
	DefaultExportedNextAction    = "Review the exported result and continue with follow-up work if needed."
	DefaultNotExportedNextAction = "Review the direct working directory changes and commit or follow up manually if needed."

	GoalExportStatusExported    = "exported"
	GoalExportStatusFailed      = "export_failed"
	GoalExportStatusNotExported = "not_exported"
)

// RoutineSuccess returns true if the goal completed and its export was routine.
func (o GoalOutcome) RoutineSuccess() bool {
	return o.GoalReached && exportStatusIsRoutineSuccess(o.ExportStatus)
}

// ShouldRenderValidation returns true if validation feedback should be shown.
func (o GoalOutcome) ShouldRenderValidation() bool {
	val := strings.TrimSpace(o.Validation)
	if val == "" {
		return false
	}
	if !o.GoalReached {
		return true
	}
	return !validationIsRoutinePass(val)
}

// ShouldRenderVerified returns true if the verified status should be shown.
func (o GoalOutcome) ShouldRenderVerified() bool {
	ver := strings.TrimSpace(o.ResolvedVerified())
	if ver == "" {
		return false
	}
	if !o.RoutineSuccess() {
		return true
	}
	return !strings.EqualFold(ver, "validator returned pass")
}

// ShouldRenderNotVerified returns true if not-verified notes should be shown.
func (o GoalOutcome) ShouldRenderNotVerified() bool {
	trimmed := strings.TrimSpace(o.NotVerified)
	return trimmed != "" && !strings.EqualFold(trimmed, DefaultNotVerifiedText)
}

// ShouldRenderNextAction returns true if the next action should be shown.
func (o GoalOutcome) ShouldRenderNextAction() bool {
	trimmed := strings.TrimSpace(o.NextAction)
	if trimmed == "" {
		return false
	}
	if !o.GoalReached {
		return true
	}
	if !strings.EqualFold(trimmed, DefaultExportedNextAction) {
		if o.GoalReached && strings.TrimSpace(o.ExportStatus) == GoalExportStatusNotExported && strings.EqualFold(trimmed, DefaultNotExportedNextAction) {
			return false
		}
		return true
	}
	switch strings.TrimSpace(o.ExportStatus) {
	case GoalExportStatusFailed, GoalExportStatusNotExported:
		return true
	default:
		return false
	}
}

// ResolvedVerified returns the verified string with default fallback.
func (o GoalOutcome) ResolvedVerified() string {
	if trimmed := strings.TrimSpace(o.Verified); trimmed != "" {
		return trimmed
	}
	return "validator returned feedback"
}

// ResolvedNotVerified returns the not-verified string with default fallback.
func (o GoalOutcome) ResolvedNotVerified() string {
	if trimmed := strings.TrimSpace(o.NotVerified); trimmed != "" {
		return trimmed
	}
	return DefaultNotVerifiedText
}

// ResolvedNextAction returns the next action string with default fallback.
func (o GoalOutcome) ResolvedNextAction() string {
	if trimmed := strings.TrimSpace(o.NextAction); trimmed != "" {
		return trimmed
	}
	return DefaultInspectNextAction
}

func exportStatusIsRoutineSuccess(status string) bool {
	switch strings.TrimSpace(status) {
	case "", GoalExportStatusExported, GoalExportStatusNotExported:
		return true
	default:
		return false
	}
}

func validationIsRoutinePass(value string) bool {
	lowered := strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(lowered, "evidence:") || strings.Contains(lowered, "verdict: fail") || strings.Contains(lowered, "verdict fail") {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, ":", " ")))
	normalized = strings.Join(strings.Fields(normalized), " ")
	return strings.Contains(normalized, "verdict pass")
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
