// Package status owns the transport-neutral status command.
package status

import (
	"context"
	"fmt"
	"strings"
	"time"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/statusapp"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const (
	maxUserTextRunes = 240
	unavailableValue = "unavailable"
)

// SessionReader exposes read-only session and provider metadata.
type SessionReader interface {
	GetSessionInfo(ctx context.Context, sessionID string) (session.TopicSessionInfo, error)
}

type Coordinator interface {
	Snapshot(ctx context.Context, locator deliverycmd.Locator) statusapp.Snapshot
}

// Handler handles the /status command.
type Handler struct {
	sessions   SessionReader
	turns      appports.CurrentTurnResolver
	status     Coordinator
	registry   deliveryfmt.StructuredMessageRegistry
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
	now        func() time.Time
}

// New creates a status command handler.
func New(sessions SessionReader, turns appports.CurrentTurnResolver, status Coordinator, registry deliveryfmt.StructuredMessageRegistry, dispatcher actortransport.Dispatcher, logger zerolog.Logger) *Handler {
	return &Handler{
		sessions:   sessions,
		turns:      turns,
		status:     status,
		registry:   registry,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.status").Logger(),
		now:        time.Now,
	}
}

// Name returns "status".
func (h *Handler) Name() string { return "status" }

// Handle reports the current provider, session, and turn status.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "status-denied")
	}
	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "status-usage")
	}

	presentation, err := h.render(ctx, p)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	return commandactor.SendStructured(ctx, h.dispatcher, env.ID, p.Locator, presentation, "status-result")
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " status"
	}
	return "/status"
}

func (h *Handler) render(ctx context.Context, p commandcmd.Payload) (deliveryfmt.StructuredPresentation, error) {
	if h.sessions == nil {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("session reader is required")
	}
	if h.registry == nil {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("structured message registry is required")
	}

	response := statusfmt.Response{
		ObservedAt:      h.now().UTC(),
		ProviderID:      unavailableValue,
		ProviderType:    unavailableValue,
		Model:           unavailableValue,
		ReasoningEffort: unavailableValue,
		ChannelType:     valueOrUnavailable(p.Locator.ChannelType),
		SessionID:       valueOrUnavailable(p.Locator.SessionID),
		SessionState:    unavailableValue,
	}
	if h.status != nil {
		snapshot := h.status.Snapshot(ctx, p.Locator)
		response.ConversationState = string(snapshot.State)
		response.DegradedSources = append([]string(nil), snapshot.Degraded...)
		if snapshot.HasWait {
			response.WaitingKind = string(snapshot.Waiting.Kind)
			response.WaitingSince = snapshot.Waiting.Since
		}
		if snapshot.HasTurn {
			response.TurnState = string(snapshot.Turn.State)
			response.TurnActive = snapshot.Turn.State == turnstatuscmd.StateActive
			response.TurnStartedAt = snapshot.Turn.StartedAt
			response.LastActivityAt = snapshot.Turn.LastActivityAt
			response.ActivityKind = string(snapshot.Turn.ActivityKind)
			response.TurnCompletedAt = snapshot.Turn.CompletedAt
			response.TurnOutcome = string(snapshot.Turn.Outcome)
		}
	}

	info, err := h.sessions.GetSessionInfo(ctx, p.Locator.SessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", p.Locator.SessionID).Msg("failed to read status session metadata")
	} else {
		response.SessionState = valueOrUnavailable(info.Status)
		response.ProviderID = valueOrUnavailable(info.ProviderID)
		response.Model = valueOrUnavailable(info.Model)
	}

	if h.turns != nil {
		if turn, ok := h.turns.CurrentTurn(p.Locator.SessionID); ok {
			response.TurnActive = true
			if response.ConversationState == "" || response.ConversationState == string(statusapp.StateInactive) || response.ConversationState == string(statusapp.StateUnavailable) {
				response.ConversationState = string(statusapp.StateWorking)
			}
			if response.TurnStartedAt.IsZero() {
				response.TurnStartedAt = turn.StartedAt
			}
			if response.LastActivityAt.IsZero() {
				response.LastActivityAt = turn.StartedAt
				response.ActivityKind = string(turnstatuscmd.ActivityStart)
			}
			response.PendingTurns = turn.PendingTurns
			response.RequestExcerpt, response.RequestTruncated = truncateRunes(turn.Text, maxUserTextRunes)
		}
	}

	transport := strings.ToLower(strings.TrimSpace(p.Locator.ChannelType))
	message := deliveryfmt.StructuredEnvelope[statusfmt.Response]{Descriptor: statusfmt.ResponseDescriptor, Body: response}
	return h.registry.RenderStructured(ctx, transport, statusfmt.ResponseDescriptor.Type, message)
}

func valueOrUnavailable(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return unavailableValue
}

func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}
