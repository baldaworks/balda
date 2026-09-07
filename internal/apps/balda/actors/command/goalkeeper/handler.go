// Package goalkeeper owns the transport-neutral goalkeeper command.
package goalkeeper

import (
	"context"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const defaultMaxIterations = 20

// GoalClearer clears the active goal run for a session.
type GoalClearer interface {
	ClearGoal(ctx context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error
}

// GoalJobChecker checks if a goal job is already active for a session.
type GoalJobChecker interface {
	HasActiveGoalJob(ctx context.Context, sessionID string) (bool, error)
}

// Handler executes the goalkeeper command.
type Handler struct {
	clearer       GoalClearer
	checker       GoalJobChecker
	dispatcher    actortransport.Dispatcher
	maxIterations int
	logger        zerolog.Logger
}

// New creates a goalkeeper command handler.
func New(clearer GoalClearer, checker GoalJobChecker, dispatcher actortransport.Dispatcher, maxIterations int, logger zerolog.Logger) *Handler {
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}
	return &Handler{
		clearer:       clearer,
		checker:       checker,
		dispatcher:    dispatcher,
		maxIterations: maxIterations,
		logger:        logger.With().Str("component", "balda.actors.command.goalkeeper").Logger(),
	}
}

func (h *Handler) Name() string { return "goalkeeper" }

func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "goalkeeper-denied")
	}

	objective := strings.TrimSpace(p.Args)
	if objective == "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, usage(p), "goalkeeper-usage")
	}

	if strings.EqualFold(objective, "clear") {
		return h.handleClear(ctx, env, p)
	}

	return h.handleStart(ctx, env, p, objective)
}

func (h *Handler) handleClear(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if h.clearer == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Goal control is unavailable right now. Please try again.", "goalkeeper-clear-unavailable")
	}

	if err := h.clearer.ClearGoal(ctx, p.Locator, p.Principal, "goal cleared by user", true); err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to submit goal clear control")
		if sendErr := commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not clear goal run.", "goalkeeper-clear-failed"); sendErr != nil {
			return sendErr
		}
		return nil
	}

	return nil
}

func (h *Handler) handleStart(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload, objective string) error {
	if h.checker != nil {
		active, err := h.checker.HasActiveGoalJob(ctx, p.Locator.SessionID)
		if err != nil {
			h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to check active goal jobs")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not start goal run.", "goalkeeper-start-failed")
		}
		if active {
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "A goal run is already active for this session.", "goalkeeper-already-active")
		}
	}

	if h.dispatcher == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not start goal run.", "goalkeeper-start-unavailable")
	}

	from := actorlayer.ActorAddress{
		Target: strings.TrimSpace(p.Transport),
		Key:    strings.TrimSpace(p.Principal),
	}
	if from.Target == "" {
		from.Target = strings.TrimSpace(p.Locator.ChannelType)
	}
	if from.Key == "" {
		from.Key = strings.TrimSpace(p.Locator.AddressKey)
	}

	goalEnv, err := goalkeepercmd.JobEnvelopeWithOptions(
		from,
		p.Locator,
		deliveryfmt.NormalizeOptions(p.Presentation),
		objective,
		p.Principal,
		h.maxIterations,
	)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to build goal job envelope")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not start goal run.", "goalkeeper-start-failed")
	}

	if _, err := h.dispatcher.Dispatch(ctx, goalEnv); err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to dispatch goal job envelope")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not start goal run.", "goalkeeper-start-failed")
	}

	return nil
}

func usage(p commandcmd.Payload) string {
	root := strings.TrimSpace(p.Invocation.Root)
	if root == "" || root == "/" {
		return "Usage:\n/goalkeeper <objective>\n/goalkeeper clear"
	}
	if !strings.HasSuffix(root, " ") {
		root += " "
	}
	return "Usage:\n" + root + "goalkeeper <objective>\n" + root + "goalkeeper clear"
}
