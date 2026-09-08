// Package topic owns topic command policy and session creation orchestration.
package topic

import (
	"context"
	"fmt"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

// SessionPort defines session lifecycle dependencies required by the topic command.
type SessionPort interface {
	CreateSession(ctx context.Context, sessionCtx session.SessionContext, agentName string) error
	BaldaProviderID() string
	GetAgentMetadata(agentName string) session.AgentMetadata
}

// TopicCreator defines topic creation and rollback methods required by the topic command.
type TopicCreator interface {
	CreateTopic(ctx context.Context, locator deliverycmd.Locator, topicName string) (deliverycmd.Locator, error)
	CloseTopic(ctx context.Context, locator deliverycmd.Locator) error
}

// Handler handles the /topic command across supported transports.
type Handler struct {
	sessions   SessionPort
	channel    TopicCreator
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
}

// New creates a new topic command Handler.
func New(sessions SessionPort, channel TopicCreator, dispatcher actortransport.Dispatcher, logger zerolog.Logger) *Handler {
	return &Handler{
		sessions:   sessions,
		channel:    channel,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.topic").Logger(),
	}
}

// Name returns "topic".
func (h *Handler) Name() string { return "topic" }

// Handle processes one /topic command request.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "topic-denied")
	}

	if p.Transport == "zulip" {
		if p.Conversation.Direct {
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "This command is only available in stream messages.", "topic-stream-only")
		}
	} else {
		if !p.Conversation.Direct {
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "This command is only available in direct messages.", "topic-dm-only")
		}
	}

	topicName := strings.TrimSpace(p.Args)
	if topicName == "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "topic-usage")
	}

	if h.sessions == nil {
		return actorlayer.TransientError(fmt.Errorf("session service is required"))
	}
	baldaProviderID := strings.TrimSpace(h.sessions.BaldaProviderID())
	if baldaProviderID == "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Balda is not ready right now.", "topic-not-ready")
	}

	if h.channel == nil {
		return actorlayer.TransientError(fmt.Errorf("topic channel is required"))
	}

	topicLocator, err := h.channel.CreateTopic(ctx, p.Locator, topicName)
	if err != nil {
		h.logger.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic locator")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not create topic session.", "topic-create-failed")
	}

	if err := h.sessions.CreateSession(ctx, session.SessionContext{
		Locator: topicLocator,
		UserID:  p.Principal,
	}, topicName); err != nil {
		h.logger.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic session")
		_ = h.channel.CloseTopic(ctx, topicLocator)
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not create topic session.", "topic-session-failed")
	}

	metadata := h.sessions.GetAgentMetadata(baldaProviderID)
	welcomeMsg := welcome.BuildAgentWelcomeMessage(topicName, topicLocator.SessionID, metadata.Type, metadata.Model, metadata.ReasoningEffort, metadata.MCPServers)

	if p.Transport == "zulip" {
		if err := commandactor.SendAgentReply(ctx, h.dispatcher, env.ID, topicLocator, welcomeMsg, "topic-welcome"); err != nil {
			h.logger.Warn().Err(err).Str("topic_name", topicName).Msg("failed to send welcome to new topic")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, fmt.Sprintf("Session created for topic '%s'.", topicName), "topic-created")
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, fmt.Sprintf("Session created. Post in topic '%s' to continue.", topicName), "topic-created")
	}

	return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, topicLocator, welcomeMsg, "topic-welcome")
}

func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " topic <name>"
	}
	return "/topic <name>"
}
