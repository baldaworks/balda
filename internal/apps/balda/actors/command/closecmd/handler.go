// Package closecmd owns close command policy and session termination orchestration.
package closecmd

import (
	"context"
	"fmt"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

// SessionPort defines session reset methods required by the close command.
type SessionPort interface {
	ResetSessionWithReason(ctx context.Context, locator session.SessionLocator, reason session.BoundaryReason) error
}

// WorkCanceller synchronously stops active and queued work for the session.
type WorkCanceller interface {
	CancelWork(ctx context.Context, locator session.SessionLocator, actor string, reason string) error
}

// ControlDispatcher dispatches control cancel envelopes.
type ControlDispatcher interface {
	CancelSession(ctx context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error
}

// TopicCloser defines topic detection and close methods.
type TopicCloser interface {
	CloseTopic(ctx context.Context, locator deliverycmd.Locator) error
	IsTopic(locator deliverycmd.Locator) bool
}

// Handler handles the /close command across supported transports.
type Handler struct {
	sessions   SessionPort
	canceller  WorkCanceller
	control    ControlDispatcher
	channel    TopicCloser
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
}

// New creates a new close command Handler.
func New(
	sessions SessionPort,
	canceller WorkCanceller,
	control ControlDispatcher,
	channel TopicCloser,
	dispatcher actortransport.Dispatcher,
	logger zerolog.Logger,
) *Handler {
	return &Handler{
		sessions:   sessions,
		canceller:  canceller,
		control:    control,
		channel:    channel,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.close").Logger(),
	}
}

// Name returns "close".
func (h *Handler) Name() string { return "close" }

// Handle processes one /close command request.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "close-denied")
	}

	if !p.Conversation.Direct {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "This command is only available in direct messages.", "close-dm-only")
	}

	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "close-usage")
	}

	if h.canceller != nil {
		if err := h.canceller.CancelWork(ctx, p.Locator, "command.close", "session canceled by close command"); err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to cancel session work during /close")
		}
	}
	if h.control != nil {
		if err := h.control.CancelSession(ctx, p.Locator, p.Principal, "session canceled by close command", false); err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to publish /close cancel control command")
		}
	}

	if h.sessions == nil {
		return actorlayer.TransientError(fmt.Errorf("session service is required"))
	}

	isTopic := h.channel != nil && h.channel.IsTopic(p.Locator)
	if isTopic {
		if err := h.sessions.ResetSessionWithReason(ctx, p.Locator, session.BoundaryReasonClose); err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to reset session during /close topic")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not close this topic.", "close-failed")
		}
		if err := commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Closing this topic and resetting session history.", "close-topic"); err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to send /close confirmation")
		}
		if h.channel != nil {
			if err := h.channel.CloseTopic(ctx, p.Locator); err != nil {
				h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to close topic")
			}
		}
		return nil
	}

	if err := h.sessions.ResetSessionWithReason(ctx, p.Locator, session.BoundaryReasonClose); err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to reset session during /close root")
		errMsg := "Could not reset this session."
		if p.Transport == "zulip" {
			errMsg = "Could not close this session."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, errMsg, "close-failed")
	}

	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Session history reset.", "close-root")
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " close"
	}
	return "/close"
}
