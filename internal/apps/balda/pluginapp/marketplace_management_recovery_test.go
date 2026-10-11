package pluginapp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestMarketplaceDiscoveryFailureSurvivesReopen(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	oldToken := good.Candidates[0].CandidateToken
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"name":"index-identity","plugins":[{"name":"demo","path":"plugins/demo"},{"name":"DEMO","path":"missing-invalid-package"}]}`)
	failed, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: good.ExpectedState, Authority: a})
	if !errors.Is(err, plugincmd.ErrUnavailable) || failed.LastAttemptStatus != discoveryFailed || failed.FailureCode != discoveryDuplicateName {
		t.Fatalf("duplicate normalized identity: %+v error=%v", failed, err)
	}
	if failed.LastSuccessfulAt != good.LastSuccessfulAt || failed.ExpectedState == good.ExpectedState || len(failed.Candidates) != 1 || failed.Candidates[0].CandidateToken != "" {
		t.Fatal("failed refresh lost prior success or kept actionable candidates")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = state.NewSQLiteProvider(t.Context(), filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), &recordingActivator{}, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	// Removing the source proves GET reads durable evidence without touching it.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	read, err := s.ManagementMarketplace(t.Context(), "market", a)
	if err != nil {
		t.Fatal(err)
	}
	if read.LastAttemptStatus != discoveryFailed || read.FailureCode != discoveryDuplicateName || read.LastSuccessfulAt != good.LastSuccessfulAt || read.Candidates[0].CandidateToken != "" {
		t.Fatalf("restart lost failed discovery: %+v", read)
	}
	unlock := s.lockOwner()
	observation, _, candidate, candidateErr := s.acceptedCandidateLocked(t.Context(), "market", "demo", oldToken)
	err = candidateErr
	if observation.Generation == 0 || candidate.Name != "" {
		t.Fatal("failed discovery returned selectable candidate")
	}
	unlock()
	if !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("old candidate after failure=%v", err)
	}
}

func TestMarketplaceDiscoveryEmptyAndInvalidEntries(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"name":"index-identity","plugins":[]}`)
	empty, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if empty.LastAttemptStatus != "succeeded" || empty.AvailablePlugins != 0 || empty.LastSuccessfulAt.IsZero() {
		t.Fatalf("empty discovery=%+v", empty)
	}
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"name":"index-identity","plugins":[{"name":"demo","path":"plugins/demo"},{"name":"missing","path":"outside"}]}`)
	partial, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: empty.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Candidates) != 1 || len(partial.DiagnosticCodes) == 0 || partial.Candidates[0].CandidateToken == "" {
		t.Fatalf("bounded invalid entry=%+v", partial)
	}
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: "market", Source: root}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.ManagementMarketplace(t.Context(), "market", a)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ExpectedState == partial.ExpectedState || len(changed.Candidates) != 0 {
		t.Fatal("chat replacement did not invalidate discovery")
	}
	if _, err := s.UpgradeMarketplaces(t.Context(), "market"); err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.ManagementMarketplace(t.Context(), "market", a)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed.Candidates) != 1 || refreshed.Candidates[0].CandidateToken == "" {
		t.Fatal("chat refresh did not accept discovery")
	}
}

func TestMarketplaceDiscoveryRejectsDuplicateManifestIdentity(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if err := copyDir(filepath.Join(root, "plugins", "demo"), filepath.Join(root, "plugins", "another")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"name":"index-identity","plugins":[{"name":"demo","path":"plugins/demo"},{"name":"alias","path":"plugins/another"}]}`)
	failed, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: good.ExpectedState, Authority: a})
	if !errors.Is(err, plugincmd.ErrUnavailable) || failed.FailureCode != discoveryDuplicateName || failed.LastSuccessfulAt != good.LastSuccessfulAt || failed.Candidates[0].CandidateToken != "" {
		t.Fatalf("duplicate manifest identity accepted: failure=%s error=%v", failed.FailureCode, err)
	}
}

func TestMarketplaceManagementRemoveRetainsInstalledPlugin(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Install(t.Context(), "demo@index-identity"); err != nil {
		t.Fatal(err)
	}
	before, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found {
		t.Fatalf("install found=%v error=%v", found, err)
	}
	revisions, err := p.Plugins().ListPluginRevisions(t.Context(), "demo")
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions=%v error=%v", revisions, err)
	}
	request := plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a}
	if err := s.ManagementRemoveMarketplace(t.Context(), request); !errors.Is(err, plugincmd.ErrInvalid) {
		t.Fatalf("unconfirmed remove=%v", err)
	}
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: "market", Source: root}); err != nil {
		t.Fatal(err)
	}
	request.Confirm = true
	if err := s.ManagementRemoveMarketplace(t.Context(), request); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("stale remove=%v", err)
	}
	current, err := s.ManagementMarketplace(t.Context(), "market", a)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedState = current.ExpectedState
	if err := s.ManagementRemoveMarketplace(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementMarketplace(t.Context(), "market", a); !errors.Is(err, plugincmd.ErrNotFound) {
		t.Fatalf("removed definition=%v", err)
	}
	after, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found || after != before {
		t.Fatalf("remove changed install: found=%v error=%v", found, err)
	}
	retained, err := p.Plugins().ListPluginRevisions(t.Context(), "demo")
	if err != nil || len(retained) != len(revisions) || retained[0] != revisions[0] {
		t.Fatalf("remove changed revisions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.stateDir, retained[0].RelativeRoot)); err != nil {
		t.Fatalf("archive removed: %v", err)
	}
}
