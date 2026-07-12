package natsbus

import (
	actorengine "github.com/baldaworks/go-actorlayer/engine"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_eventbus_nats",
	fx.Provide(
		NewBus,
		func(bus *Bus) actortransport.Dispatcher { return bus },
		func(bus *Bus) actortransport.EventPublisher { return bus },
		func(bus *Bus) actortransport.Drainer { return bus },
		func(bus *Bus) actorengine.Source { return bus },
		func(bus *Bus) actortransport.EventConsumer { return bus },
	),
)
