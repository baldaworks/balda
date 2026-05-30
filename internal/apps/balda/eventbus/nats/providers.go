package natsbus

import (
	"github.com/normahq/balda/internal/apps/balda/swarm"
	actorengine "github.com/normahq/norma/pkg/actorlayer/engine"
)

func newActorDispatcher(bus *Bus) swarm.ActorDispatcher {
	return bus
}

func newEventPublisher(bus *Bus) swarm.EventPublisher {
	return bus
}

func newBusDrainer(bus *Bus) swarm.BusDrainer {
	return bus
}

func newActorDeliverySource(bus *Bus) actorengine.Source {
	return bus
}

func newEventConsumer(bus *Bus) swarm.EventConsumer { return bus }
