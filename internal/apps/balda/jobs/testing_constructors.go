package jobs

import actortransport "github.com/normahq/balda/pkg/actorlayer/transport"

func NewJobLifecycleServiceForTests(store ServiceStore, bus actortransport.EventPublisher) (*JobLifecycleService, error) {
	return NewJobLifecycleService(jobLifecycleServiceParams{Store: store, Bus: bus})
}

func NewJobEventsServiceForTests(store ServiceStore, bus actortransport.EventPublisher) (*JobEventsService, error) {
	return NewJobEventsService(jobEventsServiceParams{Store: store, Bus: bus})
}

func NewDeliveryServiceForTests(store ServiceStore) (*DeliveryService, error) {
	return NewDeliveryService(deliveryServiceParams{Store: store})
}

func NewAgentStepsServiceForTests(store ServiceStore) (*AgentStepsService, error) {
	return NewAgentStepsService(agentStepsServiceParams{Store: store})
}
