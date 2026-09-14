package pluginapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type recordingActivator struct {
	compileErr error
	publishErr error
	compiled   [][]runtimecatalogcmd.Source
	published  []runtimecatalogcmd.Snapshot
	base       []runtimecatalogcmd.Source
	started    chan struct{}
	release    chan struct{}
}

func (a *recordingActivator) PreparePluginCandidate(
	_ context.Context,
	sources []runtimecatalogcmd.Source,
) (runtimecatalogcmd.Snapshot, error) {
	a.compiled = append(a.compiled, append([]runtimecatalogcmd.Source(nil), sources...))
	if a.compileErr != nil {
		return runtimecatalogcmd.Snapshot{}, a.compileErr
	}
	complete := append([]runtimecatalogcmd.Source(nil), a.base...)
	complete = append(complete, sources...)
	return runtimecatalog.NewCompiler().CompileApplication(complete)
}

func (a *recordingActivator) PublishCandidate(_ context.Context, snapshot runtimecatalogcmd.Snapshot) error {
	if a.started != nil {
		close(a.started)
		<-a.release
		a.started = nil
	}
	if a.publishErr != nil {
		return a.publishErr
	}
	a.published = append(a.published, snapshot)
	return nil
}

func TestManagedLifecycleInstallUpgradeDisableAndRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, activator, store := newManagedTestService(t)
	plugin := writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)
	plugin.Version = "stale-marketplace-version"

	if err := service.managed.install(ctx, plugin); err != nil {
		t.Fatalf("install() error = %v", err)
	}
	first, found, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil || !found || !first.Enabled {
		t.Fatalf("first install = %#v, found = %t, err = %v", first, found, err)
	}
	if first.Version != "1.0.0" || first.Description != "Demo 1.0.0" {
		t.Fatalf("captured manifest metadata = version %q, description %q", first.Version, first.Description)
	}
	if len(activator.compiled) != 1 || len(activator.published) != 1 {
		t.Fatalf("activation calls = compile %d, publish %d", len(activator.compiled), len(activator.published))
	}
	if _, err := os.Stat(filepath.Join(stateDir, first.DataRelativePath)); err != nil {
		t.Fatalf("plugin data root: %v", err)
	}

	if err := service.Disable(ctx, testPluginName); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	plugin = writeManagedPlugin(t, marketplaceRoot, "2.0.0", true)
	if err := service.managed.install(ctx, plugin); err != nil {
		t.Fatalf("upgrade install() error = %v", err)
	}
	second, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	if second.Enabled || second.ActiveRevisionID == first.ActiveRevisionID || second.DataRelativePath != first.DataRelativePath {
		t.Fatalf("upgraded install = %#v, first = %#v", second, first)
	}
	firstRevision, _, err := store.GetPluginRevision(ctx, testPluginName, first.ActiveRevisionID)
	if err != nil || firstRevision.RetiredAt.IsZero() {
		t.Fatalf("previous revision retired at = %v, err = %v", firstRevision.RetiredAt, err)
	}
	if err := service.Rollback(ctx, testPluginName, first.ActiveRevisionID); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	rolledBack, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil || rolledBack.ActiveRevisionID != first.ActiveRevisionID || rolledBack.Enabled {
		t.Fatalf("rolled back install = %#v, err = %v", rolledBack, err)
	}
	firstRevision, _, err = store.GetPluginRevision(ctx, testPluginName, first.ActiveRevisionID)
	secondRevision, _, secondErr := store.GetPluginRevision(ctx, testPluginName, second.ActiveRevisionID)
	if err != nil || secondErr != nil || !firstRevision.RetiredAt.IsZero() || secondRevision.RetiredAt.IsZero() {
		t.Fatalf("rollback retirement state = first %#v, second %#v, errors %v/%v", firstRevision, secondRevision, err, secondErr)
	}

	activator.compileErr = errors.New("candidate rejected")
	plugin = writeManagedPlugin(t, marketplaceRoot, "3.0.0", false)
	if err := service.managed.install(ctx, plugin); err == nil {
		t.Fatal("install() compile error = nil")
	}
	afterFailure, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil || afterFailure.ActiveRevisionID != first.ActiveRevisionID {
		t.Fatalf("install after failed compile = %#v, err = %v", afterFailure, err)
	}
}

