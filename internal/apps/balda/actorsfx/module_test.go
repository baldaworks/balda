package actorsfx_test

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actorsfx"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/go-actorlayer/dispatch"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestActorsfxModule_ProvidesJobControlActor(t *testing.T) {
	var actors []dispatch.Actor

	type params struct {
		fx.In
		Actors []dispatch.Actor `group:"balda_product_actors"`
	}

	app := fx.New(
		fx.Provide(
			func() *controlapp.Service {
				return controlapp.New(nil, nil, nil, nil, nil, zerolog.Nop())
			},
		),
		actorsfx.Module,
		fx.Invoke(func(p params) {
			actors = p.Actors
		}),
	)

	require.NoError(t, app.Err())
	require.Len(t, actors, 1)
	require.Equal(t, "system:control", actors[0].Address())
}
