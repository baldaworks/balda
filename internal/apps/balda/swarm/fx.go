package swarm

import "go.uber.org/fx"

var Module = fx.Module("balda_swarm",
	fx.Provide(
		NewTaskService,
		NewEventProjector,
		NewActorHost,
	),
	fx.Invoke(func(*EventProjector) {}),
	fx.Invoke(func(*ActorHost) {}),
)
