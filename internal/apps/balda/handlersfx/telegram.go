// Package handlersfx owns composition adapters between ingress handlers and concrete runtimes.
package handlersfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/attachmentstore"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/handlers"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/tgbotkit/runtime/events"
	runtimehandlers "github.com/tgbotkit/runtime/handlers"
	"github.com/tgbotkit/runtime/messagetype"
)

type telegramChannelAdapter struct {
	channel *baldatelegram.Adapter
}

func (a telegramChannelAdapter) MessageContextFromEvent(event *events.MessageEvent) (handlers.TelegramMessageContext, bool) {
	message, ok := a.channel.MessageContextFromEvent(event)
	return handlers.TelegramMessageContext(message), ok
}

func (a telegramChannelAdapter) CommandContextFromEvent(event *events.CommandEvent) (handlers.TelegramCommandContext, bool) {
	command, ok := a.channel.CommandContextFromEvent(event)
	return handlers.TelegramCommandContext(command), ok
}

func (a telegramChannelAdapter) TopicLifecycleFromEvent(event *events.MessageEvent) (handlers.TelegramTopicLifecycleContext, bool) {
	lifecycle, ok := a.channel.TopicLifecycleFromEvent(event)
	return handlers.TelegramTopicLifecycleContext(lifecycle), ok
}

func (a telegramChannelAdapter) CallbackContextFromEvent(event *events.CallbackQueryEvent) (handlers.TelegramCallbackContext, bool) {
	callback, ok := a.channel.CallbackContextFromEvent(event)
	return handlers.TelegramCallbackContext(callback), ok
}

func (a telegramChannelAdapter) CollectMediaGroup(message handlers.TelegramMessageContext, dispatch func(context.Context, handlers.TelegramMessageContext)) bool {
	return a.channel.CollectMediaGroup(baldatelegram.MessageContext(message), func(ctx context.Context, grouped baldatelegram.MessageContext) {
		dispatch(ctx, handlers.TelegramMessageContext(grouped))
	})
}

func (a telegramChannelAdapter) CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error) {
	return a.channel.CreateTopicLocator(ctx, chatID, topicName)
}

func (a telegramChannelAdapter) Close(ctx context.Context, locator deliverycmd.Locator) error {
	return a.channel.Close(ctx, locator)
}

func (a telegramChannelAdapter) AnswerQuestionCallback(ctx context.Context, callbackQueryID, text string, showAlert bool) error {
	return a.channel.AnswerQuestionCallback(ctx, callbackQueryID, text, showAlert)
}

type telegramRegistryAdapter struct {
	registry tgbotkit.Registry
}

func (a telegramRegistryAdapter) OnCommand(handler func(context.Context, *events.CommandEvent) error) {
	a.registry.OnCommand(runtimehandlers.CommandHandler(handler))
}

func (a telegramRegistryAdapter) OnMessage(handler func(context.Context, *events.MessageEvent) error) {
	a.registry.OnMessage(runtimehandlers.MessageHandler(handler))
}

func (a telegramRegistryAdapter) OnMessageType(messageType messagetype.MessageType, handler func(context.Context, *events.MessageEvent) error) {
	a.registry.OnMessageType(messageType, runtimehandlers.MessageHandler(handler))
}

func (a telegramRegistryAdapter) OnCallbackDataPrefix(prefix string, handler func(context.Context, *events.CallbackQueryEvent) error) {
	a.registry.OnCallbackDataPrefix(prefix, runtimehandlers.CallbackQueryHandler(handler))
}

type telegramHandlerAdapter struct {
	handler handlers.TelegramHandler
}

func (a telegramHandlerAdapter) Register(registry tgbotkit.Registry) {
	a.handler.Register(telegramRegistryAdapter{registry: registry})
}

func newTelegramHandlerAdapter(handler handlers.TelegramHandler) tgbotkit.Handler {
	return telegramHandlerAdapter{handler: handler}
}

func newTelegramChannelAdapter(channel *baldatelegram.Adapter) handlers.TelegramChannel {
	return telegramChannelAdapter{channel: channel}
}

func newTelegramAttachmentStore(store attachmentstore.Store) handlers.TelegramAttachmentStore {
	return store
}
