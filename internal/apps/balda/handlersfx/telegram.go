// Package handlersfx owns composition adapters between ingress handlers and concrete runtimes.
package handlersfx

import (
	"context"

	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/tgbotkit/runtime/events"
	runtimehandlers "github.com/tgbotkit/runtime/handlers"
	"github.com/tgbotkit/runtime/messagetype"
)

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

type telegramServerHandlerAdapter struct {
	server *baldatelegram.Server
}

func (a telegramServerHandlerAdapter) Register(registry tgbotkit.Registry) {
	a.server.Register(telegramRegistryAdapter{registry: registry})
}

func newTelegramServerHandlerAdapter(server *baldatelegram.Server) tgbotkit.Handler {
	return telegramServerHandlerAdapter{server: server}
}
