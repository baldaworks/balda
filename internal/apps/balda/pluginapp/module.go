package pluginapp

import (
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_pluginapp",
	fx.Provide(
		fx.Annotate(
			func(stateDir string, provider baldastate.Provider) (*Service, error) {
				return New(stateDir, provider.AppKV())
			},
			fx.ParamTags(`name:"balda_state_dir"`, ``),
		),
	),
)
