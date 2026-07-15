package deliveryworkflow

import (
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldachannel "github.com/normahq/balda/internal/apps/balda/channel"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_deliveryworkflow",
	fx.Provide(
		fx.Annotate(
			func(r *baldachannel.Router) Dispatcher { return channelRouterDispatcher{router: r} },
		),
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
				Outbox     DeliveryStore
				Events     JobEvents
				Questions  QuestionDeliveryBinder `optional:"true"`
				Actor      actortransport.Dispatcher
				Logger     zerolog.Logger
			}) *Service {
				return New(
					params.Dispatcher,
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
