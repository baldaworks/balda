package handlers

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

const commandStart = "start"

// CommandHandler handles telegram commands by publishing them to CommandActor ingress.
type CommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           CommandChannel
	commandIngress    commandcmd.Ingress
}

type commandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           CommandChannel
	CommandIngress    commandcmd.Ingress
}

// Register registers the handler with the registry.
func (h *CommandHandler) Register(registry CommandRegistry) {
	registry.OnCommand(h.onCommand)
}

func (h *CommandHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
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
