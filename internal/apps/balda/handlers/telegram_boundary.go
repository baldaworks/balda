package handlers

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/attachment"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
)

const telegramQuestionCallbackPrefix = "balda:q:"

// TelegramMessageContext is the ingress-owned Telegram message shape.
type TelegramMessageContext struct {
	Locator          deliverycmd.Locator
	ChatID           int64
	TopicID          int
	MessageID        int
	ReplyToMessageID int
	UserID           int64
	Entities         []client.MessageEntity
	IsReply          bool
	IsForwarded      bool
	ForwardedFromBot bool
	ReplyToUserID    int64
	ReplyToIsBot     bool
	ReplyContent     string
	ForwardedContent string
	Text             string
	Attachments      []attachment.Descriptor
	HasCommand       bool
	DeliveryOptions  deliveryfmt.Options
	ProgressPolicy   deliveryfmt.ProgressPolicy
	IsDM             bool
	MediaGroupID     string
}

// TelegramCommandContext is the ingress-owned Telegram command shape.
type TelegramCommandContext struct {
	Locator         deliverycmd.Locator
	DeliveryOptions deliveryfmt.Options
	ChatID          int64
	TopicID         int
	UserID          int64
	Command         string
	Args            string
	IsDM            bool
}

// TelegramTopicLifecycleContext is the ingress-owned Telegram topic event shape.
type TelegramTopicLifecycleContext struct {
	Locator   deliverycmd.Locator
	ChatID    int64
	TopicID   int
	MessageID int
	UserID    int64
	Type      messagetype.MessageType
}

// TelegramCallbackContext is the ingress-owned Telegram question selection shape.
type TelegramCallbackContext struct {
	Locator           deliverycmd.Locator
	CallbackQueryID   string
	QuestionID        string
	OptionIndex       int
	ProviderMessageID string
	UserID            int64
}

// TelegramChannel is the consumer-owned port for Telegram intake and protocol settlement.
type TelegramChannel interface {
	MessageContextFromEvent(event *events.MessageEvent) (TelegramMessageContext, bool)
	CommandContextFromEvent(event *events.CommandEvent) (TelegramCommandContext, bool)
	TopicLifecycleFromEvent(event *events.MessageEvent) (TelegramTopicLifecycleContext, bool)
	CallbackContextFromEvent(event *events.CallbackQueryEvent) (TelegramCallbackContext, bool)
	CollectMediaGroup(message TelegramMessageContext, dispatch func(context.Context, TelegramMessageContext)) bool
	CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error)
	Close(ctx context.Context, locator deliverycmd.Locator) error
	AnswerQuestionCallback(ctx context.Context, callbackQueryID, text string, showAlert bool) error
}

// TelegramRegistry is the ingress-owned registration port used by Telegram handlers.
type TelegramRegistry interface {
	OnCommand(handler func(context.Context, *events.CommandEvent) error)
	OnMessage(handler func(context.Context, *events.MessageEvent) error)
	OnMessageType(messageType messagetype.MessageType, handler func(context.Context, *events.MessageEvent) error)
	OnCallbackDataPrefix(prefix string, handler func(context.Context, *events.CallbackQueryEvent) error)
}

// TelegramHandler registers provider protocol callbacks without owning the runtime registry.
type TelegramHandler interface {
	Register(registry TelegramRegistry)
}

// TelegramAttachmentStore persists inbound Telegram attachment descriptors.
type TelegramAttachmentStore interface {
	PersistTelegram(ctx context.Context, descriptors []attachment.Descriptor) ([]attachment.Descriptor, error)
}
