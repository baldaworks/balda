package deliveryworkflow

import (
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_deliveryworkflow",
	fx.Provide(
		fx.Annotate(
			func(s *baldajobs.DeliveryService) DeliveryStore { return s },
		),
		fx.Annotate(
			func(s *baldajobs.JobEventsService) JobEvents { return s },
		),
		fx.Annotate(
			func(params struct {
				fx.In

				Dispatcher Dispatcher
				Registry   deliveryfmt.PromptRegistry
				Structured deliveryfmt.StructuredMessageRegistry
				Outbox     DeliveryStore
				Events     JobEvents
				Questions  QuestionDeliveryBinder `optional:"true"`
				Actor      actortransport.Dispatcher
				Logger     zerolog.Logger
			}) *Service {
				return NewWithRegistries(
					params.Dispatcher,
					params.Registry,
					params.Structured,
					params.Outbox,
					params.Events,
					params.Questions,
					params.Actor,
					params.Logger.With().Str("component", "balda.deliveryworkflow").Logger(),
				)
			},
		),
	),
)
