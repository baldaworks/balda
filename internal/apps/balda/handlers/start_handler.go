package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

const chatTypePrivate = "private"

// StartHandler handles /start command for telegram by publishing it to CommandActor ingress.
type StartHandler struct {
	ownerStore     *auth.OwnerStore
	commandIngress commandcmd.Ingress
}

type startHandlerParams struct {
	fx.In

	OwnerStore     *auth.OwnerStore   `optional:"true"`
	CommandIngress commandcmd.Ingress `optional:"true"`
}

// Register registers the handler with the registry.
func (h *StartHandler) Register(registry CommandRegistry) {
	registry.OnCommand(h.onCommand)
}

func (h *StartHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
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

	log.Debug().
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
			Presentation: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown, ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, PlanUpdates: true}},
			Invocation:   commandcmd.Invocation{Root: "/"},
		},
	})
}
