package swarm

import (
	"context"
	"fmt"
)

type unsupportedActor struct {
	address string
	name    string
}

type memoryActor struct{}

func NewAgentActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeAgent), name: ActorTypeAgent}
}

func NewMemoryActor() Actor {
	return memoryActor{}
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

func (memoryActor) Address() string {
	return WildcardAddress(ActorTypeMemory)
}

func (memoryActor) Handle(_ context.Context, env Envelope) error {
	if env.Namespace != NamespaceMemorySync {
		return PolicyError(fmt.Errorf("memory actor does not support %q/%q yet", env.Namespace, env.Kind))
	}
	return nil
}
