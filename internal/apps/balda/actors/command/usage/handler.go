// Package usage owns the transport-neutral usage command.
package usage

import (
	"context"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usageview"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type SessionStateReader interface {
	RuntimeStateValue(ctx context.Context, locator deliverycmd.Locator, key string) (any, bool, error)
}

type Handler struct {
	sessions   SessionStateReader
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
}

func New(sessions SessionStateReader, dispatcher actortransport.Dispatcher, logger zerolog.Logger) *Handler {
	return &Handler{
		sessions:   sessions,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.usage").Logger(),
	}
}

func (h *Handler) Name() string { return "usage" }

func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "usage-denied")
	}

	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "usage-usage")
	}

	snapshot, ok, err := usageview.LoadSnapshot(ctx, h.sessions, p.Locator)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to load usage snapshot")
	}
	if err != nil || !ok {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "No provider usage has been recorded for this session yet.", "usage-empty")
	}

	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, usageview.RenderSnapshot(snapshot), "usage-result")
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " usage"
	}
	return "/usage"
}
