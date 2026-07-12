package jobexec

import (
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_jobexec",
	fx.Provide(
		fx.Annotate(
			func(s *baldajobs.JobLifecycleService) JobLifecycle { return s },
		),
		fx.Annotate(
			func(params struct {
				fx.In

				JobLifecycle JobLifecycle
				Dispatcher   actortransport.Dispatcher
			}) *Service {
				return New(params.JobLifecycle, params.Dispatcher)
			},
		),
	),
)