func TestManagedLifecycleRecoversDurableActivationAfterPublishFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, marketplaceRoot, service, activator, store := newManagedTestService(t)
	activator.publishErr = errors.New("publication interrupted")

	if err := service.managed.install(ctx, writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)); err == nil {
		t.Fatal("install() publish error = nil")
	}
	if _, found, err := store.GetPluginInstall(ctx, testPluginName); err != nil || !found {
		t.Fatalf("durable install found = %t, err = %v", found, err)
	}
	pending, err := store.ListIncompletePluginActivations(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending activations = %#v, err = %v", pending, err)
	}

	activator.publishErr = nil
	if err := service.Recover(ctx); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	pending, err = store.ListIncompletePluginActivations(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after recovery = %#v, err = %v", pending, err)
	}
}

func TestManagedLifecycleRetriesInterruptedOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, marketplaceRoot, service, activator, store := newManagedTestService(t)
	first := writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)
	if err := service.managed.install(ctx, first); err != nil {
		t.Fatal(err)
	}

	second := writeManagedPlugin(t, marketplaceRoot, "2.0.0", true)
	activator.publishErr = errors.New("upgrade publication interrupted")
	if err := service.managed.install(ctx, second); err == nil {
		t.Fatal("upgrade publish error = nil")
	}
	activator.publishErr = nil
	if err := service.managed.install(ctx, second); err != nil {
		t.Fatalf("upgrade retry error = %v", err)
	}
	assertNoPendingPluginActivations(t, store)

	activator.publishErr = errors.New("disable publication interrupted")
	if err := service.Disable(ctx, testPluginName); err == nil {
		t.Fatal("disable publish error = nil")
	}
	activator.publishErr = nil
	if err := service.Disable(ctx, testPluginName); err != nil {
		t.Fatalf("disable retry error = %v", err)
	}
	assertNoPendingPluginActivations(t, store)

	activator.publishErr = errors.New("remove publication interrupted")
	if err := service.RemoveInstalled(ctx, testPluginName); err == nil {
		t.Fatal("remove publish error = nil")
	}
	activator.publishErr = nil
	if err := service.RemoveInstalled(ctx, testPluginName); err != nil {
		t.Fatalf("remove retry error = %v", err)
	}
	assertNoPendingPluginActivations(t, store)
}

func TestManagedLifecycleRejectsEscapingManagedDirectoryBeforeMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, _, store := newManagedTestService(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(stateDir, "plugin-data")); err != nil {
		t.Fatal(err)
	}

	err := service.managed.install(ctx, writeManagedPlugin(t, marketplaceRoot, "1.0.0", false))
	if err == nil {
		t.Fatal("install() escaping managed directory error = nil")
	}
	if _, err := os.Stat(filepath.Join(outside, testPluginName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path was mutated: %v", err)
	}
	if _, found, err := store.GetPluginInstall(ctx, testPluginName); err != nil || found {
		t.Fatalf("install found = %t, err = %v", found, err)
	}
}

func TestManagedLifecycleDoesNotProtectRevisionThroughEscapingAncestor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, _, store := newManagedTestService(t)
	plugin := writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := loader.LoadPlugin(plugin.PluginPath)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideRevision := filepath.Join(outside, testPluginName, string(source.Descriptor.Revision))
	if err := copyDir(plugin.PluginPath, outsideRevision); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outsideRevision, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(outsideRevision, "plugin.json")
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateDir, "plugin-revisions")); err != nil {
		t.Fatal(err)
	}

	if err := service.managed.install(ctx, plugin); err == nil {
		t.Fatal("install() escaping revision ancestor error = nil")
	}
	rootInfo, rootErr := os.Stat(outsideRevision)
	manifestInfo, manifestErr := os.Stat(manifestPath)
	if rootErr != nil || manifestErr != nil {
		t.Fatalf("outside revision stat errors = %v, %v", rootErr, manifestErr)
	}
	if rootInfo.Mode().Perm() != 0o700 || manifestInfo.Mode().Perm() != 0o600 {
		t.Fatalf("outside modes changed to root %o, manifest %o", rootInfo.Mode().Perm(), manifestInfo.Mode().Perm())
	}
	if _, found, err := store.GetPluginInstall(ctx, testPluginName); err != nil || found {
		t.Fatalf("install found = %t, err = %v", found, err)
	}
}

