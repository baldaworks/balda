package pluginapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestManagementCandidateUsesConfiguredMarketplace(t *testing.T) {
	for _, marketplace := range []string{"A", "B"} {
		t.Run(marketplace, func(t *testing.T) {
			s, p, a, rootA := newMarketplaceManagementTestService(t)
			t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
			rootB := t.TempDir()
			writeManagedPlugin(t, rootB, managementUpgradeVersion, false)
			writeSelectionIndex(t, rootA, "manifest-a")
			writeSelectionIndex(t, rootB, "A")
			for name, root := range map[string]string{"A": rootA, "B": rootB} {
				added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: name, Source: root, Authority: a})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: name, ExpectedState: added.ExpectedState, Authority: a}); err != nil {
					t.Fatal(err)
				}
			}
			candidate, err := s.ManagementCandidate(t.Context(), testPluginName, marketplace, a)
			if err != nil || candidate.Marketplace != marketplace {
				t.Fatalf("configured %s review=%+v err=%v", marketplace, candidate, err)
			}
			outcome, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Install, PluginName: testPluginName, MarketplaceName: marketplace, CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState, Authority: a})
			if err != nil || !outcome.Accepted {
				t.Fatalf("configured %s install=%+v err=%v", marketplace, outcome, err)
			}
			observed, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
			wantRoot := rootA
			if marketplace == "B" {
				wantRoot = rootB
			}
			wantRoot, pathErr := filepath.EvalSymlinks(wantRoot)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if err != nil || observed.Install == nil || observed.Install.OriginMarketplace != marketplace || observed.Install.OriginSource != wantRoot || observed.OriginIdentity == "" {
				t.Fatalf("configured origin=%+v proof=%q err=%v", observed.Install, observed.OriginIdentity, err)
			}
		})
	}
}

func TestGuardedLifecycleTrustedManifestSelector(t *testing.T) {
	s, p, _, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	writeSelectionIndex(t, root, "manifest-selector")
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: "configured", Source: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(t.Context(), testPluginName+"@manifest-selector"); err != nil {
		t.Fatalf("trusted manifest selector: %v", err)
	}
	observed, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	wantRoot, pathErr := filepath.EvalSymlinks(root)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err != nil || observed.Install == nil || observed.Install.OriginMarketplace != "configured" || observed.Install.OriginSource != wantRoot {
		t.Fatalf("trusted selected origin=%+v err=%v", observed.Install, err)
	}
}

func TestGuardedLifecycleQualifiedHealthyMarketplace(t *testing.T) {
	for _, failedSource := range []string{"malformed", "missing"} {
		t.Run(failedSource, func(t *testing.T) {
			s, p, _, healthyRoot := newMarketplaceManagementTestService(t)
			t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
			failedRoot := t.TempDir()
			if failedSource == "malformed" {
				mustMkdirAll(t, filepath.Dir(filepath.Join(failedRoot, marketplaceManifestPath)))
				mustWriteFile(t, filepath.Join(failedRoot, marketplaceManifestPath), "{")
			} else if err := os.Remove(failedRoot); err != nil {
				t.Fatal(err)
			}
			for name, root := range map[string]string{"healthy": healthyRoot, "failed": failedRoot} {
				if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: name, Source: root}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Install(t.Context(), testPluginName+"@healthy"); err != nil {
				t.Fatalf("unrelated %s source blocked healthy install: %v", failedSource, err)
			}
			before, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
			if err != nil || before.Install == nil || before.OriginIdentity == "" {
				t.Fatalf("initial proof=%+v err=%v", before, err)
			}
			writeManagedPlugin(t, healthyRoot, managementUpgradeVersion, false)
			if err := s.Upgrade(t.Context(), testPluginName); err != nil {
				t.Fatalf("unrelated %s source blocked proven upgrade: %v", failedSource, err)
			}
			upgraded, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
			if err != nil || upgraded.Install == nil || upgraded.Install.Version != managementUpgradeVersion || upgraded.OriginIdentity != before.OriginIdentity {
				t.Fatalf("upgrade changed origin or missed version: %+v err=%v", upgraded, err)
			}
			failed, err := p.PluginManagement().ObserveMarketplace(t.Context(), "failed")
			if err != nil {
				t.Fatal(err)
			}
			var metadata discoverySnapshot
			if err := json.Unmarshal(failed.MetadataJSON, &metadata); err != nil || metadata.LastAttemptStatus != discoveryFailed || metadata.LastAttemptAt.IsZero() {
				t.Fatalf("failed observation not accepted: %+v err=%v", metadata, err)
			}
			audit, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
			if err != nil {
				t.Fatal(err)
			}
			failures := 0
			for _, event := range audit.Events {
				if event.Action == usercmd.AuditActionPluginMarketplaceRefreshFailed && event.TargetID == "failed" {
					failures++
				}
			}
			if failures != 2 {
				t.Fatalf("failed discovery attempts missing canonical audit: %d", failures)
			}
			activator := s.managed.activator.(*recordingActivator)
			published := len(activator.published)
			mustWriteFile(t, filepath.Join(healthyRoot, marketplaceManifestPath), "{")
			if err := s.Upgrade(t.Context(), testPluginName+"@healthy"); err == nil {
				t.Fatal("failed selected source used stale discovery")
			}
			after, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
			if err != nil || after.Token() != upgraded.Token() || len(activator.published) != published {
				t.Fatalf("failed selected source changed plugin/catalog: %+v err=%v", after, err)
			}
		})
	}
}

func writeSelectionIndex(t *testing.T, root, name string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(filepath.Join(root, marketplaceManifestPath)))
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"name":"`+name+`","plugins":[{"name":"demo","path":"plugins/demo"}]}`)
}

func TestGuardedLifecycleSelectedMarketplaceFailure(t *testing.T) {
	s, p, _, selectedRoot := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	otherRoot := t.TempDir()
	writeManagedPlugin(t, otherRoot, managementUpgradeVersion, false)
	writeSelectionIndex(t, otherRoot, "selected")
	mustWriteFile(t, filepath.Join(selectedRoot, marketplaceManifestPath), "{")
	for name, root := range map[string]string{"selected": selectedRoot, "other": otherRoot} {
		if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: name, Source: root}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Install(t.Context(), testPluginName+"@selected"); err == nil {
		t.Fatal("failed configured source fell through to another manifest identity")
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil || after.Token() != before.Token() || len(s.managed.activator.(*recordingActivator).published) != 0 {
		t.Fatalf("failed selected install changed plugin/catalog: %+v err=%v", after, err)
	}
}
