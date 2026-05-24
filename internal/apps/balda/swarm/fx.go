package swarm

import "go.uber.org/fx"

var Module = fx.Module("balda_swarm",
	fx.Provide(
		fx.Annotate(NewEmbeddedBus, fx.As(new(WakeBus))),
		NewMailboxService,
		NewCoordinator,
		fx.Annotate(NewAgentActor, fx.As(new(Actor)), fx.ResultTags(`group:"balda_swarm_actors"`)),
		fx.Annotate(NewMemoryActor, fx.As(new(Actor)), fx.ResultTags(`group:"balda_swarm_actors"`)),
		fx.Annotate(NewDeliveryActor, fx.As(new(Actor)), fx.ResultTags(`group:"balda_swarm_actors"`)),
		NewRuntime,
	),
	fx.Invoke(func(*Runtime) {}),
)
