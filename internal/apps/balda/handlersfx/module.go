package handlersfx

import (
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/handlers"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"go.uber.org/fx"
)

// Module wires ingress-owned ports to concrete provider runtimes.
var Module = fx.Module("balda_handlersfx",
	fx.Provide(
		fx.Annotate(
			func(server *baldatelegram.Server) handlers.BaldaOwnerActivator { return server },
		),
		fx.Annotate(
			func(server *baldatelegram.Server) handlers.InboundTurnExecutor { return server },
		),
		newTelegramChannelAdapter,
		fx.Annotate(
			func(handler *handlers.StartHandler) tgbotkit.Handler {
				return newTelegramCommandHandlerAdapter(handler)
			},
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			newTelegramServerHandlerAdapter,
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			func(handler *handlers.CommandHandler) tgbotkit.Handler {
				return newTelegramCommandHandlerAdapter(handler)
			},
			fx.ResultTags(`group:"bot_handlers"`),
		),
	),
)
