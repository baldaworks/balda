package pluginapp

import (
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_pluginapp",
	fx.Provide(
		fx.Annotate(
			func(stateDir string, provider baldastate.Provider, activator CatalogActivator) (*Service, error) {
				return NewManaged(stateDir, provider.AppKV(), provider.Plugins(), activator)
			},
			fx.ParamTags(`name:"balda_state_dir"`, ``, ``),
		),
	),
)
