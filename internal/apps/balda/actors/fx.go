package actors

import (
	"github.com/normahq/balda/internal/apps/balda/actors/goalkeeper"
	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/controlapp"
	"github.com/normahq/balda/internal/apps/balda/deliveryworkflow"
	"github.com/normahq/balda/internal/apps/balda/jobexec"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_actors",
	fx.Provide(
		NewTurnDispatcher,
		fx.Annotate(
			NewTurnDispatcher,
			fx.As(new(appports.TurnQueue)),
		),
		NewJobRunRegistry,
		fx.Annotate(
			func(r *JobRunRegistry) controlapp.JobRuns { return r },
		),
		fx.Annotate(
			func(s *controlapp.Service) jobControlService { return s },
		),
		fx.Annotate(
			func(s *jobexec.Service) jobExecutionService { return s },
		),
		fx.Annotate(
			func(s *deliveryworkflow.Service) deliveryWorkflowService { return s },
		),
		fx.Annotate(
			func(s *baldajobs.JobLifecycleService) sessionJobLifecycle { return s },
		),
		fx.Annotate(
			func(s *baldajobs.JobLifecycleService) goalkeeperJobLifecycle { return s },
		),
		fx.Annotate(
			func(s *baldajobs.JobEventsService) goalkeeperJobEvents { return s },
		),
		fx.Annotate(
			func(m *baldaagent.RuntimeManager) goalRunPreparerPort { return runtimeGoalRunPreparer{manager: m} },
		),
		fx.Annotate(
			func(params sessionActorExecutorParams) dispatch.Actor {
				return &sessionActorExecutor{turns: params.Turns, runner: params.Runner, tasks: params.Tasks, scheduler: params.Scheduler}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(params jobActorExecutorParams) dispatch.Actor {
				return &jobActorExecutor{
					tasks:      params.JobLifecycle,
					dispatcher: params.Dispatcher,
					service:    params.Service,
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(params struct {
				fx.In

				JobLifecycle    goalkeeperJobLifecycle
				JobEvents       goalkeeperJobEvents
				Dispatcher      actortransport.Dispatcher
				SessionManager  *baldasession.Manager
				GoalRunPreparer goalRunPreparerPort
				JobRuns         *JobRunRegistry
				MaxIterations   int `name:"balda_goal_max_iterations"`
				Logger          zerolog.Logger
			}) dispatch.Actor {
				return goalkeeper.NewActor(goalkeeper.ActorParams{
					JobLifecycle:    params.JobLifecycle,
					JobEvents:       params.JobEvents,
					Dispatcher:      params.Dispatcher,
					SessionManager:  params.SessionManager,
					GoalRunPreparer: params.GoalRunPreparer,
					JobRuns:         params.JobRuns,
					MaxIterations:   params.MaxIterations,
					Logger:          params.Logger,
				})
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(params memoryActorExecutorParams) dispatch.Actor {
				return &memoryActorExecutor{
					store:  params.Store,
					events: params.Events,
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(params jobDeliveryActorParams) dispatch.Actor {
				return &jobDeliveryActor{
					service: params.Service,
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(params jobControlActorParams) dispatch.Actor {
				return &jobControlActor{
					turnDispatcher: params.TurnDispatcher,
					dispatcher:     params.Dispatcher,
					jobs:           params.JobLifecycle,
					scheduledJobs:  params.ScheduledJobs,
					jobRuns:        params.JobRuns,
					logger:         params.Logger.With().Str("component", "balda.job_control_actor").Logger(),
					service:        params.Service,
				}
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
	),
)
