package natsbus

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/swarm"
	actorengine "github.com/normahq/norma/pkg/actorlayer/engine"
)

func NewActorDispatcher(transport swarm.ActorRuntimeTransport) swarm.ActorDispatcher {
	return transport
}

func NewEventPublisher(transport swarm.ActorRuntimeTransport) swarm.EventPublisher {
	return transport
}

func NewBusDrainer(transport swarm.ActorRuntimeTransport) swarm.BusDrainer {
	return transport
}

func NewActorDeliverySource(transport swarm.ActorRuntimeTransport) actorengine.Source {
	if source, ok := transport.(actorengine.Source); ok {
		return source
	}
	return disabledActorDeliverySource{}
}

type disabledActorDeliverySource struct{}

func (disabledActorDeliverySource) Run(ctx context.Context, _ actorengine.Handler) error {
	<-ctx.Done()
	return ctx.Err()
}

func NewActorRuntimeStatusProvider(transport swarm.ActorRuntimeTransport) swarm.ActorRuntimeStatusProvider {
	bus := transport
	if status, ok := bus.(swarm.ActorRuntimeStatusProvider); ok {
		return status
	}
	return swarm.UnsupportedActorRuntimeTransport{}
}
