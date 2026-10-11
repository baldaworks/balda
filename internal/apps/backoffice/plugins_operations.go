package backoffice

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// PluginsOperations is Backoffice's consuming port for global plugin management.
type PluginsOperations interface {
	Inventory(ctx context.Context, authority plugincmd.Authority) ([]plugincmd.InstalledView, error)
	Detail(ctx context.Context, name string, authority plugincmd.Authority) (plugincmd.PluginDetail, error)
	Marketplaces(ctx context.Context, authority plugincmd.Authority) ([]plugincmd.MarketplaceView, error)
	Marketplace(ctx context.Context, name string, authority plugincmd.Authority) (plugincmd.MarketplaceDetail, error)
	AddMarketplace(ctx context.Context, request plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error)
	RefreshMarketplace(ctx context.Context, request plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error)
	RemoveMarketplace(ctx context.Context, request plugincmd.MarketplaceMutation) error
	Candidate(ctx context.Context, name, marketplace string, authority plugincmd.Authority) (plugincmd.CandidateView, error)
	MutatePlugin(ctx context.Context, request plugincmd.PluginMutation) (plugincmd.MutationOutcome, error)
}

// ConfigurePluginsOperations binds the shared host owner before HTTP startup.
func (r *Runtime) ConfigurePluginsOperations(operations PluginsOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("plugin operations must be configured before Backoffice start")
	}
	r.plugins = operations
	return nil
}
