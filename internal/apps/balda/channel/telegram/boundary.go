package telegram

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
)

type Channel interface {
	MessageContextFromEvent(event *events.MessageEvent) (MessageContext, bool)
	CommandContextFromEvent(event *events.CommandEvent) (CommandContext, bool)
	TopicLifecycleFromEvent(event *events.MessageEvent) (TopicLifecycleContext, bool)
	CallbackContextFromEvent(event *events.CallbackQueryEvent) (CallbackContext, bool)
	CollectMediaGroup(message MessageContext, dispatch func(context.Context, MessageContext)) bool
	CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error)
	Close(ctx context.Context, locator deliverycmd.Locator) error
	AnswerQuestionCallback(ctx context.Context, callbackQueryID, text string, showAlert bool) error
}

type Registry interface {
	OnCommand(handler func(context.Context, *events.CommandEvent) error)
	OnMessage(handler func(context.Context, *events.MessageEvent) error)
	OnMessageType(messageType messagetype.MessageType, handler func(context.Context, *events.MessageEvent) error)
	OnCallbackDataPrefix(prefix string, handler func(context.Context, *events.CallbackQueryEvent) error)
}

type Handler interface {
	Register(registry Registry)
}

type AttachmentStore interface {
	PersistTelegram(ctx context.Context, descriptors []attachment.Descriptor) ([]attachment.Descriptor, error)
}
