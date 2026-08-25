// Package handlersfx owns composition adapters between ingress handlers and concrete runtimes.
package handlersfx

import (
	"context"

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

func (a telegramChannelAdapter) CommandContextFromEvent(event *events.CommandEvent) (handlers.CommandContext, bool) {
	command, ok := a.channel.CommandContextFromEvent(event)
	if !ok {
		return handlers.CommandContext{}, false
	}
	return handlers.CommandContext(command), true
}

func (a telegramChannelAdapter) CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error) {
	return a.channel.CreateTopicLocator(ctx, chatID, topicName)
}

func (a telegramChannelAdapter) Close(ctx context.Context, locator deliverycmd.Locator) error {
	return a.channel.Close(ctx, locator)
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

type telegramCommandHandler interface {
	Register(registry handlers.CommandRegistry)
}

type telegramCommandHandlerAdapter struct {
	handler telegramCommandHandler
}

func (a telegramCommandHandlerAdapter) Register(registry tgbotkit.Registry) {
	a.handler.Register(telegramRegistryAdapter{registry: registry})
}

func newTelegramCommandHandlerAdapter(handler telegramCommandHandler) tgbotkit.Handler {
	return telegramCommandHandlerAdapter{handler: handler}
}

type telegramServerHandlerAdapter struct {
	server *baldatelegram.Server
}

func (a telegramServerHandlerAdapter) Register(registry tgbotkit.Registry) {
	a.server.Register(telegramRegistryAdapter{registry: registry})
}

func newTelegramServerHandlerAdapter(server *baldatelegram.Server) tgbotkit.Handler {
	return telegramServerHandlerAdapter{server: server}
}

func newTelegramChannelAdapter(channel *baldatelegram.Adapter) handlers.CommandChannel {
	return telegramChannelAdapter{channel: channel}
}
