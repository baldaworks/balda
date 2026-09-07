package handlersfx

import (
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"go.uber.org/fx"
)

// Module wires ingress-owned ports to concrete provider runtimes.
var Module = fx.Module("balda_handlersfx",
	fx.Provide(
		newTelegramInboundHandler,
		fx.Annotate(
			func(h *telegramInboundHandler) baldatelegram.InboundHandler { return h },
		),
		fx.Annotate(
			func(h *telegramInboundHandler) baldatelegram.BotLifecycleHandler { return h },
		),
		fx.Annotate(
			newInboundTurnExecutor,
			fx.As(new(webhookapp.SessionPublisher)),
			fx.As(new(webhookapp.JobPublisher)),
		),
		fx.Annotate(
			newTelegramStartHandler,
			fx.As(new(tgbotkit.Handler)),
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			newTelegramServerHandlerAdapter,
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			newTelegramCommandHandler,
			fx.As(new(tgbotkit.Handler)),
			fx.ResultTags(`group:"bot_handlers"`),
		),
		newZulipInboundHandler,
		fx.Annotate(
			func(ch *baldatelegram.Adapter) sessionapp.TelegramTopicChannel {
				if ch == nil {
					return nil
				}
				return ch
			},
			fx.ParamTags(`optional:"true"`),
			fx.As(new(sessionapp.TelegramTopicChannel)),
		),
		fx.Annotate(
			func(h *telegramInboundHandler) sessionapp.TelegramOwnerActivator {
				if h == nil {
					return nil
				}
				return h
			},
			fx.ParamTags(`optional:"true"`),
			fx.As(new(sessionapp.TelegramOwnerActivator)),
		),
		fx.Annotate(
			func(h *zulipInboundHandler) sessionapp.ZulipOwnerActivator {
				if h == nil {
					return nil
				}
				return h
			},
			fx.ParamTags(`optional:"true"`),
			fx.As(new(sessionapp.ZulipOwnerActivator)),
		),
	),
)
