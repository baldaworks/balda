package handlers

import (
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"go.uber.org/fx"
)

// Module provides handlers for the balda bot.
var Module = fx.Module("balda_handlers",
	fx.Provide(
		fx.Annotate(
			NewChatHandler,
			fx.As(new(chatapp.Handler)),
		),
		func(params startHandlerParams) *StartHandler {
			return &StartHandler{
				ownerStore:     params.OwnerStore,
				commandIngress: params.CommandIngress,
			}
		},
		func(params commandHandlerParams) *CommandHandler {
			return &CommandHandler{
				ownerStore:        params.OwnerStore,
				collaboratorStore: params.CollaboratorStore,
				channel:           params.Channel,
				commandIngress:    params.CommandIngress,
			}
		},
		fx.Annotate(NewCommandIngress, fx.As(new(commandcmd.Ingress))),
	),
)
