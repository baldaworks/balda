package pluginapp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
)

func (s *Service) addMarketplaceTrusted(ctx context.Context, src MarketplaceSource) error {
	src = normalizeSource(src)
	if err := s.validateManagedMarketplace(src); err != nil {
		return err
	}
	unlock := s.lockOwner()
	defer unlock()
	observed, err := s.management.ObserveMarketplace(ctx, src.Name)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(src)
	return s.management.CommitMarketplaceTrusted(ctx, state.MarketplaceCommit{Name: src.Name, ExpectedState: observed.Token(), DefinitionJSON: raw, Operation: "upsert", AuditID: uuid.NewString(), At: time.Now().UTC()})
}

func (s *Service) removeMarketplaceTrusted(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if !safeMarketplaceName(name) {
		return plugincmd.ErrInvalid
	}
	unlock := s.lockOwner()
	defer unlock()
	observed, err := s.management.ObserveMarketplace(ctx, name)
	if err != nil {
		return err
	}
	if observed.DefinitionJSON == nil {
		return nil
	}
	return s.management.CommitMarketplaceTrusted(ctx, state.MarketplaceCommit{Name: name, ExpectedState: observed.Token(), Operation: "remove", AuditID: uuid.NewString(), At: time.Now().UTC()})
}

func (s *Service) refreshMarketplacesTrusted(ctx context.Context, name string) ([]MarketplaceUpgradeResult, error) {
	unlock := s.lockOwner()
	defer unlock()
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, plugincmd.ErrUnavailable
	}
	name = strings.TrimSpace(name)
	if name != "" {
		filtered := make([]MarketplaceSource, 0, 1)
		for _, src := range sources {
			if src.Name == name {
				filtered = append(filtered, src)
			}
		}
		if len(filtered) == 0 {
			return nil, plugincmd.ErrNotFound
		}
		sources = filtered
	}
	results := make([]MarketplaceUpgradeResult, 0, len(sources))
	for _, src := range sources {
		observed, _, old, err := s.marketplaceStateLocked(ctx, src.Name)
		if err != nil {
			return nil, err
		}
		success, code := s.discoverMarketplaceLocked(ctx, src)
		now := time.Now().UTC()
		metadata := acceptedDiscovery(old, success, code, now)
		raw, _ := json.Marshal(metadata)
		operation := marketplaceRefresh
		if code != "" {
			operation = marketplaceRefreshFailed
		}
		if err := s.management.CommitMarketplaceTrusted(ctx, state.MarketplaceCommit{Name: src.Name, ExpectedState: observed.Token(), DefinitionJSON: observed.DefinitionJSON, MetadataJSON: raw, Operation: operation, AuditID: uuid.NewString(), At: now}); err != nil {
			return nil, err
		}
		if code != "" {
			return nil, plugincmd.ErrUnavailable
		}
		detail, err := s.marketplaceDetailLocked(ctx, src.Name)
		if err != nil {
			return nil, err
		}
		status := marketplaceStatusView(detail)
		results = append(results, MarketplaceUpgradeResult{Name: src.Name, Source: detail.Source, PluginCount: detail.AvailablePlugins, Refreshed: true, Status: status})
	}
	return results, nil
}
