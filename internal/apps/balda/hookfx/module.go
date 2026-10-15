// Package hookfx composes managed hooks with lifecycle and Backoffice ports.
package hookfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/hookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

type runtimeAdapter struct {
	runner   *hooks.Runner
	provider string
}

func (a runtimeAdapter) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	if event.Provider == "" {
		event.Provider = a.provider
	}
	return a.runner.Run(ctx, event)
}

type adapterParams struct {
	fx.In
	Runner   *hooks.Runner
	Provider string `name:"balda_provider"`
}

// Module binds one configuration owner to both lifecycle surfaces and browser management.
var Module = fx.Module("balda_agent_hooks",
	fx.Provide(
		func(provider state.Provider) *hooks.Management { return hooks.NewManagement(provider.HookManagement()) },
		func(provider state.Provider) *hooks.Runner { return hooks.NewRunner(provider.HookManagement()) },
		func(p adapterParams) session.HookRunner {
			return runtimeAdapter{runner: p.Runner, provider: p.Provider}
		},
		func(p adapterParams) sessionturnapp.HookRunner {
			return runtimeAdapter{runner: p.Runner, provider: p.Provider}
		},
		hookbackofficeapp.New,
	),
	fx.Invoke(func(service *sessionturnapp.TurnExecutionService, hooks sessionturnapp.HookRunner) {
		service.SetHooks(hooks)
	}),
)
