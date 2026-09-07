// Package commandfx binds CommandActor ports at the Balda composition boundary.
package commandfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/actors/command"
	commandauto "github.com/baldaworks/balda/internal/apps/balda/actors/command/auto"
	commandcancel "github.com/baldaworks/balda/internal/apps/balda/actors/command/cancel"
	commandgoalkeeper "github.com/baldaworks/balda/internal/apps/balda/actors/command/goalkeeper"
	commandhelp "github.com/baldaworks/balda/internal/apps/balda/actors/command/help"
	commandlocator "github.com/baldaworks/balda/internal/apps/balda/actors/command/locator"
	commandreset "github.com/baldaworks/balda/internal/apps/balda/actors/command/reset"
	commandusage "github.com/baldaworks/balda/internal/apps/balda/actors/command/usage"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type resetParams struct {
	fx.In
	Sessions   *session.Manager
	Canceller  appports.SessionWorkCanceller
	Dispatcher actortransport.Dispatcher
}

type usageParams struct {
	fx.In
	Sessions   *session.Manager
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

type autoParams struct {
	fx.In
	Sessions     *session.Manager
	Dispatcher   actortransport.Dispatcher
	AutoMaxTurns int `name:"balda_automode_max_turns"`
	Logger       zerolog.Logger
}

type cancelParams struct {
	fx.In
	Control    *controlapp.CommandDispatcher
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

type goalkeeperParams struct {
	fx.In
	Control       *controlapp.CommandDispatcher
	GoalJobs      *jobs.JobLifecycleService `optional:"true"`
	Dispatcher    actortransport.Dispatcher
	MaxIterations int `name:"balda_goal_max_iterations" optional:"true"`
	Logger        zerolog.Logger
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
		fx.Annotate(commandhelp.New, fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`)),
		fx.Annotate(
			func(p usageParams) *commandusage.Handler {
				return commandusage.New(p.Sessions, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p autoParams) *commandauto.Handler {
				return commandauto.New(p.Sessions, p.Dispatcher, p.AutoMaxTurns, nil, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p cancelParams) *commandcancel.Handler {
				return commandcancel.New(p.Control, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p goalkeeperParams) *commandgoalkeeper.Handler {
				var checker commandgoalkeeper.GoalJobChecker
				if p.GoalJobs != nil {
					checker = p.GoalJobs
				}
				return commandgoalkeeper.New(p.Control, checker, p.Dispatcher, p.MaxIterations, p.Logger)
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
