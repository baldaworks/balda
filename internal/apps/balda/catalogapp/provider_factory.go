package catalogapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	"github.com/normahq/runtime/v2/mcpregistry"
	adkagent "google.golang.org/adk/v2/agent"
)

// ProviderFactory keeps shared provider config immutable while adapting exact
// catalog selections to the pinned factory's root and pool-member build paths.
type ProviderFactory struct {
	providers   map[string]agentconfig.Config
	registry    mcpregistry.Reader
	options     []agentfactory.Option
	launchHooks ProviderLaunchPreparer
}

// ProviderLaunchPreparer binds native interception to private launch defaults.
type ProviderLaunchPreparer interface {
	Prepare(ctx context.Context, sessionID, providerID, cwd string, providers map[string]agentconfig.Config) (map[string]agentconfig.Config, func() error, error)
}

// ConfigureLaunchHooks attaches the consuming port during composition.
func (f *ProviderFactory) ConfigureLaunchHooks(preparer ProviderLaunchPreparer) {
	f.launchHooks = preparer
}

// BuildSession attaches provider capabilities whose release belongs to the agent.
func (f *ProviderFactory) BuildSession(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string, sessionID string) (adkagent.Agent, error) {
	if f.launchHooks == nil {
		return f.BuildScoped(ctx, request, selections)
	}
	providers, release, err := f.launchHooks.Prepare(ctx, sessionID, request.AgentID, request.WorkingDirectory, f.providers)
	if err != nil {
		return nil, err
	}
	copyFactory := *f
	copyFactory.providers = providers
	built, err := copyFactory.BuildScoped(ctx, request, selections)
	if err != nil {
		if release != nil {
			_ = release()
		}
		return nil, err
	}
	return &launchAgent{Agent: built, release: release}, nil
}

type launchAgent struct {
	adkagent.Agent
	release func() error
	once    sync.Once
	err     error
}

func (a *launchAgent) Close() error {
	a.once.Do(func() {
		if a.release != nil {
			a.err = a.release()
		}
		if closer, ok := a.Agent.(io.Closer); ok {
			a.err = errors.Join(a.err, closer.Close())
		}
	})
	return a.err
}

// NewProviderFactory retains host options and makes MCP attachment invocation-owned.
func NewProviderFactory(providers map[string]agentconfig.Config, registry mcpregistry.Reader, options ...agentfactory.Option) *ProviderFactory {
	copyProviders := maps.Clone(providers)
	for id, config := range copyProviders {
		config.MCPServers = slices.Clone(config.MCPServers)
		copyProviders[id] = config
	}
	factoryOptions := append(slices.Clone(options), agentfactory.WithAvailabilityIndependentMCP())
	return &ProviderFactory{providers: copyProviders, registry: registry, options: factoryOptions}
}

// BuildScoped applies selection to private defaults before any member is built.
func (f *ProviderFactory) BuildScoped(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string) (adkagent.Agent, error) {
	providers := maps.Clone(f.providers)
	for id, ids := range selections {
		config, ok := providers[id]
		if !ok {
			return nil, fmt.Errorf("selected provider is unavailable")
		}
		config.MCPServers = slices.Clone(ids)
		providers[id] = config
	}
	if root, ok := providers[request.AgentID]; ok && agentconfig.IsPoolType(root.Type) {
		defaults, err := providerMCPDefaults(providers, request.AgentID, nil)
		if err != nil {
			return nil, err
		}
		// Pool members are built from their own defaults. Inherit only this
		// runtime's selected pool policy, leaving shared leaf defaults intact.
		for id, ids := range defaults {
			if id == request.AgentID {
				continue
			}
			config := providers[id]
			ids = append(ids, root.MCPServers...)
			slices.Sort(ids)
			config.MCPServers = slices.Compact(ids)
			providers[id] = config
		}
	}
	return agentfactory.New(providers, f.registry, f.options...).Build(ctx, request)
}

var _ baldaagent.ScopedRuntimeFactory = (*ProviderFactory)(nil)
