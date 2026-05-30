package natsbus

import (
	"github.com/normahq/balda/internal/apps/balda/swarm"
	actorengine "github.com/normahq/norma/pkg/actorlayer/engine"
)

func NewActorDispatcher(bus *Bus) swarm.ActorDispatcher {
	return bus
}

func NewEventPublisher(bus *Bus) swarm.EventPublisher {
	return bus
}

func NewBusDrainer(bus *Bus) swarm.BusDrainer {
	return bus
}

func NewActorDeliverySource(bus *Bus) actorengine.Source {
	return bus
}

func NewEventConsumer(bus *Bus) swarm.EventConsumer { return bus }
