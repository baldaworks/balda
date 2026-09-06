// Package commandfx binds CommandActor ports at the Balda composition boundary.
package commandfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/actors/command"
	commandlocator "github.com/baldaworks/balda/internal/apps/balda/actors/command/locator"
	commandreset "github.com/baldaworks/balda/internal/apps/balda/actors/command/reset"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"go.uber.org/fx"
)

type resetParams struct {
	fx.In
	Sessions   *session.Manager
	Canceller  appports.SessionWorkCanceller
	Dispatcher actortransport.Dispatcher
}

var Module = fx.Module("balda_command",
	fx.Provide(
		fx.Annotate(commandlocator.New, fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`)),
		fx.Annotate(
			func(p resetParams) *commandreset.Handler {
				return commandreset.New(p.Sessions, p.Canceller, p.Dispatcher)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(handlers []command.Handler, advertisements []commandcmd.Advertisement) (*command.Router, error) {
				router, err := command.NewRouter(handlers)
				if err != nil {
					return nil, err
				}
				for _, advertisement := range advertisements {
					if advertisement.Enabled {
						if err := router.ValidateAdvertised(advertisement.Names); err != nil {
							return nil, err
						}
					}
				}
				return router, nil
			},
			fx.ParamTags(`group:"balda_command_handlers"`, `group:"balda_command_advertisements"`),
		),
		fx.Annotate(
			func(router *command.Router) dispatch.Actor {
				return command.NewActor(router)
			},
			fx.As(new(dispatch.Actor)), fx.ResultTags(`group:"balda_product_actors"`),
		),
	),
)
