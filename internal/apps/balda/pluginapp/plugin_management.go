package pluginapp

import (
	"context"
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// ManagementExpectedState returns a private-state/retained-content comparison.
func (s *Service) ManagementExpectedState(ctx context.Context, name string, a plugincmd.Authority) (string, error) {
	if err := s.managementAuthority(ctx, a); err != nil {
		return "", err
	}
	unlock := s.lockOwner()
	defer unlock()
	if err := s.managementAuthority(ctx, a); err != nil {
		return "", err
	}
	_, token, err := s.pluginObservationLocked(ctx, name)
	return token, err
}

// ManagementCandidate reviews one accepted install candidate or the locked upgrade.
func (s *Service) ManagementCandidate(ctx context.Context, name, marketplace string, a plugincmd.Authority) (plugincmd.CandidateView, error) {
	if err := s.managementAuthority(ctx, a); err != nil {
		return plugincmd.CandidateView{}, err
	}
	unlock := s.lockOwner()
	defer unlock()
	if err := s.managementAuthority(ctx, a); err != nil {
		return plugincmd.CandidateView{}, err
	}
	observed, token, err := s.pluginObservationLocked(ctx, name)
	if err != nil {
		return plugincmd.CandidateView{}, err
	}
	selected, err := s.resolveManagementCandidateLocked(ctx, observed, marketplace, false)
	if err != nil {
		code := "origin_unavailable"
		if observed.Install != nil && observed.OriginIdentity == "" {
			code = "origin_unverified"
		}
		return plugincmd.CandidateView{DiagnosticCodes: []string{code}}, err
	}
	c := selected.candidate
	return plugincmd.CandidateView{Name: c.Name, Version: c.Version, Description: c.Description, Marketplace: selected.source.Name, Revision: c.Revision, CandidateToken: discoveryCandidateToken(selected.observation, c), ExpectedState: token, Capabilities: c.Capabilities, DiagnosticCodes: append([]string(nil), c.DiagnosticCodes...)}, nil
}

// ManagementMutatePlugin runs the shared lifecycle under browser authority.
func (s *Service) ManagementMutatePlugin(ctx context.Context, r plugincmd.PluginMutation) (plugincmd.MutationOutcome, error) {
	outcome := plugincmd.MutationOutcome{PluginName: r.PluginName}
	if err := r.Validate(); err != nil {
		return outcome, err
	}
	if err := s.managementAuthority(ctx, r.Authority); err != nil {
		return outcome, err
	}
	unlock := s.lockOwner()
	defer unlock()
	release, err := s.managed.activator.LockCatalogMutation(ctx)
	if err != nil {
		return outcome, plugincmd.ErrUnavailable
	}
	defer release()
	// Validate before reconciliation, which can itself publish pending work.
	if err := s.managementAuthority(ctx, r.Authority); err != nil {
		return outcome, err
	}
	observed, token, err := s.pluginObservationLocked(ctx, r.PluginName)
	if err != nil {
		return outcome, err
	}
	if token != r.ExpectedState {
		return outcome, plugincmd.ErrConflict
	}
	if (r.Operation == plugincmd.Install) != (observed.Install == nil) {
		return outcome, plugincmd.ErrConflict
	}
	acceptance := &pluginAcceptance{authority: &r.Authority, expectedSQL: observed.Token(), availability: s.managed.revisionAvailability(observed)}
	var selected selectedPluginCandidate
	if r.Operation == plugincmd.Install || r.Operation == plugincmd.Upgrade {
		selected, err = s.resolveManagementCandidateLocked(ctx, observed, r.MarketplaceName, false)
		if err != nil {
			return outcome, err
		}
		if discoveryCandidateToken(selected.observation, selected.candidate) != r.CandidateToken {
			return outcome, plugincmd.ErrConflict
		}
		acceptance.marketplace = selected.source.Name
		acceptance.expectedMarketplace = selected.observation.Token()
		acceptance.revision = selected.candidate.Revision
		if r.Operation == plugincmd.Install {
			identity := selected.originIdentity
			acceptance.originIdentity = &identity
		}
	}
	current, err := s.management.ObservePlugin(ctx, r.PluginName)
	if err != nil {
		return outcome, err
	}
	if current.Token() != observed.Token() {
		return outcome, plugincmd.ErrConflict
	}
	// Observation and candidate checks can outlive the browser session.
	if err := s.managementAuthority(ctx, r.Authority); err != nil {
		return outcome, err
	}
	if _, err := s.managed.reconcilePending(ctx, r.PluginName); err != nil {
		return outcome, plugincmd.ErrRecoveryPending
	}
	after, err := s.management.ObservePlugin(ctx, r.PluginName)
	if err != nil {
		return outcome, err
	}
	// Recovery can complete the exact previously observed pending intents only.
	beforeCompleted := observed
	beforeCompleted.Pending = nil
	afterCompleted := after
	afterCompleted.Pending = nil
	if beforeCompleted.Token() != afterCompleted.Token() || len(after.Pending) != 0 || s.managed.revisionAvailability(after) != acceptance.availability {
		return outcome, plugincmd.ErrConflict
	}
	acceptance.expectedSQL = after.Token()

	switch r.Operation {
	case plugincmd.Install, plugincmd.Upgrade:
		plugin, materializeErr := s.materializeAcceptedCandidateLocked(ctx, selected, after.Install)
		if materializeErr != nil {
			return outcome, materializeErr
		}
		if after.Install != nil && after.Install.ActiveRevisionID == selected.candidate.Revision {
			return outcome, plugincmd.ErrConflict
		}
		err = s.managed.installLocked(ctx, plugin, r.Operation == plugincmd.Upgrade, acceptance)
	case plugincmd.Enable, plugincmd.Disable:
		if observed.Install.Enabled == (r.Operation == plugincmd.Enable) {
			return outcome, plugincmd.ErrConflict
		}
		err = s.managed.setEnabledLocked(ctx, r.PluginName, r.Operation == plugincmd.Enable, acceptance)
	case plugincmd.Rollback:
		if observed.Install.ActiveRevisionID == r.Revision {
			return outcome, plugincmd.ErrConflict
		}
		err = s.managed.rollbackLocked(ctx, r.PluginName, r.Revision, acceptance)
	case plugincmd.Remove:
		err = s.managed.removeLocked(ctx, r.PluginName, acceptance)
	}
	outcome.Accepted = acceptance.accepted
	outcome.RecoveryPending = outcome.Accepted && err != nil
	if outcome.RecoveryPending {
		return outcome, plugincmd.ErrRecoveryPending
	}
	if err != nil {
		return outcome, boundedPluginError(err)
	}
	return outcome, nil
}

func boundedPluginError(err error) error {
	for _, bounded := range []error{plugincmd.ErrInvalid, plugincmd.ErrForbidden, plugincmd.ErrConflict, plugincmd.ErrNotFound, plugincmd.ErrRecoveryPending} {
		if errors.Is(err, bounded) {
			return bounded
		}
	}
	return plugincmd.ErrUnavailable
}
