package actors

import (
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_actors",
	fx.Provide(
		NewTurnDispatcher,
		NewTaskRunRegistry,
		fx.Annotate(
			func(params sessionActorExecutorParams) swarm.Actor {
				return &sessionActorExecutor{turns: params.Turns, runner: params.Runner, tasks: params.Tasks, scheduler: params.Scheduler}
			},
			fx.As(new(swarm.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			func(params taskActorExecutorParams) swarm.Actor {
				return &taskActorExecutor{
					tasks:      params.TaskService,
					dispatcher: params.Dispatcher,
					sessions:   params.Sessions,
				}
			},
			fx.As(new(swarm.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			newGoalActor,
			fx.As(new(swarm.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			newTaskDeliveryActor,
			fx.As(new(swarm.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			newTaskControlActor,
			fx.As(new(swarm.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
	),
)
