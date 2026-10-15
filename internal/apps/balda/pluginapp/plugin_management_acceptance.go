package pluginapp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
)

const pluginAdoptOrigin = "adopt-origin"

// pluginAcceptance is an explicit per-operation fence, never ambient context.
type pluginAcceptance struct {
	authority           *plugincmd.Authority
	expectedSQL         string
	availability        string
	marketplace         string
	expectedMarketplace string
	revision            string
	originIdentity      *string
	accepted            bool
}

func (m *managedLifecycle) acceptAndPublish(ctx context.Context, operation, name, from, to string, install *state.PluginInstallRecord, revision *state.PluginRevisionRecord, snapshot runtimecatalogcmd.PreparedApplicationCandidate, a *pluginAcceptance) error {
	now := m.now().UTC()
	if install != nil {
		install.UpdatedAt = now
	}
	intent := state.PluginActivationIntent{IntentID: uuid.NewString(), PluginID: name, FromRevisionID: from, ToRevisionID: to, Operation: operation, State: state.PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if m.management == nil {
		var err error
		switch operation {
		case "remove":
			err = m.store.DeactivatePlugin(ctx, intent)
		case pluginAdoptOrigin:
			err = m.store.AdoptPluginOrigin(ctx, intent, *install)
		default:
			err = m.store.ActivatePlugin(ctx, intent, *install)
		}
		if err != nil {
			return err
		}
	} else {
		observed, err := m.management.ObservePlugin(ctx, name)
		if err != nil {
			return err
		}
		mutation := state.PluginCommit{PluginID: name, ExpectedState: observed.Token(), Install: install, Revision: revision, Intent: intent, AuditID: uuid.NewString()}
		if a != nil {
			if observed.Token() != a.expectedSQL || m.revisionAvailability(observed) != a.availability {
				return plugincmd.ErrConflict
			}
			mutation.ExpectedState = a.expectedSQL
			mutation.MarketplaceName = a.marketplace
			mutation.ExpectedMarketplaceState = a.expectedMarketplace
			mutation.OriginIdentity = a.originIdentity
			if a.authority != nil {
				authority := *a.authority
				authority.At = now
				mutation.Authority = &authority
			}
		}
		if mutation.Authority != nil {
			err = m.management.CommitPlugin(ctx, mutation)
		} else {
			err = m.management.CommitPluginTrusted(ctx, mutation)
		}
		if err != nil {
			return err
		}
	}
	if a != nil {
		a.accepted = true
	}
	if err := m.activator.PublishCandidate(ctx, snapshot); err != nil {
		if m.management != nil {
			return errors.Join(plugincmd.ErrRecoveryPending, err)
		}
		return err
	}
	if err := m.store.CompletePluginActivation(ctx, intent.IntentID, m.now().UTC()); err != nil {
		if m.management != nil {
			return errors.Join(plugincmd.ErrRecoveryPending, err)
		}
		return err
	}
	return nil
}

func (m *managedLifecycle) revisionAvailability(observed state.PluginObservation) string {
	type availability struct {
		Revision  string
		Available bool
	}
	states := make([]availability, 0, len(observed.Revisions))
	for _, revision := range observed.Revisions {
		available := false
		root, err := resolveContainedPath(m.stateDir, filepath.FromSlash(revision.RelativeRoot))
		if err == nil {
			actual, err := m.loader.InspectRevision(root)
			available = err == nil && string(actual) == revision.RevisionID
		}
		states = append(states, availability{Revision: revision.RevisionID, Available: available})
	}
	raw, _ := json.Marshal(states)
	return plugincmd.Digest(string(raw))
}

func (s *Service) pluginObservationLocked(ctx context.Context, name string) (state.PluginObservation, string, error) {
	if s.management == nil || s.managed == nil || !safeMarketplaceName(name) {
		return state.PluginObservation{}, "", plugincmd.ErrInvalid
	}
	observed, err := s.management.ObservePlugin(ctx, name)
	if err != nil {
		return observed, "", err
	}
	return observed, plugincmd.Digest(observed.Token() + s.managed.revisionAvailability(observed)), nil
}
