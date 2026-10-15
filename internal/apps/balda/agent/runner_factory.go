package agent

import (
	"fmt"
	"slices"

	"go.uber.org/fx"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
)

// ToolHookScope describes the runtime context exposed to Balda's host tool
// plugin. Provider is an opaque filter and audit value.
type ToolHookScope struct {
	Enabled  bool
	Provider string
	CWD      string
}

// HostPluginFactory creates a fresh Balda host plugin for one runner.
type HostPluginFactory interface {
	NewToolPlugin(scope ToolHookScope) (*plugin.Plugin, error)
}

// RunnerFactoryParams wires the optional host plugin implementation.
type RunnerFactoryParams struct {
	fx.In

	Host HostPluginFactory `optional:"true"`
}

// RunnerFactory constructs stock ADK runners and adds Balda host plugins when
// a host plugin implementation is available and the caller enables the scope.
type RunnerFactory struct {
	host HostPluginFactory
}

// NewRunnerFactory creates the Balda-owned runner construction boundary.
func NewRunnerFactory(params RunnerFactoryParams) *RunnerFactory {
	return &RunnerFactory{host: params.Host}
}

// New constructs a stock ADK runner without mutating caller-owned plugin
// configuration. ADK remains responsible for rejecting duplicate plugin names.
func (f *RunnerFactory) New(cfg runner.Config, scope ToolHookScope) (*runner.Runner, error) {
	cfg, err := f.prepareConfig(cfg, scope)
	if err != nil {
		return nil, err
	}
	return runner.New(cfg)
}

func (f *RunnerFactory) prepareConfig(cfg runner.Config, scope ToolHookScope) (runner.Config, error) {
	cfg.PluginConfig.Plugins = slices.Clone(cfg.PluginConfig.Plugins)
	if scope.Enabled && f != nil && f.host != nil {
		hostPlugin, err := f.host.NewToolPlugin(scope)
		if err != nil {
			return runner.Config{}, fmt.Errorf("creating host tool plugin: %w", err)
		}
		if hostPlugin == nil {
			return runner.Config{}, fmt.Errorf("creating host tool plugin: factory returned nil")
		}
		// ADK stops a callback chain at the first non-nil result. Run Balda first
		// so it evaluates model/provider input and can stop denied calls before
		// trusted in-process extensions. Nil paths deliberately continue to
		// extensions that may transform accepted arguments afterward.
		cfg.PluginConfig.Plugins = append([]*plugin.Plugin{hostPlugin}, cfg.PluginConfig.Plugins...)
	}
	return cfg, nil
}
