package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/relay/internal/apps/relay/auth"
	relaytelegram "github.com/normahq/relay/internal/apps/relay/channel/telegram"
	"github.com/normahq/relay/internal/apps/relay/messenger"
	"github.com/normahq/relay/internal/apps/relay/session"
	"github.com/normahq/relay/internal/apps/relay/tgbotkit"
	relaywelcome "github.com/normahq/relay/internal/apps/relay/welcome"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

type commandSessionManager interface {
	CreateSession(ctx context.Context, sessionCtx session.SessionContext, agentName string) error
	GetAgentMetadata(agentName string) session.AgentMetadata
	RelayProviderID() string
	StopSession(locator session.SessionLocator)
}

// CommandHandler handles relay commands like /topic and /close.
type CommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           *relaytelegram.Adapter
	sessionManager    commandSessionManager
	turnDispatcher    turnQueue
	messenger         *messenger.Messenger
	userHandler       *userHandler
}

func BuildAgentWelcomeMessage(name, sessionID, agentType, model string, mcpServers []string) string {
	return relaywelcome.BuildAgentWelcomeMessage(name, sessionID, agentType, model, mcpServers)
}

type commandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           *relaytelegram.Adapter
	SessionManager    *session.Manager
	TurnDispatcher    *TurnDispatcher
	Messenger         *messenger.Messenger
	UserHandler       *userHandler
}

// NewCommandHandler creates a new relay command handler.
func NewCommandHandler(params commandHandlerParams) *CommandHandler {
	return &CommandHandler{
		ownerStore:        params.OwnerStore,
		collaboratorStore: params.CollaboratorStore,
		channel:           params.Channel,
		sessionManager:    params.SessionManager,
		turnDispatcher:    params.TurnDispatcher,
		messenger:         params.Messenger,
		userHandler:       params.UserHandler,
	}
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
	case "user":
		// Route to UserHandler
		return h.userHandler.HandleUserCommand(ctx, commandCtx)
	default:
		return nil
	}
}

func (h *CommandHandler) onTopicCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
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
	relayProviderID := strings.TrimSpace(h.sessionManager.RelayProviderID())
	if relayProviderID == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "relay.provider is not configured."); err != nil {
			return err
		}
		return nil
	}

	log.Info().
		Int64("user_id", commandCtx.UserID).
		Int64("chat_id", commandCtx.ChatID).
		Str("topic_name", topicName).
		Msg("creating topic session")

	topicLocator, err := h.channel.CreateTopicLocator(ctx, commandCtx.ChatID, fmt.Sprintf("Relay: %s", topicName))
	if err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to create topic session: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.sessionManager.CreateSession(ctx, session.SessionContext{
		Locator: topicLocator,
		UserID:  session.TelegramUserID(commandCtx.UserID),
	}, topicName); err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic session after topic creation")
		_ = h.channel.Close(ctx, topicLocator)
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to create topic session: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}

	metadata := h.sessionManager.GetAgentMetadata(relayProviderID)

	welcomeMsg := BuildAgentWelcomeMessage(topicName, topicLocator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := h.channel.SendMarkdown(ctx, topicLocator, welcomeMsg); err != nil {
		log.Error().Err(err).Msg("failed to send welcome message")
		return err
	}

	return nil
}

func (h *CommandHandler) onCloseCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
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
		if h.turnDispatcher != nil {
			_, _, _ = h.turnDispatcher.CancelSession(commandCtx.Locator, true)
		}
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Closing this topic and stopping agent session."); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to send /close confirmation")
		}
		if err := h.channel.Close(ctx, commandCtx.Locator); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to close topic")
		}
		h.sessionManager.StopSession(commandCtx.Locator)
		return nil
	}

	if h.turnDispatcher != nil {
		_, _, _ = h.turnDispatcher.CancelSession(commandCtx.Locator, true)
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Stopping relay provider session. It will be recreated on your next message."); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Msg("failed to send /close owner session confirmation")
	}
	h.sessionManager.StopSession(commandCtx.Locator)
	return nil
}

func (h *CommandHandler) onCancelCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
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

	if h.turnDispatcher == nil {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cancel is unavailable right now. Please try again."); err != nil {
			return err
		}
		return nil
	}

	hadInFlight, dropped, err := h.turnDispatcher.CancelSession(commandCtx.Locator, true)
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to cancel session turns")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to cancel current turn: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}

	if !hadInFlight && dropped == 0 {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "No running or queued turns for this session."); err != nil {
			return err
		}
		return nil
	}

	response := "Canceled current turn."
	if !hadInFlight {
		response = "No running turn to cancel."
	}
	if dropped > 0 {
		response = fmt.Sprintf("%s Dropped %d queued message(s).", response, dropped)
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, response); err != nil {
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
