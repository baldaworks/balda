// Package permissioncmd defines transport-neutral contracts for permission review.
package permissioncmd

import (
	"context"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/questioncmd"
)

type Mode string

const (
	ModeAllowAll Mode = "allow_all"
	ModeAsk      Mode = "ask"
	ModeDenyAll  Mode = "deny_all"
)

type Option struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type Location struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type ContentKind string

const (
	ContentKindText     ContentKind = "text"
	ContentKindDiff     ContentKind = "diff"
	ContentKindTerminal ContentKind = "terminal"
)

type Content struct {
	Kind       ContentKind `json:"kind"`
	Text       string      `json:"text,omitempty"`
	Path       string      `json:"path,omitempty"`
	OldText    *string     `json:"old_text,omitempty"`
	NewText    string      `json:"new_text,omitempty"`
	TerminalID string      `json:"terminal_id,omitempty"`
}

type ToolCall struct {
	ID        string     `json:"id,omitempty"`
	Title     string     `json:"title,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	RawInput  string     `json:"raw_input,omitempty"`
	Locations []Location `json:"locations,omitempty"`
	Content   []Content  `json:"content,omitempty"`
}

type Request struct {
	Interaction questioncmd.InteractionContext `json:"interaction"`
	ToolCall    ToolCall                       `json:"tool_call"`
	Options     []Option                       `json:"options"`
}

type Decision struct {
	OptionID string `json:"option_id,omitempty"`
	Source   string `json:"source,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
}

type OutcomeKind string

const (
	OutcomeAllowed  OutcomeKind = "allowed"
	OutcomeDenied   OutcomeKind = "denied"
	OutcomeCanceled OutcomeKind = "canceled"
)

// Outcome is the provider-independent semantic result of a permission review.
type Outcome struct {
	Kind       OutcomeKind `json:"kind"`
	Source     string      `json:"source,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type OutcomeSink interface {
	RecordPermissionOutcome(Outcome)
}

type interactionContextKey struct{}
type outcomeContextKey struct{}

func WithInteraction(ctx context.Context, interaction questioncmd.InteractionContext) context.Context {
	return context.WithValue(ctx, interactionContextKey{}, interaction)
}

func InteractionFromContext(ctx context.Context) (questioncmd.InteractionContext, bool) {
	if ctx == nil {
		return questioncmd.InteractionContext{}, false
	}
	interaction, ok := ctx.Value(interactionContextKey{}).(questioncmd.InteractionContext)
	return interaction, ok && strings.TrimSpace(interaction.SessionID) != ""
}

func WithOutcomeSink(ctx context.Context, sink OutcomeSink) context.Context {
	return context.WithValue(ctx, outcomeContextKey{}, sink)
}

func RecordOutcome(ctx context.Context, outcome Outcome) {
	if ctx == nil {
		return
	}
	if sink, ok := ctx.Value(outcomeContextKey{}).(OutcomeSink); ok && sink != nil {
		sink.RecordPermissionOutcome(outcome)
	}
}
