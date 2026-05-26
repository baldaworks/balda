package natsbus

import "github.com/normahq/balda/internal/apps/balda/swarm"

func NewCommandPublisher(bus swarm.CommandBus) swarm.CommandPublisher {
	return bus
}

func NewEventPublisher(bus swarm.CommandBus) swarm.EventPublisher {
	return bus
}

func NewDLQPublisher(bus swarm.CommandBus) swarm.DLQPublisher {
	return bus
}

func NewCommandConsumer(bus swarm.CommandBus) swarm.CommandConsumer {
	return bus
}

func NewBusDrainer(bus swarm.CommandBus) swarm.BusDrainer {
	return bus
}

func NewCoordinatorBus(bus swarm.CommandBus) swarm.CoordinatorBus {
	return bus
}

func NewRuntimeBus(bus swarm.CommandBus) swarm.RuntimeBus {
	return bus
}

func NewCommandBusStatusProvider(bus swarm.CommandBus) swarm.CommandBusStatusProvider {
	if status, ok := bus.(swarm.CommandBusStatusProvider); ok {
		return status
	}
	return swarm.UnsupportedCommandBus{}
}
