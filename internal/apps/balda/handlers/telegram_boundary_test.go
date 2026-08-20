package handlers

import (
	"context"

	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/tgbotkit/runtime/events"
)

// testTelegramChannel keeps concrete Telegram integration in test composition
// while production handlers depend only on their local port.
type testTelegramChannel struct {
	*baldatelegram.Adapter
}

func (a *testTelegramChannel) MessageContextFromEvent(event *events.MessageEvent) (TelegramMessageContext, bool) {
	message, ok := a.Adapter.MessageContextFromEvent(event)
	return TelegramMessageContext(message), ok
}

func (a *testTelegramChannel) CommandContextFromEvent(event *events.CommandEvent) (TelegramCommandContext, bool) {
	command, ok := a.Adapter.CommandContextFromEvent(event)
	return TelegramCommandContext(command), ok
}

func (a *testTelegramChannel) TopicLifecycleFromEvent(event *events.MessageEvent) (TelegramTopicLifecycleContext, bool) {
	lifecycle, ok := a.Adapter.TopicLifecycleFromEvent(event)
	return TelegramTopicLifecycleContext(lifecycle), ok
}

func (a *testTelegramChannel) CallbackContextFromEvent(event *events.CallbackQueryEvent) (TelegramCallbackContext, bool) {
	callback, ok := a.Adapter.CallbackContextFromEvent(event)
	return TelegramCallbackContext(callback), ok
}

func (a *testTelegramChannel) CollectMediaGroup(message TelegramMessageContext, dispatch func(context.Context, TelegramMessageContext)) bool {
	return a.Adapter.CollectMediaGroup(baldatelegram.MessageContext(message), func(ctx context.Context, grouped baldatelegram.MessageContext) {
		dispatch(ctx, TelegramMessageContext(grouped))
	})
}
