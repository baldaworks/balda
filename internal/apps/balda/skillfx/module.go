// Package skillfx composes standalone skill management with the host catalog and store.
package skillfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

// Module binds the existing shared provider and catalog to management policy.
var Module = fx.Module("balda_skills", fx.Provide(fx.Annotate(func(stateDir string, provider state.Provider, catalog *catalogapp.Runtime) (*skillapp.Service, error) {
	return skillapp.New(stateDir, provider.SkillManagement(), catalog)
}, fx.ParamTags(`name:"balda_state_dir"`, ``, ``))))
