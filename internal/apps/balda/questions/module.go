package questions

import (
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_questions",
	fx.Provide(
		func(params struct {
			fx.In

			Store      baldastate.QuestionStore
			Scheduled  baldastate.ScheduledJobStore
			Controls   ControlPublisher `optional:"true"`
			Structured deliveryfmt.StructuredMessageRegistry
			Logger     zerolog.Logger
		}) (*Service, error) {
			if params.Store == nil {
				return nil, fmt.Errorf("question store is required")
			}
			service := New(params.Store, params.Scheduled, params.Logger.With().Str("component", "balda.questions").Logger())
			service.SetControlPublisher(params.Controls)
			service.SetStructuredRegistry(params.Structured)
			return service, nil
		},
		NewDeliveryBindingProjector,
	),
)
