package natsbus

import (
	"go.uber.org/fx"
)

var Module = fx.Module("balda_eventbus_nats",
	fx.Provide(
		NewBus,
		newActorDispatcher,
		newEventPublisher,
		newBusDrainer,
		newActorDeliverySource,
		newEventConsumer,
	),
)
