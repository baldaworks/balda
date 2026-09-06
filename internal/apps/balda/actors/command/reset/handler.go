// Package reset owns reset command policy and lifecycle orchestration.
package reset

import (
	"context"
	"fmt"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type SessionPort interface {
	GetSessionInfo(ctx context.Context, sessionID string) (session.TopicSessionInfo, error)
	ResetSession(ctx context.Context, locator session.SessionLocator) error
	CreateSession(ctx context.Context, sessionCtx session.SessionContext, agentName string) error
	BaldaProviderID() string
	GetAgentMetadata(agentName string) session.AgentMetadata
	TakeStartupNotice(sessionID string) string
}

type WorkCanceller interface {
	CancelWork(ctx context.Context, locator session.SessionLocator, actor string, reason string) error
}

type Handler struct {
	sessions   SessionPort
	canceller  WorkCanceller
	dispatcher actortransport.Dispatcher
}

func New(sessions SessionPort, canceller WorkCanceller, dispatcher actortransport.Dispatcher) *Handler {
	return &Handler{sessions: sessions, canceller: canceller, dispatcher: dispatcher}
}
func (h *Handler) Name() string { return "reset" }
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "reset-denied")
	}
	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "reset-usage")
	}
	if h.sessions == nil {
		return actorlayer.TransientError(fmt.Errorf("session service is required"))
	}
	info, _ := h.sessions.GetSessionInfo(ctx, p.Locator.SessionID)
	label := strings.TrimSpace(info.AgentName)
	if label == "" {
		if p.Conversation.Direct {
			label = "balda"
		} else {
			label = "auto"
		}
	}
	userID := strings.TrimSpace(info.UserID)
	if userID == "" {
		userID = strings.TrimSpace(p.Principal)
	}
	if h.canceller != nil {
		if err := h.canceller.CancelWork(ctx, p.Locator, "command.reset", "session canceled by reset command"); err != nil {
			return actorlayer.TransientError(fmt.Errorf("cancel session work: %w", err))
		}
	}
	if err := h.sessions.ResetSession(ctx, p.Locator); err != nil {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not reset this session.", "reset-failed")
	}
	if err := h.sessions.CreateSession(ctx, session.SessionContext{Locator: p.Locator, UserID: userID}, label); err != nil {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not reset this session.", "recreate-failed")
	}
	return h.deliverResult(ctx, env.ID, p, label)
}

func (h *Handler) deliverResult(ctx context.Context, operationID string, p commandcmd.Payload, label string) error {
	metadata := h.sessions.GetAgentMetadata(strings.TrimSpace(h.sessions.BaldaProviderID()))
	displayName := label
	if !p.Conversation.Direct {
		displayName = "balda"
	}
	message := welcome.BuildAgentWelcomeMessage(displayName, p.Locator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := commandactor.SendMarkdown(ctx, h.dispatcher, operationID, p.Locator, message, "reset-welcome"); err != nil {
		return err
	}
	if notice := strings.TrimSpace(h.sessions.TakeStartupNotice(p.Locator.SessionID)); notice != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, operationID, p.Locator, notice, "reset-startup")
	}
	return nil
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " reset"
	}
	return "/reset"
}
