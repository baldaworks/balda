package pluginapp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
)

// mutatePluginTrusted holds the same owner and catalog gate as browser writes.
func (s *Service) mutatePluginTrusted(ctx context.Context, selector, operation string) error {
	name, marketplace := splitSelector(selector)
	if !safeMarketplaceName(name) {
		return plugincmd.ErrInvalid
	}
	unlock := s.lockOwner()
	defer unlock()
	release, err := s.managed.activator.LockCatalogMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	if _, err := s.managed.reconcilePending(ctx, name); err != nil {
		return err
	}
	observed, err := s.management.ObservePlugin(ctx, name)
	if err != nil {
		return err
	}
	if operation == string(plugincmd.Upgrade) && observed.Install == nil {
		return plugincmd.ErrNotFound
	}
	// A trusted command explicitly requests fresh source discovery. Accept evidence
	// under the owner before selecting the package; browser GET never does this.
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return err
	}
	var selectedSourceFailed bool
	for _, src := range sources {
		record, _, old, err := s.marketplaceStateLocked(ctx, src.Name)
		if err != nil {
			return err
		}
		success, code := s.discoverMarketplaceLocked(ctx, src)
		now := time.Now().UTC()
		metadata := acceptedDiscovery(old, success, code, now)
		raw, _ := json.Marshal(metadata)
		op := marketplaceRefresh
		if code != "" {
			op = marketplaceRefreshFailed
			selectedSourceFailed = selectedSourceFailed || src.Name == marketplace
		}
		if err := s.management.CommitMarketplaceTrusted(ctx, state.MarketplaceCommit{Name: src.Name, ExpectedState: record.Token(), DefinitionJSON: record.DefinitionJSON, MetadataJSON: raw, Operation: op, AuditID: uuid.NewString(), At: now}); err != nil {
			return err
		}
	}
	if selectedSourceFailed {
		return plugincmd.ErrUnavailable
	}
	selectionObservation := observed
	if operation == pluginAdoptOrigin {
		selectionObservation.Install = nil
	}
	selected, err := s.resolveManagementCandidateLocked(ctx, selectionObservation, marketplace, true)
	if err != nil {
		return err
	}
	acceptance := &pluginAcceptance{expectedSQL: observed.Token(), availability: s.managed.revisionAvailability(observed), marketplace: selected.source.Name, expectedMarketplace: selected.observation.Token(), revision: selected.candidate.Revision}
	if observed.Install == nil || operation == pluginAdoptOrigin {
		identity := selected.originIdentity
		acceptance.originIdentity = &identity
	}
	installed := observed.Install
	if operation == pluginAdoptOrigin {
		installed = nil
	}
	plugin, err := s.materializeAcceptedCandidateLocked(ctx, selected, installed)
	if err != nil {
		return err
	}
	if operation == pluginAdoptOrigin {
		return s.managed.adoptOriginLocked(ctx, plugin, acceptance)
	}
	return s.managed.installLocked(ctx, plugin, operation == string(plugincmd.Upgrade), acceptance)
}

func (m *managedLifecycle) trustedBaseline(ctx context.Context, name string, a *pluginAcceptance) (*pluginAcceptance, error) {
	if a != nil {
		return a, nil
	}
	observed, err := m.management.ObservePlugin(ctx, name)
	if err != nil {
		return nil, err
	}
	return &pluginAcceptance{expectedSQL: observed.Token(), availability: m.revisionAvailability(observed)}, nil
}
