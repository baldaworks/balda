package natsbus

import (
	"go.uber.org/fx"
)

var Module = fx.Module("balda_eventbus_nats",
	fx.Provide(
		NewCommandBus,
		NewCommandPublisher,
		NewEventPublisher,
		NewDLQPublisher,
		NewCommandConsumer,
		NewBusDrainer,
		NewCoordinatorBus,
		NewRuntimeBus,
		NewCommandBusStatusProvider,
	),
)
