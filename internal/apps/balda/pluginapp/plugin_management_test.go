package pluginapp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

const managementUpgradeVersion = "2.0.0"

func TestGuardedLifecycleCandidateAndEnabledABA(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a}); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.ManagementCandidate(t.Context(), "demo", "market", a)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Install, PluginName: "demo", MarketplaceName: "market", CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState, Authority: a})
	if err != nil || !result.Accepted {
		t.Fatalf("install: %+v %v", result, err)
	}
	installed, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	before, err := s.ManagementExpectedState(t.Context(), "demo", a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	_, err = s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: "demo", ExpectedState: before, Confirm: true, Authority: a})
	if !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("enabled ABA accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.stateDir, filepath.FromSlash("plugin-revisions/demo/"+installed.ActiveRevisionID))); err != nil {
		t.Fatal(err)
	}
}

func TestGuardedLifecycleRejectsReviewedContentDrift(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a}); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.ManagementCandidate(t.Context(), "demo", "market", a)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "plugins", "demo", "new-content.txt"), "same version changed content")
	_, err = s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Install, PluginName: "demo", MarketplaceName: "market", CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState, Authority: a})
	if !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("unreviewed content accepted: %v", err)
	}
	state, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if state.Install != nil || len(state.Revisions) != 0 || len(state.Pending) != 0 || state.Generation != 0 {
		t.Fatalf("rejected content wrote state: %+v", state)
	}
}

func TestGuardedLifecycleGitOriginProof(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	runGit(t, root, "init", "-b", "fixture")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "first")
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: "market", Source: "file://" + root, Ref: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(t.Context(), "demo@market"); err != nil {
		t.Fatal(err)
	}
	observed, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || observed.OriginIdentity == "" {
		t.Fatalf("missing atomic proof: %v", err)
	}
	writeManagedPlugin(t, root, managementUpgradeVersion, false)
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "second")
	if err := s.Upgrade(t.Context(), "demo@market"); err != nil {
		t.Fatalf("same source upgrade: %v", err)
	}
	unchanged, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || unchanged.OriginIdentity != observed.OriginIdentity {
		t.Fatal("upgrade rewrote origin proof")
	}
	replacement := t.TempDir()
	writeManagedPlugin(t, replacement, "3.0.0", false)
	mustMkdirAll(t, filepath.Join(replacement, ".agents", "plugins"))
	mustWriteFile(t, filepath.Join(replacement, marketplaceManifestPath), `{"name":"index-identity","plugins":[{"name":"demo","path":"plugins/demo"}]}`)
	runGit(t, replacement, "init", "-b", "fixture")
	runGit(t, replacement, "config", "user.name", "Test")
	runGit(t, replacement, "config", "user.email", "test@example.com")
	runGit(t, replacement, "add", ".")
	runGit(t, replacement, "commit", "-m", "replacement")
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: "market", Source: "file://" + replacement, Ref: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upgrade(t.Context(), "demo@market"); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("replaced remote accepted: %v", err)
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || after.Token() != unchanged.Token() {
		t.Fatal("rejected origin changed state")
	}
	if err := s.kv.Delete(t.Context(), "plugin_origin_identity:demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementCandidate(t.Context(), "demo", "", a); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("unprovable Git origin accepted: %v", err)
	}
	if err := s.kv.SetJSON(t.Context(), "plugin_origin_identity:demo", unchanged.OriginIdentity); err != nil {
		t.Fatal(err)
	}
	token, err := s.ManagementExpectedState(t.Context(), "demo", a)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Remove, PluginName: "demo", ExpectedState: token, Confirm: true, Authority: a})
	if err != nil || !outcome.Accepted {
		t.Fatalf("remove: %+v %v", outcome, err)
	}
	removed, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || removed.OriginIdentity != "" || removed.Generation <= after.Generation {
		t.Fatal("remove retained origin proof or generation")
	}
	if err := s.Install(t.Context(), "demo@market"); err != nil {
		t.Fatal(err)
	}
	reinstalled, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || reinstalled.OriginIdentity == observed.OriginIdentity || reinstalled.OriginIdentity == "" {
		t.Fatal("reinstall did not accept fresh proof")
	}
}

func TestGuardedLifecycleChatAndBrowserShareOwner(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: "market", Source: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(t.Context(), "demo@market"); err != nil {
		t.Fatal(err)
	}
	expected, err := s.ManagementExpectedState(t.Context(), "demo", a)
	if err != nil {
		t.Fatal(err)
	}
	recorder := s.managed.activator.(*recordingActivator)
	recorder.started = make(chan struct{})
	recorder.release = make(chan struct{})
	chat := make(chan error, 1)
	go func() { chat <- s.Disable(t.Context(), "demo") }()
	<-recorder.started
	browser := make(chan error, 1)
	go func() {
		_, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Enable, PluginName: "demo", ExpectedState: expected, Authority: a})
		browser <- err
	}()
	close(recorder.release)
	if err := <-chat; err != nil {
		t.Fatal(err)
	}
	if err := <-browser; !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("browser accepted stale chat state: %v", err)
	}
	installed, found, err := s.managed.store.GetPluginInstall(t.Context(), "demo")
	if err != nil || !found || installed.Enabled {
		t.Fatal("browser overwrote chat disable")
	}
}
