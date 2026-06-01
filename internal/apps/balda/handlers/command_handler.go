package handlers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/actors"
	"github.com/normahq/balda/internal/apps/balda/auth"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"github.com/normahq/balda/internal/apps/balda/welcome"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

type commandSessionManager interface {
	CreateSession(ctx context.Context, sessionCtx session.SessionContext, agentName string) error
	GetAgentMetadata(agentName string) session.AgentMetadata
	BaldaProviderID() string
	ResetSession(ctx context.Context, locator session.SessionLocator) error
}

// CommandHandler handles Balda chat commands such as /topic, /goal, /close,
// /cancel, /restart, and /user.
type CommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           *baldatelegram.Adapter
	sessionManager    commandSessionManager
	actorDispatcher   swarm.ActorDispatcher
	goalMaxIterations int
	userHandler       *userHandler
	requestRestart    func() error
}

type commandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           *baldatelegram.Adapter
	SessionManager    *session.Manager
	ActorDispatcher   swarm.ActorDispatcher
	MaxIterations     int `name:"balda_goal_max_iterations"`
	UserHandler       *userHandler
}

// Register registers the handler with the registry.
func (h *CommandHandler) Register(registry tgbotkit.Registry) {
	registry.OnCommand(h.onCommand)
}

func (h *CommandHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
	commandCtx, ok := h.channel.CommandContextFromEvent(event)
	if !ok {
		return nil
	}

	switch commandCtx.Command {
	case "topic":
		return h.onTopicCommand(ctx, commandCtx)
	case "close":
		return h.onCloseCommand(ctx, commandCtx)
	case "cancel":
		return h.onCancelCommand(ctx, commandCtx)
	case "goal":
		return h.onGoalCommand(ctx, commandCtx)
	case "restart":
		return h.onRestartCommand(ctx, commandCtx)
	case "user":
		// Route to UserHandler
		return h.userHandler.HandleUserCommand(ctx, commandCtx)
	default:
		return nil
	}
}

func (h *CommandHandler) onGoalCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendAgentReply(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	objective := strings.TrimSpace(commandCtx.Args)
	if objective == "" {
		if err := h.channel.SendAgentReply(ctx, commandCtx.Locator, "Usage: /goal <objective>"); err != nil {
			return err
		}
		return nil
	}

	started, err := h.submitGoalTask(ctx, commandCtx.Locator, objective, baldatelegram.UserID(commandCtx.UserID))
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to start /goal run")
		if sendErr := h.channel.SendAgentReply(ctx, commandCtx.Locator, "Could not start goal run."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if !started {
		if err := h.channel.SendAgentReply(ctx, commandCtx.Locator, "A goal run is already active for this session."); err != nil {
			return err
		}
		return nil
	}

	return nil
}

func (h *CommandHandler) onTopicCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	topicName := strings.TrimSpace(commandCtx.Args)
	if topicName == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /topic <name>"); err != nil {
			return err
		}
		return nil
	}
	baldaProviderID := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if baldaProviderID == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Balda is not ready right now."); err != nil {
			return err
		}
		return nil
	}

	log.Info().
		Int64("user_id", commandCtx.UserID).
		Int64("chat_id", commandCtx.ChatID).
		Str("topic_name", topicName).
		Msg("creating topic session")

	topicLocator, err := h.channel.CreateTopicLocator(ctx, commandCtx.ChatID, fmt.Sprintf("Balda: %s", topicName))
	if err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not create topic session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.sessionManager.CreateSession(ctx, session.SessionContext{
		Locator: topicLocator,
		UserID:  baldatelegram.UserID(commandCtx.UserID),
	}, topicName); err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic session after topic creation")
		_ = h.channel.Close(ctx, topicLocator)
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not create topic session."); sendErr != nil {
			return sendErr
		}
		return nil
	}

	metadata := h.sessionManager.GetAgentMetadata(baldaProviderID)

	welcomeMsg := welcome.BuildAgentWelcomeMessage(topicName, topicLocator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := h.channel.SendMarkdown(ctx, topicLocator, welcomeMsg); err != nil {
		log.Error().Err(err).Msg("failed to send welcome message")
		return err
	}

	return nil
}

func (h *CommandHandler) onCloseCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /close"); err != nil {
			return err
		}
		return nil
	}

	if commandCtx.TopicID > 0 {
		if err := submitSessionCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, baldatelegram.UserID(commandCtx.UserID), "session canceled by close command", false); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish /close cancel control command")
		}
		if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset session during /close")
			if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not close this topic."); sendErr != nil {
				return sendErr
			}
			return nil
		}
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Closing this topic and resetting session history."); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to send /close confirmation")
		}
		if err := h.channel.Close(ctx, commandCtx.Locator); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to close topic")
		}
		return nil
	}

	if err := submitSessionCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, baldatelegram.UserID(commandCtx.UserID), "session canceled by close command", false); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish /close cancel control command")
	}
	if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset main dm session during /close")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not reset this session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Session history reset."); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Msg("failed to send /close main dm session confirmation")
	}
	return nil
}

func (h *CommandHandler) onCancelCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /cancel"); err != nil {
			return err
		}
		return nil
	}

	if h.actorDispatcher == nil {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cancel is unavailable right now. Please try again."); err != nil {
			return err
		}
		return nil
	}
	env, err := actors.ControlCancelEnvelope(commandCtx.Locator, "", baldatelegram.UserID(commandCtx.UserID), "session canceled by user")
	if err != nil {
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not request cancel."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if _, err := h.actorDispatcher.Dispatch(ctx, env); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish cancel command")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not request cancel."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cancel requested."); err != nil {
		return err
	}
	return nil
}

func (h *CommandHandler) onRestartCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.isOwner(commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner can use this command."); err != nil {
			return err
		}
		return nil
	}
	if !commandCtx.IsDM {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /restart"); err != nil {
			return err
		}
		return nil
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Restart requested. Shutting down now."); err != nil {
		return err
	}
	restart := h.requestRestart
	if restart == nil {
		restart = requestSelfRestart
	}
	if err := restart(); err != nil {
		log.Warn().Err(err).Msg("failed to trigger self restart")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Could not trigger restart."); sendErr != nil {
			return sendErr
		}
	}
	return nil
}

func (h *CommandHandler) canUseSessionCommand(ctx context.Context, userID int64) bool {
	if h.isOwner(userID) {
		return true
	}
	if h.collaboratorStore == nil {
		return false
	}
	_, found, err := h.collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	if err != nil {
		log.Warn().Err(err).Int64("user_id", userID).Msg("failed to check collaborator access")
		return false
	}
	return found
}

func (h *CommandHandler) isOwner(userID int64) bool {
	return h.ownerStore != nil && h.ownerStore.IsOwner(userID)
}

func requestSelfRestart() error {
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		return fmt.Errorf("find current process: %w", err)
	}
	if err := self.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal current process: %w", err)
	}
	return nil
}
