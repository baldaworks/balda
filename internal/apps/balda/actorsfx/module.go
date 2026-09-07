// Package actorsfx binds Balda product actors at the application composition boundary.
package actorsfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/go-actorlayer/dispatch"
	"go.uber.org/fx"
)

// Module provides Balda product actors and their composition adapters.
var Module = fx.Module("balda_actorsfx",
	fx.Provide(
		fx.Annotate(
			func(s *controlapp.Service) dispatch.Actor {
				return actors.NewJobControlActor(s)
			},
			fx.As(new(dispatch.Actor)),
			fx.ResultTags(`group:"balda_product_actors"`),
		),
	),
)
