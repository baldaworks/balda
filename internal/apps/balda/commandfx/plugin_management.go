package commandfx

import (
	"context"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const maxStatusItems = 64

// PluginManagementAdapter binds the command actor's neutral management port
// to pluginapp and runtime status implementations.
type PluginManagementAdapter struct {
	plugins *pluginapp.Service
	runtime *CatalogStatusReader
}

func NewPluginManagementAdapter(plugins *pluginapp.Service, runtime *CatalogStatusReader) *PluginManagementAdapter {
	return &PluginManagementAdapter{plugins: plugins, runtime: runtime}
}

func (a *PluginManagementAdapter) ListInstalled(ctx context.Context) ([]plugincmd.PluginSummary, error) {
	items, err := a.plugins.ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]plugincmd.PluginSummary, 0, len(items))
	for _, item := range items {
		out = append(out, pluginSummary(item))
	}
	return out, nil
}

func (a *PluginManagementAdapter) ListAvailable(ctx context.Context) ([]plugincmd.AvailablePlugin, error) {
	items, err := a.plugins.ListAvailable(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]plugincmd.AvailablePlugin, 0, len(items))
	for _, item := range items {
		out = append(out, availablePlugin(item))
	}
	return out, nil
}

func (a *PluginManagementAdapter) GetInstalled(ctx context.Context, name string) (plugincmd.PluginSummary, bool, error) {
	item, ok, err := a.plugins.GetInstalled(ctx, name)
	return pluginSummary(item), ok, err
}

func (a *PluginManagementAdapter) GetAvailable(ctx context.Context, name string) (plugincmd.AvailablePlugin, bool, error) {
	item, ok, err := a.plugins.GetAvailable(ctx, name)
	return availablePlugin(item), ok, err
}

func (a *PluginManagementAdapter) Install(ctx context.Context, ref string) error {
	return a.plugins.Install(ctx, ref)
}
func (a *PluginManagementAdapter) Upgrade(ctx context.Context, ref string) error {
	return a.plugins.Upgrade(ctx, ref)
}
func (a *PluginManagementAdapter) AdoptOrigin(ctx context.Context, ref string) error {
	return a.plugins.AdoptOrigin(ctx, ref)
}
func (a *PluginManagementAdapter) Enable(ctx context.Context, name string) error {
	return a.plugins.Enable(ctx, name)
}
func (a *PluginManagementAdapter) Disable(ctx context.Context, name string) error {
	return a.plugins.Disable(ctx, name)
}
func (a *PluginManagementAdapter) Rollback(ctx context.Context, name, revision string) error {
	return a.plugins.Rollback(ctx, name, revision)
}
func (a *PluginManagementAdapter) RemoveInstalled(ctx context.Context, name string) error {
	return a.plugins.RemoveInstalled(ctx, name)
}
func (a *PluginManagementAdapter) Purge(ctx context.Context, name, revision string, purgeData bool) error {
	return a.plugins.Purge(ctx, name, revision, purgeData)
}

func (a *PluginManagementAdapter) Status(ctx context.Context, name string) (plugincmd.PluginStatus, bool, error) {
	state, ok, err := a.plugins.Inspect(ctx, name)
	if err != nil || !ok {
		return plugincmd.PluginStatus{}, ok, err
	}
	status := plugincmd.PluginStatus{
		Name: state.Name, Version: state.Version, Marketplace: state.Marketplace,
		Origin: state.Origin, Revision: state.Revision, Enabled: state.Enabled, Drifted: state.Drifted,
		Capabilities: plugincmd.CapabilitySummary{Commands: state.Capabilities.Commands, Skills: state.Capabilities.Skills, MCPServers: state.Capabilities.MCPServers, Diagnostics: state.Capabilities.Diagnostics},
	}
	if a.runtime != nil {
		status.Runtime = a.runtime.Status(state.Name, state.Revision)
	}
	return status, true, nil
}

func (a *PluginManagementAdapter) ListMarketplaceStatuses(ctx context.Context) ([]plugincmd.MarketplaceStatus, error) {
	items, err := a.plugins.ListMarketplaceStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]plugincmd.MarketplaceStatus, 0, len(items))
	for _, item := range items {
		out = append(out, marketplaceStatus(item))
	}
	return out, nil
}

func (a *PluginManagementAdapter) GetMarketplaceStatus(ctx context.Context, name string) (plugincmd.MarketplaceStatus, bool, error) {
	item, ok, err := a.plugins.GetMarketplaceStatus(ctx, name)
	return marketplaceStatus(item), ok, err
}

func (a *PluginManagementAdapter) AddMarketplace(ctx context.Context, src plugincmd.MarketplaceSource) error {
	return a.plugins.AddMarketplace(ctx, pluginapp.MarketplaceSource{Name: src.Name, Source: src.Source, Ref: src.Ref, Sparse: append([]string(nil), src.Sparse...)})
}

func (a *PluginManagementAdapter) UpgradeMarketplaces(ctx context.Context, name string) ([]plugincmd.MarketplaceUpgradeResult, error) {
	items, err := a.plugins.UpgradeMarketplaces(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]plugincmd.MarketplaceUpgradeResult, 0, len(items))
	for _, item := range items {
		out = append(out, plugincmd.MarketplaceUpgradeResult{Name: item.Name, Source: plugincmd.PublicMarketplaceSource(item.Source), PluginCount: item.PluginCount, Refreshed: item.Refreshed, Status: marketplaceStatus(item.Status)})
	}
	return out, nil
}

