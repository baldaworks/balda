// Package hookfx composes managed hooks with lifecycle and Backoffice ports.
package hookfx

import (
	"context"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/hookadk"
	"github.com/baldaworks/balda/internal/apps/balda/hookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativeapp"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
	"google.golang.org/adk/v2/plugin"
)

type hostPluginFactory struct {
	adapter *hookadk.Adapter
}

func (f hostPluginFactory) NewToolPlugin(scope baldaagent.ToolHookScope) (*plugin.Plugin, error) {
	return f.adapter.NewPlugin(hookadk.Scope{Provider: scope.Provider, CWD: scope.CWD})
}

type runtimeAdapter struct {
	runner   *hooks.Runner
	provider string
	native   *hooknativeapp.Service
}

func (a runtimeAdapter) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	if event.Name == hookcmd.UserPromptSubmit {
		if err := a.native.VerifySession(ctx, event.SessionID); err != nil {
			return hookcmd.Result{}, err
		}
	}
	if event.Provider == "" {
		event.Provider = a.provider
	}
	return a.runner.Run(ctx, event)
}

type adapterParams struct {
	fx.In
	Runner   *hooks.Runner
	Provider string `name:"balda_provider"`
	Native   *hooknativeapp.Service
}

// Module binds one configuration owner to both lifecycle surfaces and browser management.
var Module = fx.Module("balda_agent_hooks",
	fx.Provide(
		func(provider state.Provider, native *hooknativeapp.Service) *hooks.Management {
			management := hooks.NewManagement(provider.HookManagement())
			management.ConfigureValidator(native)
			return management
		},
		func(provider state.Provider) *hooks.Runner { return hooks.NewManagedRunner(provider.HookManagement()) },
		func(runner *hooks.Runner) *hookadk.Adapter { return hookadk.NewAdapter(runner) },
		func(adapter *hookadk.Adapter) baldaagent.HostPluginFactory {
			return hostPluginFactory{adapter: adapter}
		},
		func(p adapterParams) session.HookRunner {
			return runtimeAdapter{runner: p.Runner, provider: p.Provider, native: p.Native}
		},
		func(p adapterParams) sessionturnapp.HookRunner {
			return runtimeAdapter{runner: p.Runner, provider: p.Provider, native: p.Native}
		},
		hookbackofficeapp.New,
	),
	fx.Invoke(func(service *sessionturnapp.TurnExecutionService, hooks sessionturnapp.HookRunner) {
		service.SetHooks(hooks)
	}),
)