func TestManagedLifecycleRepairsUnpublishedWritableRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, _, _ := newManagedTestService(t)
	plugin := writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := loader.LoadPlugin(plugin.PluginPath)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(stateDir, "plugin-revisions", testPluginName, string(source.Descriptor.Revision))
	if err := copyDir(plugin.PluginPath, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := service.managed.install(ctx, plugin); err != nil {
		t.Fatalf("install() error = %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("repaired revision root mode = %o", info.Mode().Perm())
	}
}

func TestManagedLifecycleReportsDriftAndSeparatesRemoveFromPurge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, _, store := newManagedTestService(t)
	if err := service.managed.install(ctx, writeManagedPlugin(t, marketplaceRoot, "1.0.0", true)); err != nil {
		t.Fatal(err)
	}
	install, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := store.GetPluginRevision(ctx, testPluginName, install.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := service.Capabilities(ctx, testPluginName+"@"+testMarketplaceName)
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if diff.Active.Skills != 1 || diff.Candidate.Skills != 1 || diff.Active.Commands != 1 {
		t.Fatalf("capability diff = %#v", diff)
	}
	if err := service.RemoveInstalled(ctx, testPluginName); err != nil {
		t.Fatalf("RemoveInstalled() error = %v", err)
	}
	if _, found, err := store.GetPluginInstall(ctx, testPluginName); err != nil || found {
		t.Fatalf("install after remove found = %t, err = %v", found, err)
	}
	revisionRoot := filepath.Join(stateDir, filepath.FromSlash(revision.RelativeRoot))
	if _, err := os.Stat(revisionRoot); err != nil {
		t.Fatalf("retained revision root: %v", err)
	}
	if err := service.Purge(ctx, testPluginName, revision.RevisionID, true); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if _, err := os.Stat(revisionRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged revision stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, install.DataRelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged data stat error = %v", err)
	}
	lastPublished := service.managed.activator.(*recordingActivator).published
	if _, present := lastPublished[len(lastPublished)-1].Sources[runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindBuiltin, Name: "host"}]; !present {
		t.Fatal("non-plugin source was dropped from published snapshot")
	}
}

func TestManagedLifecycleReportsImmutableRevisionDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, _, store := newManagedTestService(t)
	if err := service.managed.install(ctx, writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)); err != nil {
		t.Fatal(err)
	}
	install, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := store.GetPluginRevision(ctx, testPluginName, install.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	revisionRoot := filepath.Join(stateDir, filepath.FromSlash(revision.RelativeRoot))
	if err := os.Chmod(revisionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(revisionRoot, "unmanaged"), "change")
	drifted, err := service.Drifted(ctx, testPluginName)
	if err != nil || !drifted {
		t.Fatalf("Drifted() = %t, %v", drifted, err)
	}
	current, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil || current.ActiveRevisionID != install.ActiveRevisionID {
		t.Fatalf("drift changed durable activation: %#v, err = %v", current, err)
	}
}

func TestManagedLifecycleSerializesDurableSwitchAndPublication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, marketplaceRoot, service, activator, store := newManagedTestService(t)
	if err := service.managed.install(ctx, writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)); err != nil {
		t.Fatal(err)
	}
	activator.started = make(chan struct{})
	activator.release = make(chan struct{})
	upgrade := writeManagedPlugin(t, marketplaceRoot, "2.0.0", true)

	upgradeDone := make(chan error, 1)
	go func() {
		upgradeDone <- service.managed.install(ctx, upgrade)
	}()
	<-activator.started
	disableDone := make(chan error, 1)
	go func() { disableDone <- service.Disable(ctx, testPluginName) }()
	select {
	case err := <-disableDone:
		t.Fatalf("Disable() crossed publication boundary early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(activator.release)
	if err := <-upgradeDone; err != nil {
		t.Fatalf("upgrade error = %v", err)
	}
	if err := <-disableDone; err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	install, _, err := store.GetPluginInstall(ctx, testPluginName)
	if err != nil || install.Enabled {
		t.Fatalf("final install = %#v, err = %v", install, err)
	}
	last := activator.published[len(activator.published)-1]
	if _, present := last.Sources[runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: testPluginName}]; present {
		t.Fatal("last published snapshot retained disabled plugin")
	}
}

