// Package help owns the transport-neutral help command.
package help

import (
	"context"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type Handler struct {
	dispatcher actortransport.Dispatcher
}

func New(dispatcher actortransport.Dispatcher) *Handler {
	return &Handler{dispatcher: dispatcher}
}

func (h *Handler) Name() string { return "help" }

func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "help-usage")
	}

	canUseSession := p.Access.SessionCommands || p.Access.Owner || p.Access.Collaborator || p.Access.WorkspaceMember
	isOwner := p.Access.Owner

	msg := renderHelpMessage(canUseSession, isOwner)
	return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, msg, "help-result")
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " help"
	}
	return "/help"
}

func renderHelpMessage(canUseSessionCommands bool, isOwner bool) string {
	var lines []string
	lines = append(lines, "# Available commands")
	lines = append(lines, "", "## Onboarding", "", "- `/start` — connect or restore access")

	if canUseSessionCommands {
		lines = append(lines, "", "## Sessions", "")
		lines = append(lines, "- `/topic <name>` — create a new DM topic session")
		lines = append(lines, "- `/reset` — reset current session and start again")
		lines = append(lines, "- `/close` — close topic or clear current DM session")
		lines = append(lines, "- `/cancel` — request cancel for the current turn")
		lines = append(lines, "- `/locator` — show current session locator")
		lines = append(lines, "- `/usage` — show last provider usage for this session")
		lines = append(lines, "", "## Automation", "")
		lines = append(lines, "- `/goalkeeper <objective>` — start a goal run")
		lines = append(lines, "- `/goalkeeper clear` — clear active goal run")
		lines = append(lines, "- `/auto` — show auto mode status")
		lines = append(lines, "- `/auto on` — enable auto mode")
		lines = append(lines, "- `/auto off` — disable auto mode")
	}

	if isOwner {
		lines = append(lines, "", plugincmd.HelpMarkdown())
		lines = append(lines, "", "## Admin", "")
		lines = append(lines, "- `/user add` — create collaborator invite")
		lines = append(lines, "- `/user list` — list collaborators and invites")
		lines = append(lines, "- `/user remove <user_id>` — remove collaborator")
	}

	return strings.Join(lines, "\n")
}
