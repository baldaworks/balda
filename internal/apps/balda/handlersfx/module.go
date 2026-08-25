package handlersfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/handlers"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"go.uber.org/fx"
)

// Module wires ingress-owned ports to concrete provider runtimes.
var Module = fx.Module("balda_handlersfx",
	fx.Provide(
		newTelegramChannelAdapter,
		newTelegramAttachmentStore,
		fx.Annotate(
			func(handler *handlers.StartHandler) tgbotkit.Handler {
				return newTelegramHandlerAdapter(handler)
			},
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			func(handler *handlers.BaldaHandler) tgbotkit.Handler {
				return newTelegramHandlerAdapter(handler)
			},
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			func(handler *handlers.CommandHandler) tgbotkit.Handler {
				return newTelegramHandlerAdapter(handler)
			},
			fx.ResultTags(`group:"bot_handlers"`),
		),
	),
)
