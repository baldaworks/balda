package jobs

import "go.uber.org/fx"

var Module = fx.Module("balda_jobs",
	fx.Provide(
		NewJobService,
		NewEventProjector,
		NewOutboxPublisher,
	),
	fx.Invoke(func(*EventProjector, *OutboxPublisher) {}),
)
