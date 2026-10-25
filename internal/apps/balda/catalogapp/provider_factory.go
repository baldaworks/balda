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
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	adkagent "google.golang.org/adk/v2/agent"
)

// ErrNativeHookRelease is the bounded classification returned when native
// hook cleanup fails. Hook/provider details remain below the catalog boundary.
var ErrNativeHookRelease = errors.New("native hook release failed")

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
		return nil, errors.Join(err, releaseNativeHooks(release))
	}
	return &launchAgent{Agent: built, release: release}, nil
}

// BuildSessionProduct attaches native hooks to one independently owned runtime
// product. Hooks are acquired first, so close releases the product before them.
func (f *ProviderFactory) BuildSessionProduct(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string, sessionID string) (*baldaagent.RuntimeProduct, error) {
	if f.nativeHooks == nil {
		return f.BuildScopedProduct(ctx, request, selections)
	}
	providers, release, err := f.prepareNativeHooks(ctx, request, sessionID)
	if err != nil {
		return nil, err
	}
	copyFactory := *f
	copyFactory.providers = providers
	product, err := copyFactory.BuildScopedProduct(ctx, request, selections)
	if err != nil {
		return nil, joinCatalogConstructionError(err, nil, release)
	}
	return runtimeProductWithRelease(product, release)
}

func runtimeProductWithRelease(product *baldaagent.RuntimeProduct, release func() error) (*baldaagent.RuntimeProduct, error) {
	if product == nil {
		return nil, joinCatalogConstructionError(fmt.Errorf("runtime product is required"), nil, release)
	}
	adapted, err := baldaagent.NewRuntimeProduct(product.Agent, product.Control, func() error {
		return errors.Join(closeCatalogProduct(product), releaseNativeHooks(release))
	})
	if err != nil {
		return nil, joinCatalogConstructionError(err, product, release)
	}
	return adapted, nil
}

func joinCatalogConstructionError(primary error, product *baldaagent.RuntimeProduct, release func() error) error {
	return errors.Join(primary, closeCatalogProduct(product), releaseNativeHooks(release))
}

func closeCatalogProduct(product *baldaagent.RuntimeProduct) error {
	if product == nil {
		return nil
	}
	if err := product.Close(); err != nil {
		return baldaagent.ErrRuntimeProductClose
	}
	return nil
}

func releaseNativeHooks(release func() error) error {
	if release == nil {
		return nil
	}
	if err := release(); err != nil {
		return ErrNativeHookRelease
	}
	return nil
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
		return nil, nil, errors.Join(err, releaseNativeHooks(release))
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
		if closer, ok := a.Agent.(io.Closer); ok {
			a.err = closer.Close()
		}
		if a.release != nil {
			a.err = errors.Join(a.err, releaseNativeHooks(a.release))
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

// WithProviders returns an invocation-local factory for an exact provider graph.
func (f *ProviderFactory) WithProviders(providers map[string]agentconfig.Config) baldaagent.ScopedRuntimeFactory {
	copyFactory := *f
	copyFactory.providers = cloneProviderConfigs(providers)
	return &copyFactory
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
	providers, err := f.selectedProviders(request, selections)
	if err != nil {
		return nil, err
	}
	return agentfactory.New(providers, f.registry, f.options...).Build(ctx, request)
}

// BuildScopedProduct applies selection before returning a product-aware runtime.
func (f *ProviderFactory) BuildScopedProduct(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string) (*baldaagent.RuntimeProduct, error) {
	providers, err := f.selectedProviders(request, selections)
	if err != nil {
		return nil, err
	}
	product, err := agentfactory.New(providers, f.registry, f.options...).BuildProduct(ctx, request)
	if err != nil {
		return nil, err
	}
	adapted, err := baldaagent.NewRuntimeProduct(product.Agent, product.Control, product.Close)
	if err != nil {
		var closeErr error
		if product.Close() != nil {
			closeErr = baldaagent.ErrRuntimeProductClose
		}
		return nil, errors.Join(err, closeErr)
	}
	return adapted, nil
}

func (f *ProviderFactory) selectedProviders(request agentfactory.BuildRequest, selections map[string][]string) (map[string]agentconfig.Config, error) {
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
		root.MCPServers = append(slices.Clone(root.MCPServers), request.MCPServerIDs...)
		slices.Sort(root.MCPServers)
		root.MCPServers = slices.Compact(root.MCPServers)
		providers[request.AgentID] = root
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
	return providers, nil
}

var _ baldaagent.ScopedRuntimeFactory = (*ProviderFactory)(nil)
var _ baldaagent.ScopedProductRuntimeFactory = (*ProviderFactory)(nil)
var _ baldaagent.SessionProductRuntimeFactory = (*ProviderFactory)(nil)
