package catalogapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
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
	nativeHooks NativeHookPreparer
}

// NativeHookPreparer reports native policy and prepares one catalog-selected ACP leaf.
type NativeHookPreparer interface {
	Required(ctx context.Context, providerID string) (bool, error)
	Prepare(ctx context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error)
}

// ConfigureNativeHooks attaches the consuming port during composition.
func (f *ProviderFactory) ConfigureNativeHooks(preparer NativeHookPreparer) {
	f.nativeHooks = preparer
}

// BuildSession attaches provider capabilities whose release belongs to the agent.
func (f *ProviderFactory) BuildSession(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string, sessionID string) (adkagent.Agent, error) {
	if f.nativeHooks == nil {
		return f.BuildScoped(ctx, request, selections)
	}
	providers, release, err := f.prepareNativeHooks(ctx, request, sessionID)
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

func (f *ProviderFactory) prepareNativeHooks(ctx context.Context, request agentfactory.BuildRequest, sessionID string) (map[string]agentconfig.Config, func() error, error) {
	required, err := f.nativeHooks.Required(ctx, request.AgentID)
	if err != nil {
		return nil, nil, err
	}
	providers := cloneProviderConfigs(f.providers)
	if !required {
		return providers, func() error { return nil }, nil
	}

	var releases []func() error
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				if releases[i] != nil {
					releaseErr = errors.Join(releaseErr, releases[i]())
				}
			}
		})
		return releaseErr
	}

	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(rawID string) error {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return fmt.Errorf("native hook provider member is empty: %w", hooknative.ErrInvalidProviderTree)
		}
		if visiting[id] {
			return fmt.Errorf("native hook provider tree contains a cycle at %q: %w", id, hooknative.ErrInvalidProviderTree)
		}
		if visited[id] {
			return nil
		}
		config, ok := providers[id]
		if !ok {
			return fmt.Errorf("native hook provider %q is unavailable: %w", id, hooknative.ErrInvalidProviderTree)
		}
		visiting[id] = true
		defer delete(visiting, id)
		if agentconfig.IsPoolType(config.Type) {
			if config.PoolConfig == nil || len(config.PoolConfig.Members) == 0 {
				return fmt.Errorf("native hook provider pool %q has no members: %w", id, hooknative.ErrInvalidProviderTree)
			}
			for _, member := range config.PoolConfig.Members {
				if err := visit(member); err != nil {
					return err
				}
			}
			visited[id] = true
			return nil
		}
		if !agentconfig.IsACPType(config.Type) {
			visited[id] = true
			return nil
		}
		prepared, err := f.nativeHooks.Prepare(ctx, hooknative.LeafRequest{
			SessionID:  sessionID,
			ProviderID: request.AgentID,
			LeafID:     id,
			CWD:        request.WorkingDirectory,
			Config:     config,
		})
		if err != nil {
			return err
		}
		providers[id] = prepared.Config
		releases = append(releases, prepared.Release)
		visited[id] = true
		return nil
	}
	if err := visit(request.AgentID); err != nil {
		return nil, nil, errors.Join(err, release())
	}
	return providers, release, nil
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
	copyProviders := cloneProviderConfigs(providers)
	factoryOptions := append(slices.Clone(options), agentfactory.WithAvailabilityIndependentMCP())
	return &ProviderFactory{providers: copyProviders, registry: registry, options: factoryOptions}
}

func cloneProviderConfigs(providers map[string]agentconfig.Config) map[string]agentconfig.Config {
	cloned := make(map[string]agentconfig.Config, len(providers))
	for id, config := range providers {
		cloned[id] = cloneProviderConfig(config)
	}
	return cloned
}

func cloneProviderConfig(config agentconfig.Config) agentconfig.Config {
	config.MCPServers = slices.Clone(config.MCPServers)
	config.GenericACP = cloneACPConfig(config.GenericACP)
	config.GeminiACP = cloneACPConfig(config.GeminiACP)
	config.CodexACP = cloneACPConfig(config.CodexACP)
	config.OpenCodeACP = cloneACPConfig(config.OpenCodeACP)
	config.CopilotACP = cloneACPConfig(config.CopilotACP)
	config.ClaudeCodeACP = cloneACPConfig(config.ClaudeCodeACP)
	config.GrokACP = cloneACPConfig(config.GrokACP)
	config.RegistryACP = cloneACPConfig(config.RegistryACP)
	config.AgyACP = cloneACPConfig(config.AgyACP)
	config.AntigravityACP = cloneACPConfig(config.AntigravityACP)
	if config.OpenAI != nil {
		cloned := *config.OpenAI
		config.OpenAI = &cloned
	}
	if config.AIStudio != nil {
		cloned := *config.AIStudio
		config.AIStudio = &cloned
	}
	if config.PoolConfig != nil {
		cloned := *config.PoolConfig
		cloned.Members = slices.Clone(config.PoolConfig.Members)
		config.PoolConfig = &cloned
	}
	return config
}

func cloneACPConfig(config *agentconfig.ACPConfig) *agentconfig.ACPConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	cloned.Cmd = slices.Clone(config.Cmd)
	cloned.ExtraArgs = slices.Clone(config.ExtraArgs)
	return &cloned
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
