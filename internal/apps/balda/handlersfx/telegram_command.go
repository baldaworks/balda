package handlersfx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/runtime/events"
	runtimehandlers "github.com/tgbotkit/runtime/handlers"
	"go.uber.org/fx"
)

const (
	chatTypePrivate = "private"
)

// telegramStartHandler handles Telegram /start commands by publishing them to CommandActor.
type telegramStartHandler struct {
	ownerStore     *auth.OwnerStore
	commandIngress commandcmd.Ingress
	logger         zerolog.Logger
}

type telegramStartHandlerParams struct {
	fx.In

	OwnerStore     *auth.OwnerStore   `optional:"true"`
	CommandIngress commandcmd.Ingress `optional:"true"`
	Logger         zerolog.Logger
}

func newTelegramStartHandler(params telegramStartHandlerParams) *telegramStartHandler {
	return &telegramStartHandler{
		ownerStore:     params.OwnerStore,
		commandIngress: params.CommandIngress,
		logger:         params.Logger.With().Str("component", "balda.handlersfx.telegram_start").Logger(),
	}
}

// Register registers the /start handler with the tgbotkit registry.
func (h *telegramStartHandler) Register(registry tgbotkit.Registry) {
	registry.OnCommand(runtimehandlers.CommandHandler(h.onCommand))
}

func (h *telegramStartHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
	if event.Command != commandStart {
		return nil
	}
	if event.Message == nil || event.Message.Chat.Type != chatTypePrivate {
		return nil
	}

	chatID := event.Message.Chat.Id
	userID := int64(0)
	if event.Message.From != nil {
		userID = event.Message.From.Id
	}
	args := strings.TrimSpace(event.Args)

	h.logger.Debug().
		Int64("user_id", userID).
		Int64("chat_id", chatID).
		Msg("Start command received")

	if h.commandIngress == nil {
		return nil
	}

	isOwner := h.ownerStore != nil && h.ownerStore.IsOwner(userID)
	return h.commandIngress.PublishCommand(ctx, commandcmd.Request{
		InvocationID: fmt.Sprintf("telegram:command:%d:%d", chatID, event.Message.MessageId),
		Payload: commandcmd.Payload{
			Version:      commandcmd.SchemaVersion,
			Name:         commandStart,
			Args:         args,
			Locator:      telegramref.NewLocator(chatID, 0),
			Transport:    telegramref.ChannelType,
			Principal:    telegramref.UserID(userID),
			Access:       commandcmd.Access{SessionCommands: true, Owner: isOwner, Collaborator: !isOwner},
			Conversation: commandcmd.Conversation{Direct: true},
			Presentation: deliveryfmt.Options{
				DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
				ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, PlanUpdates: true},
			},
			Invocation: commandcmd.Invocation{Root: "/"},
		},
	})
}

// telegramCommandHandler handles general Telegram commands by publishing them to CommandActor.
type telegramCommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           baldatelegram.Channel
	commandIngress    commandcmd.Ingress
	logger            zerolog.Logger
}

type telegramCommandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore            `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore     `optional:"true"`
	Channel           *baldatelegram.Adapter      `optional:"true"`
	CommandIngress    commandcmd.Ingress          `optional:"true"`
	Logger            zerolog.Logger
}

func newTelegramCommandHandler(params telegramCommandHandlerParams) *telegramCommandHandler {
	var ch baldatelegram.Channel
	if params.Channel != nil {
		ch = params.Channel
	}
	return &telegramCommandHandler{
		ownerStore:        params.OwnerStore,
		collaboratorStore: params.CollaboratorStore,
		channel:           ch,
		commandIngress:    params.CommandIngress,
		logger:            params.Logger.With().Str("component", "balda.handlersfx.telegram_command").Logger(),
	}
}

// Register registers the command handler with the tgbotkit registry.
func (h *telegramCommandHandler) Register(registry tgbotkit.Registry) {
	registry.OnCommand(runtimehandlers.CommandHandler(h.onCommand))
}

func (h *telegramCommandHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
	if h.channel == nil {
		return nil
	}
	commandCtx, ok := h.channel.CommandContextFromEvent(event)
	if !ok {
		return nil
	}
	if commandCtx.Command == commandStart {
		return nil
	}
	if h.commandIngress == nil {
		return nil
	}
	allowed := h.canUseSessionCommand(ctx, commandCtx.UserID)
	isOwner := h.ownerStore != nil && h.ownerStore.IsOwner(commandCtx.UserID)
	return h.commandIngress.PublishCommand(ctx, commandcmd.Request{
		InvocationID: fmt.Sprintf("telegram:command:%d:%d", commandCtx.ChatID, commandCtx.MessageID),
		Payload: commandcmd.Payload{
			Version:      commandcmd.SchemaVersion,
			Name:         commandCtx.Command,
			Args:         commandCtx.Args,
			Locator:      commandCtx.Locator,
			Transport:    commandCtx.Locator.ChannelType,
			Principal:    telegramref.UserID(commandCtx.UserID),
			Access:       commandcmd.Access{SessionCommands: allowed, Owner: isOwner, Collaborator: allowed && !isOwner},
			Conversation: commandcmd.Conversation{Direct: commandCtx.IsDM},
			Presentation: commandCtx.DeliveryOptions,
			Invocation:   commandcmd.Invocation{Root: "/"},
		},
	})
}

func (h *telegramCommandHandler) canUseSessionCommand(ctx context.Context, userID int64) bool {
	if h.ownerStore != nil && h.ownerStore.IsOwner(userID) {
		return true
	}
	if h.collaboratorStore == nil {
		return false
	}
	_, found, err := h.collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	if err != nil {
		h.logger.Warn().Err(err).Int64("user_id", userID).Msg("failed to check collaborator access")
		return false
	}
	return found
}
