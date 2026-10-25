package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/agentproduct"
	adkagent "google.golang.org/adk/v2/agent"
)

// ErrRuntimeProductClose is the bounded classification returned when product
// cleanup fails. Provider cleanup details must not cross this boundary.
var ErrRuntimeProductClose = errors.New("runtime product close failed")

// ScopedRuntimeFactory constructs providers with immutable per-provider MCP
// defaults, including defaults used by lazily constructed pool members.
type ScopedRuntimeFactory interface {
	BuildScoped(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string) (adkagent.Agent, error)
	WithProviders(configs map[string]agentconfig.Config) ScopedRuntimeFactory
}

// RuntimeProduct carries an immutable Agent, its optional control plane, and
// the sole close entrypoint for resources acquired by one factory layer.
type RuntimeProduct struct {
	Agent   adkagent.Agent
	Control agentproduct.SessionControl

	closeOnce sync.Once
	closeFn   func() error
	closeErr  error
}

// NewRuntimeProduct constructs one product owner. Callers must close the
// RuntimeProduct instead of closing Agent directly.
func NewRuntimeProduct(ag adkagent.Agent, control agentproduct.SessionControl, closeFn func() error) (*RuntimeProduct, error) {
	if ag == nil {
		return nil, fmt.Errorf("runtime product agent is required")
	}
	return &RuntimeProduct{Agent: ag, Control: control, closeFn: closeFn}, nil
}

// Close releases the product and any outer resources exactly once.
func (p *RuntimeProduct) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		if p.closeFn != nil {
			p.closeErr = p.closeFn()
		}
	})
	return p.closeErr
}

type runtimeProductFactory interface {
	BuildProduct(ctx context.Context, request agentfactory.BuildRequest) (*agentproduct.Product, error)
}

// ScopedProductRuntimeFactory is the additive product-aware scoped factory
// path. Legacy ScopedRuntimeFactory implementations remain supported.
type ScopedProductRuntimeFactory interface {
	BuildScopedProduct(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string) (*RuntimeProduct, error)
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

// SessionProductRuntimeFactory prepares a session-owned runtime product.
type SessionProductRuntimeFactory interface {
	BuildSessionProduct(ctx context.Context, request agentfactory.BuildRequest, selections map[string][]string, sessionID string) (*RuntimeProduct, error)
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

func adaptProduct(product *agentproduct.Product) (*RuntimeProduct, error) {
	if product == nil {
		return nil, fmt.Errorf("runtime product is required")
	}
	adapted, err := NewRuntimeProduct(product.Agent, product.Control, product.Close)
	if err != nil {
		return nil, errors.Join(err, classifyRuntimeProductClose(product.Close()))
	}
	return adapted, nil
}

func classifyRuntimeProductClose(err error) error {
	if err == nil {
		return nil
	}
	return ErrRuntimeProductClose
}

func closeRuntimeProduct(product *RuntimeProduct) error {
	if product == nil {
		return nil
	}
	return classifyRuntimeProductClose(product.Close())
}

func (f *scopedRuntimeFactory) BuildSessionState(providerID, workspace string) (map[string]any, error) {
	if original, ok := f.original.(sessionStateFactory); ok {
		return original.BuildSessionState(providerID, workspace)
	}
	return nil, nil
}