func TestManagedLifecycleUpgradeCannotReinstallAfterConcurrentRemove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, marketplaceRoot, service, activator, store := newManagedTestService(t)
	if err := service.managed.install(ctx, writeManagedPlugin(t, marketplaceRoot, "1.0.0", false)); err != nil {
		t.Fatal(err)
	}
	upgrade := writeManagedPlugin(t, marketplaceRoot, "2.0.0", true)
	activator.started = make(chan struct{})
	activator.release = make(chan struct{})
	removeDone := make(chan error, 1)
	go func() { removeDone <- service.RemoveInstalled(ctx, testPluginName) }()
	<-activator.started

	upgradeDone := make(chan error, 1)
	go func() { upgradeDone <- service.managed.upgrade(ctx, upgrade) }()
	select {
	case err := <-upgradeDone:
		t.Fatalf("upgrade crossed remove publication boundary early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(activator.release)
	if err := <-removeDone; err != nil {
		t.Fatalf("RemoveInstalled() error = %v", err)
	}
	if err := <-upgradeDone; err == nil || err.Error() != "plugin not installed" {
		t.Fatalf("upgrade after remove error = %v, want plugin not installed", err)
	}
	if _, found, err := store.GetPluginInstall(ctx, testPluginName); err != nil || found {
		t.Fatalf("removed plugin reinstalled: found = %t, err = %v", found, err)
	}
}

func newManagedTestService(
	t *testing.T,
) (string, string, *Service, *recordingActivator, state.PluginStore) {
	t.Helper()
	ctx := context.Background()
	stateDir := t.TempDir()
	t.Cleanup(func() { _ = makeTreeWritable(stateDir) })
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	activator := &recordingActivator{base: []runtimecatalogcmd.Source{{
		Descriptor: runtimecatalogcmd.SourceDescriptor{
			ID:       runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindBuiltin, Name: "host"},
			Revision: "builtin-v1",
		},
	}}}
	service, err := NewManaged(stateDir, provider.AppKV(), provider.Plugins(), activator)
	if err != nil {
		t.Fatal(err)
	}
	marketplaceRoot := filepath.Join(t.TempDir(), "marketplace")
	mustMkdirAll(t, filepath.Join(marketplaceRoot, "plugins", testPluginName))
	mustMkdirAll(t, filepath.Join(marketplaceRoot, ".agents", "plugins"))
	mustWriteFile(t, filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), `{
  "name":"demo-market",
  "plugins":[{"name":"demo","path":"./plugins/demo"}]
}`)
	if err := service.AddMarketplace(ctx, MarketplaceSource{Name: testMarketplaceName, Source: marketplaceRoot}); err != nil {
		t.Fatal(err)
	}
	return stateDir, marketplaceRoot, service, activator, provider.Plugins()
}

func writeManagedPlugin(t *testing.T, marketplaceRoot, version string, withSkill bool) AvailablePlugin {
	t.Helper()
	pluginRoot := filepath.Join(marketplaceRoot, "plugins", testPluginName)
	if err := os.RemoveAll(pluginRoot); err != nil {
		t.Fatal(err)
	}
	mustMkdirAll(t, pluginRoot)
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"` + version + `","description":"Demo ` + version + `"}`
	if withSkill {
		manifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"` + version + `","description":"Demo ` + version + `","extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"ship","description":"Ship safely","skill":"ship"}]}}}`
		mustMkdirAll(t, filepath.Join(pluginRoot, "skills", "ship"))
		mustWriteFile(t, filepath.Join(pluginRoot, "skills", "ship", "SKILL.md"), "---\nname: ship\ndescription: Ship safely.\n---\n")
	}
	mustWriteFile(t, filepath.Join(pluginRoot, "plugin.json"), manifest)
	return AvailablePlugin{
		Name: testPluginName, Version: version, Marketplace: testMarketplaceName,
		SourceRoot: marketplaceRoot, PluginPath: pluginRoot,
	}
}

func assertNoPendingPluginActivations(t *testing.T, store state.PluginStore) {
	t.Helper()
	pending, err := store.ListIncompletePluginActivations(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending activations = %#v, err = %v", pending, err)
	}
}

