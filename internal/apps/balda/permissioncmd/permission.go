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

type ToolCall struct {
	ID        string     `json:"id,omitempty"`
	Title     string     `json:"title,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	RawInput  string     `json:"raw_input,omitempty"`
	Locations []Location `json:"locations,omitempty"`
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

type interactionContextKey struct{}

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
