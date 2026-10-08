package jobexec

import (
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_jobexec",
	fx.Provide(
		NewWebhookRunFinalizer,
		fx.Annotate(
			func(s *baldajobs.JobLifecycleService) JobLifecycle { return s },
		),
		fx.Annotate(
			func(params struct {
				fx.In

				JobLifecycle JobLifecycle
				Dispatcher   actortransport.Dispatcher
				Modes        appports.ScheduleModeResolver `optional:"true"`
			}) *Service {
				return NewWithScheduleModes(params.JobLifecycle, params.Dispatcher, params.Modes)
			},
		),
	),
)
