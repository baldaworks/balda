package actors

import (
	"github.com/normahq/balda/internal/apps/balda/actors/goalkeeper"
	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/pkg/actorlayer/dispatch"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_actors",
	fx.Provide(
		NewTurnDispatcher,
		NewTaskRunRegistry,
		NewSessionWorkCanceller,
		fx.Annotate(
			func(params sessionActorExecutorParams) dispatch.Actor {
				return &sessionActorExecutor{turns: params.Turns, runner: params.Runner, tasks: params.Tasks, scheduler: params.Scheduler}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			func(params taskActorExecutorParams) dispatch.Actor {
				return &taskActorExecutor{
					tasks:      params.JobService,
					dispatcher: params.Dispatcher,
					sessions:   params.Sessions,
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			func(params struct {
				fx.In

				JobService         *baldajobs.JobService
				Dispatcher         actortransport.Dispatcher
				SessionManager     *baldasession.Manager
				RuntimeManager     *baldaagent.RuntimeManager
				TaskRuns           *TaskRunRegistry
				MaxIterations      int  `name:"balda_goal_max_iterations"`
				PlanUpdatesEnabled bool `name:"balda_telegram_plan_updates"`
				Logger             zerolog.Logger
			}) dispatch.Actor {
				return goalkeeper.NewActor(goalkeeper.ActorParams{
					JobService:         params.JobService,
					Dispatcher:         params.Dispatcher,
					SessionManager:     params.SessionManager,
					GoalRunPreparer:    goalRunPreparerAdapter{manager: params.RuntimeManager},
					TaskRuns:           params.TaskRuns,
					MaxIterations:      params.MaxIterations,
					PlanUpdatesEnabled: params.PlanUpdatesEnabled,
					Logger:             params.Logger,
				})
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			func(params memoryActorExecutorParams) dispatch.Actor {
				return &memoryActorExecutor{
					store:  params.Store,
					events: params.Events,
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			func(params taskDeliveryActorParams) dispatch.Actor {
				return &taskDeliveryActor{
					channel: params.Channel,
					tasks:   params.JobService,
					logger:  params.Logger.With().Str("component", "balda.task_delivery_actor").Logger(),
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
		fx.Annotate(
			func(params taskControlActorParams) dispatch.Actor {
				return &taskControlActor{
					turnDispatcher: params.TurnDispatcher,
					dispatcher:     params.Dispatcher,
					tasks:          params.JobService,
					taskRuns:       params.TaskRuns,
					logger:         params.Logger.With().Str("component", "balda.task_control_actor").Logger(),
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_swarm_actors"`),
		),
	),
)
