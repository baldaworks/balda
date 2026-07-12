package jobs

import "go.uber.org/fx"

var Module = fx.Module("balda_jobs",
	fx.Provide(
		NewJobLifecycleService,
		NewJobEventsService,
		NewDeliveryService,
		NewAgentStepsService,
		NewEventProjector,
		NewOutboxPublisher,
	),
	fx.Invoke(func(*EventProjector, *OutboxPublisher) {}),
)
