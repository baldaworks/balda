package natsbus

import (
	"github.com/normahq/balda/internal/apps/balda/swarm"
	actorengine "github.com/normahq/balda/pkg/actorlayer/engine"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_eventbus_nats",
	fx.Provide(
		NewBus,
		func(bus *Bus) swarm.ActorDispatcher { return bus },
		func(bus *Bus) swarm.EventPublisher { return bus },
		func(bus *Bus) swarm.BusDrainer { return bus },
		func(bus *Bus) actorengine.Source { return bus },
		func(bus *Bus) swarm.EventConsumer { return bus },
	),
)
