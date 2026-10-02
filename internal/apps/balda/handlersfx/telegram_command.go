package handlersfx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/authpayload"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
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
	bindings         bindingInvitationAdmitter
	bindingChannels  bindingChannelRegistry
	inbound          *telegramInboundHandler
	dispatcher       actortransport.Dispatcher
	ownerStore       *auth.OwnerStore
	telegramProfiles *auth.TelegramProfileService
	commandIngress   commandcmd.Ingress
	logger           zerolog.Logger
}

type telegramStartHandlerParams struct {
	fx.In
	Bindings        bindingInvitationAdmitter `optional:"true"`
	BindingChannels bindingChannelRegistry    `optional:"true"`
	Inbound         *telegramInboundHandler   `optional:"true"`
	Dispatcher      actortransport.Dispatcher `optional:"true"`

	OwnerStore       *auth.OwnerStore             `optional:"true"`
	TelegramProfiles *auth.TelegramProfileService `optional:"true"`
	CommandIngress   commandcmd.Ingress           `optional:"true"`
	Logger           zerolog.Logger
}

func newTelegramStartHandler(params telegramStartHandlerParams) *telegramStartHandler {
	return &telegramStartHandler{
		bindings: params.Bindings, bindingChannels: params.BindingChannels, inbound: params.Inbound, dispatcher: params.Dispatcher,
		ownerStore:       params.OwnerStore,
		telegramProfiles: params.TelegramProfiles,
		commandIngress:   params.CommandIngress,
		logger:           params.Logger.With().Str("component", "balda.handlersfx.telegram_start").Logger(),
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
	if authpayload.Contains(args) {
		if h.bindings == nil || h.bindingChannels == nil {
			return nil
		}
		payload, exact := authpayload.Parse(args)
		info, available := h.bindingChannels.Get("telegram")
		if !exact || !available || info.Integration.Key == "" || event.Message.From == nil {
			return sendPlain(ctx, h.dispatcher, serverActorAddress, telegramref.NewLocator(chatID, 0), "Could not connect this account. Copy the complete invitation from Backoffice and try again.")
		}
		username := ""
		if event.Message.From.Username != nil {
			username = *event.Message.From.Username
		}
		_, err := h.bindings.Consume(ctx, usercmd.BindingProof{Payload: payload, Integration: info.Integration, Principal: strconv.FormatInt(userID, 10), Direct: true, Locator: telegramref.NewLocator(chatID, 0), DisplayName: event.Message.From.FirstName, ProviderUsername: username, ProviderFirstName: event.Message.From.FirstName, Provenance: "chat_id=" + strconv.FormatInt(chatID, 10)})
		reply := "Account connected. Refresh bindings in Backoffice."
		if err != nil {
			reply = "Could not connect this account. Check or replace the invitation in Backoffice."
		}
		if err == nil && h.inbound != nil {
			h.inbound.activateBoundPrimary(ctx, userID, chatID)
		}
		return sendPlain(ctx, h.dispatcher, serverActorAddress, telegramref.NewLocator(chatID, 0), reply)
	}
	if args == "" && h.bindings != nil && h.inbound != nil && !h.inbound.canAccessCollaboratorScope(ctx, userID) {
		return sendPlain(ctx, h.dispatcher, serverActorAddress, telegramref.NewLocator(chatID, 0), "Open Backoffice Access, select your user and create a Telegram invitation. Open its bot link or send the invitation payload here.")
	}

	h.logger.Debug().
		Int64("user_id", userID).
		Int64("chat_id", chatID).
		Msg("Start command received")

	if h.commandIngress == nil {
		return nil
	}

	isOwner := h.ownerStore != nil && h.ownerStore.IsOwner(userID)
	if isOwner && event.Message.From != nil {
		username := ""
		if event.Message.From.Username != nil {
			username = *event.Message.From.Username
		}
		if err := h.telegramProfiles.Refresh(ctx, userID, username, event.Message.From.FirstName); err != nil {
			h.logger.Warn().Err(err).Int64("user_id", userID).Msg("failed to refresh telegram binding profile")
		}
	}
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
	telegramProfiles  *auth.TelegramProfileService
	collaboratorStore *auth.CollaboratorStore
	channel           baldatelegram.Channel
	commandIngress    commandcmd.Ingress
	logger            zerolog.Logger
}

type telegramCommandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore             `optional:"true"`
	TelegramProfiles  *auth.TelegramProfileService `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore      `optional:"true"`
	Channel           *baldatelegram.Adapter       `optional:"true"`
	CommandIngress    commandcmd.Ingress           `optional:"true"`
	Logger            zerolog.Logger
}

func newTelegramCommandHandler(params telegramCommandHandlerParams) *telegramCommandHandler {
	var ch baldatelegram.Channel
	if params.Channel != nil {
		ch = params.Channel
	}
	return &telegramCommandHandler{
		ownerStore:        params.OwnerStore,
		telegramProfiles:  params.TelegramProfiles,
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
	if authpayload.Contains(commandCtx.Args) {
		return nil
	}
	if h.commandIngress == nil {
		return nil
	}
	allowed := h.canUseSessionCommand(ctx, commandCtx.UserID)
	if allowed {
		if err := h.telegramProfiles.Refresh(ctx, commandCtx.UserID, commandCtx.Username, commandCtx.FirstName); err != nil {
			h.logger.Warn().Err(err).Int64("user_id", commandCtx.UserID).Msg("failed to refresh telegram binding profile")
		}
	}
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
	_, found, err := h.collaboratorStore.GetCollaborator(ctx, auth.TelegramSubject(userID))
	if err != nil {
		h.logger.Warn().Err(err).Int64("user_id", userID).Msg("failed to check collaborator access")
		return false
	}
	return found
}
