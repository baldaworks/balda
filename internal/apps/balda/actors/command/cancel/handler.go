// Package cancel owns the transport-neutral cancel command.
package cancel

import (
	"context"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

// TurnCanceller cancels active/queued turns for a session.
type TurnCanceller interface {
	CancelTurn(ctx context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error
}

// Handler executes the cancel command.
type Handler struct {
	canceller  TurnCanceller
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
}

// New creates a cancel command handler.
func New(canceller TurnCanceller, dispatcher actortransport.Dispatcher, logger zerolog.Logger) *Handler {
	return &Handler{
		canceller:  canceller,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.cancel").Logger(),
	}
}

func (h *Handler) Name() string { return "cancel" }

func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "cancel-denied")
	}

	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "cancel-usage")
	}

	if h.canceller == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Cancel is unavailable right now. Please try again.", "cancel-unavailable")
	}

	if err := h.canceller.CancelTurn(ctx, p.Locator, p.Principal, "session turn canceled by user", true); err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to submit cancel control")
		if sendErr := commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not request cancel.", "cancel-failed"); sendErr != nil {
			return sendErr
		}
		return nil
	}

	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Cancel requested.", "cancel-requested")
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" && root != "/" {
		return root + " cancel"
	}
	return "/cancel"
}
