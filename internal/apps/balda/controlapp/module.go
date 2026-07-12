package controlapp

import (
	"github.com/normahq/balda/internal/apps/balda/appports"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_controlapp",
	fx.Provide(
		fx.Annotate(
			func(s *baldajobs.JobLifecycleService) JobLifecycle { return s },
		),
		fx.Annotate(
			func(turns appports.TurnQueue, jobs JobLifecycle, runs JobRuns, logger zerolog.Logger) *SessionWorkCanceller {
				return NewSessionWorkCanceller(turns, jobs, runs, logger)
			},
		),
		fx.Annotate(
			func(c *SessionWorkCanceller) appports.SessionWorkCanceller { return c },
		),
		fx.Annotate(
			func(params struct {
				fx.In

				TurnQueue     appports.TurnQueue
				Dispatcher    actortransport.Dispatcher
				JobLifecycle  JobLifecycle
				ScheduledJobs ScheduledJobs `optional:"true"`
				JobRuns       JobRuns
				Logger        zerolog.Logger
			}) *Service {
				return New(
					params.TurnQueue,
					params.Dispatcher,
					params.JobLifecycle,
					params.ScheduledJobs,
					params.JobRuns,
					params.Logger.With().Str("component", "balda.controlapp").Logger(),
				)
			},
		),
	),
)
