package questions

import (
	"context"
	"fmt"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_questions",
	fx.Provide(
		func(store baldastate.QuestionStore, scheduled baldastate.ScheduledJobStore, logger zerolog.Logger) (*Service, error) {
			if store == nil {
				return nil, fmt.Errorf("question store is required")
			}
			return New(store, scheduled, logger.With().Str("component", "balda.questions").Logger()), nil
		},
		NewDeliveryBindingProjector,
	),
	fx.Invoke(func(lc fx.Lifecycle, projector *DeliveryBindingProjector) {
		if projector == nil {
			return
		}
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error { return projector.Start(ctx) },
			OnStop:  func(ctx context.Context) error { return projector.Stop(ctx) },
		})
	}),
)