func TestLegacyMigrationRequiresExplicitOriginAndIsNotReactivatedAfterRemove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, marketplaceRoot, service, _, store := newManagedTestService(t)
	legacyRoot := filepath.Join(stateDir, "plugins", testPluginName)
	mustMkdirAll(t, filepath.Join(legacyRoot, "skills", "ship"))
	mustWriteFile(t, filepath.Join(legacyRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"legacy"}`)

	if err := service.MigrateLegacy(ctx); err != nil {
		t.Fatalf("MigrateLegacy() error = %v", err)
	}
	installed, found, err := service.Inspect(ctx, testPluginName)
	if err != nil || !found || installed.Marketplace != originUnknown {
		t.Fatalf("Inspect() = (%#v, %v, %v)", installed, found, err)
	}
	writeManagedPlugin(t, marketplaceRoot, "2.0.0", true)
	if err := service.Upgrade(ctx, testPluginName+"@"+testMarketplaceName); err == nil {
		t.Fatal("Upgrade() error = nil before explicit origin adoption")
	}
	if err := service.AdoptOrigin(ctx, testPluginName+"@"+testMarketplaceName); err != nil {
		record, _, _ := store.GetPluginInstall(ctx, testPluginName)
		t.Fatalf("AdoptOrigin() error = %v; origin = (%q, %q, %q)", err, record.OriginMarketplace, record.OriginSource, record.OriginPath)
	}
	if err := service.Upgrade(ctx, testPluginName+"@"+testMarketplaceName); err != nil {
		t.Fatalf("Upgrade() after adoption error = %v", err)
	}
	if err := service.RemoveInstalled(ctx, testPluginName); err != nil {
		t.Fatalf("RemoveInstalled() error = %v", err)
	}
	lateRoot := filepath.Join(stateDir, "plugins", "late-plugin")
	mustMkdirAll(t, lateRoot)
	mustWriteFile(t, filepath.Join(lateRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"late-plugin","version":"1.0.0"}`)
	if err := service.MigrateLegacy(ctx); err != nil {
		t.Fatalf("MigrateLegacy() after remove error = %v", err)
	}
	if _, found, err := service.GetInstalled(ctx, testPluginName); err != nil || found {
		t.Fatalf("GetInstalled() after restart-style migration = (_, %v, %v), want absent", found, err)
	}
	if _, found, err := service.GetInstalled(ctx, "late-plugin"); err != nil || found {
		t.Fatalf("GetInstalled(late-plugin) = (_, %v, %v), want absent after completed migration", found, err)
	}
}

func TestLegacyMigrationRejectsSymlinkedRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, _, service, _, _ := newManagedTestService(t) //nolint:dogsled // helper returns unrelated fixtures
	outsideRoot := filepath.Join(t.TempDir(), "plugins")
	pluginRoot := filepath.Join(outsideRoot, "outside")
	mustMkdirAll(t, pluginRoot)
	mustWriteFile(t, filepath.Join(pluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"outside","version":"1.0.0"}`)
	if err := os.Symlink(outsideRoot, filepath.Join(stateDir, "plugins")); err != nil {
		t.Fatal(err)
	}

	if err := service.MigrateLegacy(ctx); err == nil {
		t.Fatal("MigrateLegacy() error = nil for symlinked legacy root")
	}
	if _, found, err := service.GetInstalled(ctx, "outside"); err != nil || found {
		t.Fatalf("GetInstalled(outside) = (_, %v, %v), want absent", found, err)
	}
}

func TestLegacyMigrationCaptureRemainsAnchoredAfterRootSwap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir, _, service, _, _ := newManagedTestService(t) //nolint:dogsled // helper returns unrelated fixtures
	legacyPluginRoot := filepath.Join(stateDir, "plugins", testPluginName)
	mustMkdirAll(t, legacyPluginRoot)
	mustWriteFile(t, filepath.Join(legacyPluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"legacy"}`)

	stateRoot, err := os.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateRoot.Close() }()
	legacyRoot, err := stateRoot.OpenRoot("plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacyRoot.Close() }()

	outsideRoot := filepath.Join(t.TempDir(), "plugins")
	outsidePluginRoot := filepath.Join(outsideRoot, "outside")
	mustMkdirAll(t, outsidePluginRoot)
	mustWriteFile(t, filepath.Join(outsidePluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"outside","version":"1.0.0"}`)
	if err := os.Rename(filepath.Join(stateDir, "plugins"), filepath.Join(stateDir, "plugins-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoot, filepath.Join(stateDir, "plugins")); err != nil {
		t.Fatal(err)
	}

	if err := service.managed.migrateLegacyRoot(ctx, legacyRoot); err != nil {
		t.Fatalf("migrateLegacyRoot() error = %v", err)
	}
	if _, found, err := service.GetInstalled(ctx, testPluginName); err != nil || !found {
		t.Fatalf("GetInstalled(demo) = (_, %v, %v), want anchored legacy package", found, err)
	}
	if _, found, err := service.GetInstalled(ctx, "outside"); err != nil || found {
		t.Fatalf("GetInstalled(outside) = (_, %v, %v), want absent", found, err)
	}
}
