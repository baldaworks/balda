package startupnotifyapp

import (
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type params struct {
	fx.In

	Users     UserReader
	Sender    Sender
	Resolvers []ResolverBinding `group:"balda_startup_dm_resolver"`
}

// Module provides the startup readiness notification use case.
var Module = fx.Module("balda_startup_notify_app",
	fx.Provide(func(p params, logger zerolog.Logger) (*Service, error) {
		return New(p.Users, p.Sender, p.Resolvers, logger)
	}),
)
