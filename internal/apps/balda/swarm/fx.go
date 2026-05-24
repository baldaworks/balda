package swarm

import "go.uber.org/fx"

var Module = fx.Module("balda_swarm",
	fx.Provide(
		fx.Annotate(NewEmbeddedBus, fx.As(new(WakeBus))),
		NewShadowMetrics,
		NewMailboxService,
		NewTaskService,
		NewAgentRegistry,
		NewAgentAllocator,
		NewCoordinator,
		fx.Annotate(NewMemoryActor, fx.As(new(Actor)), fx.ResultTags(`group:"balda_swarm_actors"`)),
		NewRuntime,
	),
	fx.Invoke(func(*Runtime) {}),
)
