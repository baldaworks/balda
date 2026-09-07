// Package actorsfx binds Balda product actors at the application composition boundary.
package actorsfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/actors/goalkeeper"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	"github.com/baldaworks/balda/internal/apps/balda/jobexec"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/permissions"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type memoryActorParams struct {
	fx.In

	Store  *memory.Store
	Events actortransport.EventPublisher `optional:"true"`
}

type sessionActorParams struct {
	fx.In

	Dispatcher actortransport.Dispatcher
	Turns      appports.TurnQueue
	Runner     appports.SessionTurnRunner
	Tasks      *baldajobs.JobLifecycleService `optional:"true"`
	Scheduler  appports.ScheduledJobRecorder  `optional:"true"`
	Questions  *questions.Service             `optional:"true"`
	Sessions   *baldasession.Manager          `optional:"true"`
}

type goalkeeperActorParams struct {
	fx.In

	JobLifecycle    *baldajobs.JobLifecycleService
	JobEvents       *baldajobs.JobEventsService
	Dispatcher      actortransport.Dispatcher
	SessionManager  *baldasession.Manager
	GoalRunPreparer goalkeeper.GoalRunPreparer
	JobRuns         *actors.JobRunRegistry
	QuestionService *questions.Service
	MaxIterations   int `name:"balda_goal_max_iterations"`
	Logger          zerolog.Logger
}

// Module provides Balda product actors and their composition adapters.
var Module = fx.Module("balda_actorsfx",
	fx.Provide(
		actors.NewTurnDispatcher,
		fx.Annotate(
			actors.NewTurnDispatcher,
			fx.As(new(appports.TurnQueue)),
		),
		actors.NewJobRunRegistry,
		fx.Annotate(
			func(r *actors.JobRunRegistry) controlapp.JobRuns { return r },
		),
		fx.Annotate(
			func(m *baldaagent.RuntimeManager) goalkeeper.GoalRunPreparer {
				return runtimeGoalRunPreparer{manager: m}
			},
		),
		fx.Annotate(
			func(s *controlapp.Service) dispatch.Actor {
				return actors.NewJobControlActor(s)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(s *permissions.Service) dispatch.Actor {
				return actors.NewPermissionActor(s)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(p sessionActorParams) dispatch.Actor {
				var tasks actors.SessionJobLifecycle
				if p.Tasks != nil {
					tasks = p.Tasks
				}
				var sessions actors.SessionRuntimeStateUpdater
				if p.Sessions != nil {
					sessions = p.Sessions
				}
				return actors.NewSessionActor(actors.SessionActorConfig{
					Turns:      p.Turns,
					Runner:     p.Runner,
					Tasks:      tasks,
					Scheduler:  p.Scheduler,
					Dispatcher: p.Dispatcher,
					Questions:  p.Questions,
					Sessions:   sessions,
				})
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(s *jobexec.Service) dispatch.Actor {
				return actors.NewJobActorExecutor(s)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(p goalkeeperActorParams) dispatch.Actor {
				return goalkeeper.NewActor(goalkeeper.ActorParams{
					JobLifecycle:    p.JobLifecycle,
					JobEvents:       p.JobEvents,
					Dispatcher:      p.Dispatcher,
					SessionManager:  p.SessionManager,
					GoalRunPreparer: p.GoalRunPreparer,
					JobRuns:         p.JobRuns,
					QuestionService: p.QuestionService,
					MaxIterations:   p.MaxIterations,
					Logger:          p.Logger,
				})
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(p memoryActorParams) dispatch.Actor {
				return actors.NewMemoryActorExecutor(p.Store, p.Events)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(d actortransport.Dispatcher) dispatch.Actor {
				return actors.NewQuestionActor(d)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
		fx.Annotate(
			func(s *deliveryworkflow.Service) dispatch.Actor {
				var svc actors.DeliveryWorkflowService = deliveryWorkflowAdapter{service: s}
				return actors.NewJobDeliveryActor(svc)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
	),
)
