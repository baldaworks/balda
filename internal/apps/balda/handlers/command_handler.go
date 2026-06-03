package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/auth"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"github.com/normahq/balda/internal/apps/balda/welcome"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
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

type goalTaskService interface {
	ListActiveGoalTasksBySession(ctx context.Context, sessionID string) ([]baldastate.SwarmTaskRecord, error)
}

// CommandHandler handles Balda chat commands such as /topic, /goal, /reset,
// /restart, /close, /cancel, and /user.
type CommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           *baldatelegram.Adapter
	sessionManager    commandSessionManager
	actorDispatcher   actortransport.Dispatcher
	taskService       goalTaskService
	goalMaxIterations int
	userHandler       *userHandler
}

type commandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           *baldatelegram.Adapter
	SessionManager    *session.Manager
	ActorDispatcher   actortransport.Dispatcher
	TaskService       *swarm.TaskService `optional:"true"`
	MaxIterations     int                `name:"balda_goal_max_iterations"`
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
	case "reset", "restart":
		return h.onResetCommand(ctx, commandCtx)
	case "close":
		return h.onCloseCommand(ctx, commandCtx)
	case "cancel":
		return h.onCancelCommand(ctx, commandCtx)
	case "goal":
		return h.onGoalCommand(ctx, commandCtx)
	case "user":
		// Route to UserHandler
		return h.userHandler.HandleUserCommand(ctx, commandCtx)
	default:
		return nil
	}
}

func (h *CommandHandler) onGoalCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	objective := strings.TrimSpace(commandCtx.Args)
	if objective == "" {
		if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage:\n/goal <objective>\n/goal clear"); err != nil {
			return err
		}
		return nil
	}
	if strings.EqualFold(objective, "clear") {
		if h.actorDispatcher == nil {
			if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Goal control is unavailable right now. Please try again."); err != nil {
				return err
			}
			return nil
		}
		if err := submitGoalClearControl(ctx, h.actorDispatcher, commandCtx.Locator, baldatelegram.UserID(commandCtx.UserID), "goal cleared by user", true); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish goal clear command")
			if sendErr := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not clear goal run."); sendErr != nil {
				return sendErr
			}
		}
		return nil
	}

	started, err := h.submitGoalTask(ctx, commandCtx.Locator, objective, baldatelegram.UserID(commandCtx.UserID))
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to start /goal run")
		if sendErr := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not start goal run."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if !started {
		if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "A goal run is already active for this session."); err != nil {
			return err
		}
		return nil
	}

	return nil
}

func (h *CommandHandler) onResetCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	commandName := commandCtx.Command
	if commandName == "" {
		commandName = "reset"
	}

	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, fmt.Sprintf("Usage: /%s", commandName)); err != nil {
			return err
		}
		return nil
	}

	if err := submitSessionCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, baldatelegram.UserID(commandCtx.UserID), fmt.Sprintf("session canceled by %s command", commandName), false); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Str("command", commandName).Msg("failed to publish session cancel control command")
	}
	if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Str("command", commandName).Msg("failed to reset session during session restart command")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not reset this session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Session restarted."); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Str("command", commandName).Msg("failed to send restart confirmation")
	}
	return nil
}

func (h *CommandHandler) onTopicCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	topicName := strings.TrimSpace(commandCtx.Args)
	if topicName == "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /topic <name>"); err != nil {
			return err
		}
		return nil
	}
	baldaProviderID := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if baldaProviderID == "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Balda is not ready right now."); err != nil {
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
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not create topic session."); sendErr != nil {
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
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not create topic session."); sendErr != nil {
			return sendErr
		}
		return nil
	}

	metadata := h.sessionManager.GetAgentMetadata(baldaProviderID)

	welcomeMsg := welcome.BuildAgentWelcomeMessage(topicName, topicLocator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, topicLocator, welcomeMsg); err != nil {
		log.Error().Err(err).Msg("failed to send welcome message")
		return err
	}

	return nil
}

func (h *CommandHandler) onCloseCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /close"); err != nil {
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
			if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not close this topic."); sendErr != nil {
				return sendErr
			}
			return nil
		}
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Closing this topic and resetting session history."); err != nil {
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
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not reset this session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Session history reset."); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Msg("failed to send /close main dm session confirmation")
	}
	return nil
}

func (h *CommandHandler) onCancelCommand(ctx context.Context, commandCtx baldatelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /cancel"); err != nil {
			return err
		}
		return nil
	}

	if h.actorDispatcher == nil {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Cancel is unavailable right now. Please try again."); err != nil {
			return err
		}
		return nil
	}
	if err := submitSessionTurnCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, baldatelegram.UserID(commandCtx.UserID), "session turn canceled by user", true); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish cancel command")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not request cancel."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Cancel requested."); err != nil {
		return err
	}
	return nil
}

func (h *CommandHandler) canUseSessionCommand(ctx context.Context, userID int64) bool {
	if h.ownerStore != nil && h.ownerStore.IsOwner(userID) {
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
