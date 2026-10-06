package catalogapp

import (
	"context"
	"fmt"
	"maps"
	"slices"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	"github.com/normahq/runtime/v2/mcpregistry"
	adkagent "google.golang.org/adk/v2/agent"
)

// ProviderFactory keeps shared provider config immutable while adapting exact
// catalog selections to the pinned factory's root and pool-member build paths.
type ProviderFactory struct {
	providers map[string]agentconfig.Config
	registry  mcpregistry.Reader
	options   []agentfactory.Option
}

// NewProviderFactory retains host options, including the permission handler.
func NewProviderFactory(providers map[string]agentconfig.Config, registry mcpregistry.Reader, options ...agentfactory.Option) *ProviderFactory {
	copyProviders := maps.Clone(providers)
	for id, config := range copyProviders {
		config.MCPServers = slices.Clone(config.MCPServers)
		copyProviders[id] = config
	}
	return &ProviderFactory{providers: copyProviders, registry: registry, options: slices.Clone(options)}
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
