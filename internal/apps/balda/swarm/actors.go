package swarm

import (
	"context"
	"fmt"
)

type unsupportedActor struct {
	address string
	name    string
}

func NewAgentActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeAgent), name: ActorTypeAgent}
}

func NewMemoryActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeMemory), name: ActorTypeMemory}
}

func NewDeliveryActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeDelivery), name: ActorTypeDelivery}
}

func (a unsupportedActor) Address() string {
	return a.address
}

func (a unsupportedActor) Handle(_ context.Context, env Envelope) error {
	return PolicyError(fmt.Errorf("%s actor does not support %q/%q yet", a.name, env.Namespace, env.Kind))
}
