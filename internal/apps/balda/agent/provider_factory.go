package agent

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
)

// ScopedRuntimeFactory constructs providers with immutable per-provider MCP
// defaults, including defaults used by lazily constructed pool members.
type ScopedRuntimeFactory interface {
	BuildScoped(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string) (adkagent.Agent, error)
	WithProviders(configs map[string]agentconfig.Config) ScopedRuntimeFactory
}

func (b *Builder) withProviders(configs map[string]agentconfig.Config) (*Builder, error) {
	if b == nil || b.scopedFactory == nil {
		return nil, fmt.Errorf("scoped provider factory is required")
	}
	copyBuilder := *b
	copyBuilder.normaCfg.Providers = maps.Clone(configs)
	copyBuilder.scopedFactory = b.scopedFactory.WithProviders(configs)
	copyBuilder.factory = &scopedRuntimeFactory{owner: copyBuilder.scopedFactory, original: b.factory}
	return &copyBuilder, nil
}

// SessionRuntimeFactory prepares session-owned native provider capabilities.
type SessionRuntimeFactory interface {
	BuildSession(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string, sessionID string) (adkagent.Agent, error)
}

func (b *Builder) withProviderMCP(selections map[string][]string) (*Builder, error) {
	if selections == nil {
		return b, nil
	}
	if b.scopedFactory == nil {
		return nil, fmt.Errorf("scoped provider factory is required")
	}
	copyBuilder := *b
	copyBuilder.providerMCPSelections = selections
	copyBuilder.normaCfg.Providers = maps.Clone(b.normaCfg.Providers)
	selected := make(map[string][]string, len(selections))
	for id, ids := range selections {
		config, ok := copyBuilder.normaCfg.Providers[id]
		if !ok {
			return nil, fmt.Errorf("selected provider is unavailable")
		}
		selected[id] = slices.Clone(ids)
		config.MCPServers = slices.Clone(ids)
		copyBuilder.normaCfg.Providers[id] = config
	}
	copyBuilder.factory = &scopedRuntimeFactory{owner: b.scopedFactory, selections: selected, original: b.factory}
	return &copyBuilder, nil
}

type scopedRuntimeFactory struct {
	owner      ScopedRuntimeFactory
	selections map[string][]string
	original   runtimeFactory
}

func (f *scopedRuntimeFactory) Build(ctx context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error) {
	return f.owner.BuildScoped(ctx, request, f.selections)
}

func (f *scopedRuntimeFactory) BuildSessionState(providerID, workspace string) (map[string]any, error) {
	if original, ok := f.original.(sessionStateFactory); ok {
		return original.BuildSessionState(providerID, workspace)
	}
	return nil, nil
}
