// Package auto owns the transport-neutral auto mode command.
package auto

import (
	"context"
	"strings"
	"time"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type SessionStateReader interface {
	RuntimeStateValue(ctx context.Context, locator deliverycmd.Locator, key string) (any, bool, error)
}

type Handler struct {
	sessions    SessionStateReader
	dispatcher  actortransport.Dispatcher
	maxTurns    int
	nowProvider func() time.Time
	logger      zerolog.Logger
}

func New(sessions SessionStateReader, dispatcher actortransport.Dispatcher, maxTurns int, nowProvider func() time.Time, logger zerolog.Logger) *Handler {
	if nowProvider == nil {
		nowProvider = time.Now
	}
	return &Handler{
		sessions:    sessions,
		dispatcher:  dispatcher,
		maxTurns:    automode.NormalizeMaxTurns(maxTurns),
		nowProvider: nowProvider,
		logger:      logger.With().Str("component", "balda.actors.command.auto").Logger(),
	}
}

func (h *Handler) Name() string { return "auto" }

func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "auto-denied")
	}

	arg := strings.ToLower(strings.TrimSpace(p.Args))
	switch arg {
	case "":
		status, err := h.loadAutoStatus(ctx, p.Locator)
		if err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to load auto mode status")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not read auto mode status.", "auto-error")
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, automode.RenderStatusMarkdown(status), "auto-status")
	case "on":
		if err := h.dispatchAutoStateUpdate(ctx, p.Locator, automode.EnableStateWithMaxTurns(h.nowProvider(), h.maxTurns)); err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to dispatch auto mode enable")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not enable auto mode.", "auto-error")
		}
		status := automode.NormalizeWithDefault(automode.Status{
			Enabled:  true,
			State:    automode.StateIdle,
			MaxTurns: h.maxTurns,
		}, h.maxTurns)
		return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, automode.RenderStatusMarkdown(status), "auto-enabled")
	case "off":
		if err := h.dispatchAutoStateUpdate(ctx, p.Locator, automode.DisableState()); err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to dispatch auto mode disable")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not disable auto mode.", "auto-error")
		}
		status := automode.DefaultStatusWithMaxTurns(h.maxTurns)
		return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, automode.RenderStatusMarkdown(status), "auto-disabled")
	default:
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "auto-usage")
	}
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " auto\n" + root + " auto on\n" + root + " auto off"
	}
	return "/auto\n/auto on\n/auto off"
}

func (h *Handler) dispatchAutoStateUpdate(ctx context.Context, locator deliverycmd.Locator, state map[string]any) error {
	if h.dispatcher == nil {
		return nil
	}
	env, err := automodecmd.Envelope(automodecmd.Payload{
		Locator: locator,
		State:   state,
	})
	if err != nil {
		return err
	}
	_, err = h.dispatcher.Dispatch(ctx, env)
	return err
}

func (h *Handler) loadAutoStatus(ctx context.Context, locator deliverycmd.Locator) (automode.Status, error) {
	status := automode.DefaultStatusWithMaxTurns(h.maxTurns)
	if h.sessions == nil {
		return status, nil
	}
	if value, ok, err := h.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyEnabled); err != nil {
		return status, err
	} else if ok {
		status.Enabled = automode.ParseBool(value)
	}
	if value, ok, err := h.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMode); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.State = strings.TrimSpace(text)
		}
	}
	if value, ok, err := h.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyConsecutiveTurns); err != nil {
		return status, err
	} else if ok {
		status.ConsecutiveTurns = automode.ParseInt(value, 0)
	}
	if value, ok, err := h.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMaxTurns); err != nil {
		return status, err
	} else if ok {
		status.MaxTurns = automode.ParseInt(value, h.maxTurns)
	}
	if value, ok, err := h.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyLastTurnAt); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.LastTurnAt = strings.TrimSpace(text)
		}
	}
	if value, ok, err := h.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyLastStopReason); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.LastStopReason = strings.TrimSpace(text)
		}
	}
	return automode.NormalizeWithDefault(status, h.maxTurns), nil
}
