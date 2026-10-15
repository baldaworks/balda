package hooknative

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

var (
	// ErrNoAdapter reports that no registered adapter supports a required ACP leaf.
	ErrNoAdapter = errors.New("native hook adapter is unavailable")
	// ErrMultipleAdapters reports an ambiguous adapter registration.
	ErrMultipleAdapters = errors.New("multiple native hook adapters match")
	// ErrInvalidProviderTree reports a catalog traversal that cannot identify leaves.
	ErrInvalidProviderTree = fmt.Errorf("invalid native hook provider tree: %w", hookcmd.ErrUnsupported)
)

// LeafRequest describes one ACP leaf selected by the catalog owner.
type LeafRequest struct {
	SessionID  string
	ProviderID string
	LeafID     string
	CWD        string
	Config     agentconfig.Config
}

// PreparedLeaf contains an immutable replacement and its owned cleanup.
type PreparedLeaf struct {
	Config  agentconfig.Config
	Release func() error
}

// Adapter owns support detection and launch preparation for one native family.
type Adapter interface {
	Name() string
	Supports(config agentconfig.Config) bool
	Prepare(ctx context.Context, request LeafRequest) (PreparedLeaf, error)
}

// Registry selects an adapter without assigning precedence to registration order.
type Registry struct {
	adapters []Adapter
}

// Validate requires exactly one adapter to support config.
func (r *Registry) Validate(config agentconfig.Config) error {
	_, err := r.adapterFor("", config)
	return err
}

// NewRegistry constructs an immutable native adapter registry.
func NewRegistry(adapters ...Adapter) (*Registry, error) {
	copyAdapters := slices.Clone(adapters)
	names := make(map[string]struct{}, len(copyAdapters))
	for _, adapter := range copyAdapters {
		if adapter == nil || strings.TrimSpace(adapter.Name()) == "" {
			return nil, fmt.Errorf("register native hook adapter: %w", hookcmd.ErrInvalid)
		}
		if _, ok := names[adapter.Name()]; ok {
			return nil, fmt.Errorf("register native hook adapter %q: %w", adapter.Name(), hookcmd.ErrConflict)
		}
		names[adapter.Name()] = struct{}{}
	}
	return &Registry{adapters: copyAdapters}, nil
}

// Prepare requires exactly one adapter to support the requested ACP leaf.
func (r *Registry) Prepare(ctx context.Context, request LeafRequest) (PreparedLeaf, error) {
	adapter, err := r.adapterFor(request.LeafID, request.Config)
	if err != nil {
		return PreparedLeaf{}, err
	}
	prepared, err := adapter.Prepare(ctx, request)
	if err != nil {
		return PreparedLeaf{}, fmt.Errorf("prepare native hook leaf %q with adapter %q: %w", request.LeafID, adapter.Name(), err)
	}
	return prepared, nil
}

func (r *Registry) adapterFor(leafID string, config agentconfig.Config) (Adapter, error) {
	if r == nil {
		return nil, selectionError(leafID, nil)
	}
	var matches []Adapter
	for _, adapter := range r.adapters {
		if adapter.Supports(config) {
			matches = append(matches, adapter)
		}
	}
	if len(matches) != 1 {
		return nil, selectionError(leafID, matches)
	}
	return matches[0], nil
}

func selectionError(leafID string, matches []Adapter) error {
	if len(matches) == 0 {
		return fmt.Errorf("select native hook adapter for leaf %q: %w: %w", leafID, ErrNoAdapter, hookcmd.ErrUnsupported)
	}
	names := make([]string, 0, len(matches))
	for _, adapter := range matches {
		names = append(names, adapter.Name())
	}
	slices.Sort(names)
	return fmt.Errorf("select native hook adapter for leaf %q (%s): %w: %w", leafID, strings.Join(names, ", "), ErrMultipleAdapters, hookcmd.ErrConflict)
}