func (a *PluginManagementAdapter) RemoveMarketplace(ctx context.Context, name string) error {
	return a.plugins.RemoveMarketplace(ctx, name)
}

func pluginSummary(item pluginapp.PluginSummary) plugincmd.PluginSummary {
	return plugincmd.PluginSummary{Name: item.Name, Version: item.Version, Description: item.Description}
}

func availablePlugin(item pluginapp.AvailablePlugin) plugincmd.AvailablePlugin {
	codes := make([]string, 0, len(item.Diagnostics))
	for _, diagnostic := range item.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return plugincmd.AvailablePlugin{Name: item.Name, DisplayName: item.DisplayName, Description: item.Description, Version: item.Version, Marketplace: item.Marketplace, MarketplaceLabel: item.MarketplaceLabel, Category: item.Category, Installed: item.Installed, DiagnosticCodes: boundedSorted(codes)}
}

func marketplaceStatus(item pluginapp.MarketplaceStatus) plugincmd.MarketplaceStatus {
	return plugincmd.MarketplaceStatus{Name: item.Name, Source: plugincmd.PublicMarketplaceSource(item.Source), Kind: item.Kind, Ref: item.Ref, Sparse: append([]string(nil), item.Sparse...), Cached: item.Cached, LastRefreshedAt: item.LastRefreshedAt, ResolvedRef: item.ResolvedRef, ManifestPresent: item.ManifestPresent, AvailablePlugins: item.AvailablePlugins}
}

// CatalogStatusReader projects non-secret desired and observed runtime state.
type CatalogStatusReader struct {
	catalog *runtimecatalog.Store
	mcp     *mcpruntime.Reconciler
	ads     *AdvertisementProjector
}

func NewCatalogStatusReader(catalog *runtimecatalog.Store, mcp *mcpruntime.Reconciler, ads *AdvertisementProjector) *CatalogStatusReader {
	return &CatalogStatusReader{catalog: catalog, mcp: mcp, ads: ads}
}

func (r *CatalogStatusReader) Status(pluginName, activeRevision string) plugincmd.RuntimeStatus {
	if r == nil || r.catalog == nil {
		return plugincmd.RuntimeStatus{DiagnosticCodes: []string{"catalog_unavailable"}}
	}
	snapshot, err := r.catalog.Application()
	if err != nil {
		return plugincmd.RuntimeStatus{DiagnosticCodes: []string{"catalog_unavailable"}}
	}
	status := plugincmd.RuntimeStatus{SnapshotID: string(snapshot.ID), SnapshotSequence: snapshot.Sequence}
	advertisements := r.ads.status(pluginName, runtimecatalogcmd.RevisionID(activeRevision), snapshot.Sequence)
	status.Advertisements = advertisements.Advertisements
	status.ProjectionLag = advertisements.Lag
	status.ProjectionOmissions = append(status.ProjectionOmissions, advertisements.Omissions...)
	skillNames := make(map[string]int)
	pluginSkillNames := make(map[string]struct{})
	for _, descriptor := range snapshot.Skills {
		skillNames[descriptor.Name]++
		if pluginSource(descriptor.ID.Source, pluginName) && descriptor.Revision == runtimecatalogcmd.RevisionID(activeRevision) {
			status.Skills++
			pluginSkillNames[descriptor.Name] = struct{}{}
		}
	}
	for name := range pluginSkillNames {
		if skillNames[name] > 1 {
			status.SkillAmbiguities++
		}
	}
	desiredMCP := make(map[mcpruntime.InstanceKey]struct{})
	for _, descriptor := range snapshot.MCPServers {
		if pluginSource(descriptor.ID.Source, pluginName) && descriptor.Revision == runtimecatalogcmd.RevisionID(activeRevision) {
			status.DesiredMCPServers++
			desiredMCP[mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}] = struct{}{}
		}
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if pluginSource(diagnostic.Source, pluginName) {
			status.DiagnosticCodes = append(status.DiagnosticCodes, diagnostic.Code)
		}
	}
	if r.mcp != nil {
		healthByKey := make(map[mcpruntime.InstanceKey]mcpruntime.Health)
		for _, health := range r.mcp.Health() {
			healthByKey[health.Key] = health
		}
		for key := range desiredMCP {
			health, found := healthByKey[key]
			if !found {
				status.DegradedMCPServers++
				status.ProjectionOmissions = append(status.ProjectionOmissions, "mcp_not_observed")
				continue
			}
			if health.State == mcpruntime.HealthReady {
				status.ReadyMCPServers++
			} else {
				status.DegradedMCPServers++
			}
			if health.ErrorClass != "" {
				status.ProjectionOmissions = append(status.ProjectionOmissions, health.ErrorClass)
			}
		}
	} else if status.DesiredMCPServers > 0 {
		status.DegradedMCPServers = status.DesiredMCPServers
		status.ProjectionOmissions = append(status.ProjectionOmissions, "mcp_status_unavailable")
	}
	status.DiagnosticCodes = boundedSorted(status.DiagnosticCodes)
	status.ProjectionOmissions = boundedSorted(status.ProjectionOmissions)
	return status
}

func pluginSource(source runtimecatalogcmd.SourceID, name string) bool {
	return source.Kind == runtimecatalogcmd.SourceKindPlugin && source.Name == name
}

func boundedSorted(values []string) []string {
	sort.Strings(values)
	if len(values) > maxStatusItems {
		values = values[:maxStatusItems]
	}
	return values
}
