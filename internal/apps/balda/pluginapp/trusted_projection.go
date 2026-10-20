package pluginapp

import (
	"context"
	"sort"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func marketplaceStatusView(detail plugincmd.MarketplaceDetail) MarketplaceStatus {
	view := detail.MarketplaceView
	status := MarketplaceStatus{Name: view.Name, Source: view.Source, Kind: view.Kind, Ref: view.Ref, Sparse: append([]string(nil), view.Sparse...), ResolvedRef: view.ResolvedRevision, AvailablePlugins: view.AvailablePlugins, LastAttemptStatus: view.LastAttemptStatus, FailureCode: view.FailureCode}
	if !view.LastAttemptAt.IsZero() {
		status.LastAttemptAt = view.LastAttemptAt.Format(time.RFC3339)
	}
	if !view.LastSuccessfulAt.IsZero() {
		status.LastRefreshedAt = view.LastSuccessfulAt.Format(time.RFC3339)
		status.Cached = true
		status.ManifestPresent = true
	}
	return status
}

func (s *Service) marketplaceStatusesLocked(ctx context.Context) ([]MarketplaceStatus, error) {
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, plugincmd.ErrUnavailable
	}
	statuses := make([]MarketplaceStatus, 0, len(sources))
	for _, source := range sources {
		detail, err := s.marketplaceDetailLocked(ctx, source.Name)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, marketplaceStatusView(detail))
	}
	return statuses, nil
}

func (s *Service) availableLocked(ctx context.Context) ([]AvailablePlugin, error) {
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, plugincmd.ErrUnavailable
	}
	installs, err := s.managed.store.ListPluginInstalls(ctx)
	if err != nil {
		return nil, plugincmd.ErrUnavailable
	}
	installed := make(map[string]bool, len(installs))
	for _, install := range installs {
		installed[install.PluginID] = true
	}
	var result []AvailablePlugin
	for _, source := range sources {
		_, _, metadata, err := s.marketplaceStateLocked(ctx, source.Name)
		if err != nil {
			return nil, err
		}
		detail, err := s.marketplaceDetailLocked(ctx, source.Name)
		if err != nil {
			return nil, err
		}
		for i, candidate := range detail.Candidates {
			plugin := AvailablePlugin{Category: metadata.LastSuccessful.Candidates[i].Category, Name: candidate.Name, DisplayName: candidate.Name, Description: candidate.Description, Version: candidate.Version, Marketplace: source.Name, MarketplaceLabel: source.Name, Installed: installed[candidate.Name], Revision: candidate.Revision, Installable: candidate.CandidateToken != "", Stale: detail.LastAttemptStatus != discoverySucceeded, DeclaresBaldaExtension: candidate.DeclaresBaldaExtension,
				capabilities: CapabilitySummary{Commands: candidate.Capabilities.Commands, Skills: candidate.Capabilities.Skills, MCPServers: candidate.Capabilities.MCPServers, Diagnostics: candidate.Capabilities.Diagnostics}, marketplaceAlias: metadata.LastSuccessful.IndexName}
			codes := append([]string(nil), candidate.DiagnosticCodes...)
			if plugin.Stale {
				codes = append(codes, "marketplace_discovery_stale")
			}
			for _, code := range codes {
				plugin.Diagnostics = append(plugin.Diagnostics, runtimecatalogcmd.Diagnostic{Code: code, Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: candidate.Name}})
			}
			result = append(result, plugin)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Marketplace == result[j].Marketplace {
			return result[i].Name < result[j].Name
		}
		return result[i].Marketplace < result[j].Marketplace
	})
	return result, nil
}
