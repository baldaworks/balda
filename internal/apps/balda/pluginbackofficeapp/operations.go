// Package pluginbackofficeapp binds shared plugin policy to browser operations.
package pluginbackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// Owner is the shared plugin application's authorized management surface.
type Owner interface {
	ManagementInventory(ctx context.Context, authority plugincmd.Authority) ([]plugincmd.InstalledView, error)
	ManagementDetail(ctx context.Context, name string, authority plugincmd.Authority) (plugincmd.PluginDetail, error)
	ManagementMarketplaces(ctx context.Context, authority plugincmd.Authority) ([]plugincmd.MarketplaceView, error)
	ManagementMarketplace(ctx context.Context, name string, authority plugincmd.Authority) (plugincmd.MarketplaceDetail, error)
	ManagementAddMarketplace(ctx context.Context, request plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error)
	ManagementRefreshMarketplace(ctx context.Context, request plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error)
	ManagementRemoveMarketplace(ctx context.Context, request plugincmd.MarketplaceMutation) error
	ManagementCandidate(ctx context.Context, name, marketplace string, authority plugincmd.Authority) (plugincmd.CandidateView, error)
	ManagementMutatePlugin(ctx context.Context, request plugincmd.PluginMutation) (plugincmd.MutationOutcome, error)
}

// RuntimeStatusReader supplies catalog-owner evidence independently of selection.
type RuntimeStatusReader interface {
	Status(name, revision string) plugincmd.RuntimeStatus
}

// Operations uses the existing global owner; it never constructs a lifecycle.
type Operations struct {
	owner  Owner
	health RuntimeStatusReader
}

// New binds browser operations to existing host owners.
func New(owner Owner, health RuntimeStatusReader) *Operations {
	return &Operations{owner: owner, health: health}
}

// Inventory returns selected installations and separate current runtime health.
func (o *Operations) Inventory(ctx context.Context, a plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	items, err := o.owner.ManagementInventory(ctx, a)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Runtime = o.status(items[i].Name, items[i].Revision)
	}
	return items, nil
}

// Detail keeps retained contributions visible independently of live health.
func (o *Operations) Detail(ctx context.Context, name string, a plugincmd.Authority) (plugincmd.PluginDetail, error) {
	item, err := o.owner.ManagementDetail(ctx, name, a)
	if err != nil {
		return plugincmd.PluginDetail{}, err
	}
	item.Runtime = o.status(item.Name, item.Revision)
	return item, nil
}
func (o *Operations) status(name, revision string) plugincmd.RuntimeStatus {
	if o.health == nil {
		return plugincmd.RuntimeStatus{DiagnosticCodes: []string{"catalog_unavailable"}}
	}
	return o.health.Status(name, revision)
}

// Marketplaces reads accepted discovery evidence.
func (o *Operations) Marketplaces(ctx context.Context, a plugincmd.Authority) ([]plugincmd.MarketplaceView, error) {
	return o.owner.ManagementMarketplaces(ctx, a)
}

// Marketplace reads one configured definition.
func (o *Operations) Marketplace(ctx context.Context, name string, a plugincmd.Authority) (plugincmd.MarketplaceDetail, error) {
	return o.owner.ManagementMarketplace(ctx, name, a)
}

// AddMarketplace adds one unique definition without discovery.
func (o *Operations) AddMarketplace(ctx context.Context, r plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error) {
	return o.owner.ManagementAddMarketplace(ctx, r)
}

// RefreshMarketplace explicitly accepts a new discovery attempt.
func (o *Operations) RefreshMarketplace(ctx context.Context, r plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error) {
	return o.owner.ManagementRefreshMarketplace(ctx, r)
}

// RemoveMarketplace removes only the confirmed definition.
func (o *Operations) RemoveMarketplace(ctx context.Context, r plugincmd.MarketplaceMutation) error {
	return o.owner.ManagementRemoveMarketplace(ctx, r)
}

// Candidate reviews an accepted package, resolving upgrades from locked origin.
func (o *Operations) Candidate(ctx context.Context, name, marketplace string, a plugincmd.Authority) (plugincmd.CandidateView, error) {
	return o.owner.ManagementCandidate(ctx, name, marketplace, a)
}

// MutatePlugin delegates the guarded lifecycle and its truthful outcome.
func (o *Operations) MutatePlugin(ctx context.Context, r plugincmd.PluginMutation) (plugincmd.MutationOutcome, error) {
	return o.owner.ManagementMutatePlugin(ctx, r)
}
