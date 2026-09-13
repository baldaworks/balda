package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	firstPluginRevision  = "rev-1"
	secondPluginRevision = "rev-2"
)

func TestSQLitePluginStoreActivationSurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store := provider.Plugins()
	now := time.Date(2026, 9, 13, 5, 0, 0, 123, time.UTC)
	revision := PluginRevisionRecord{PluginID: "demo", RevisionID: "rev-1", Version: "1.0.0", RelativeRoot: "plugin-revisions/demo/rev-1", CapabilityJSON: `{"skills":1}`, CreatedAt: now}
	if err := store.PutPluginRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	intent := PluginActivationIntent{IntentID: "intent-1", PluginID: "demo", ToRevisionID: "rev-1", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	install := PluginInstallRecord{PluginID: "demo", OriginMarketplace: "market", OriginSource: "https://example.com/repo.git", OriginPath: "plugins/demo", ActiveRevisionID: "rev-1", Enabled: true, Version: "1.0.0", CapabilityJSON: `{"skills":1}`, DataRelativePath: "plugin-data/demo", UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, intent, install); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}

	provider, err = NewSQLiteProvider(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	stored, found, err := provider.Plugins().GetPluginInstall(ctx, "demo")
	if err != nil || !found {
		t.Fatalf("GetPluginInstall() = (%#v, %t, %v)", stored, found, err)
	}
	if stored != install {
		t.Fatalf("install = %#v, want %#v", stored, install)
	}
	incomplete, err := provider.Plugins().ListIncompletePluginActivations(ctx)
	if err != nil || len(incomplete) != 1 || incomplete[0].IntentID != "intent-1" {
		t.Fatalf("incomplete = %#v, err = %v", incomplete, err)
	}
	if err := provider.Plugins().CompletePluginActivation(ctx, "intent-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	incomplete, err = provider.Plugins().ListIncompletePluginActivations(ctx)
	if err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete after complete = %#v, err = %v", incomplete, err)
	}
}

func TestSQLitePluginStoreActivationIsAtomicAndOriginLocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	store := provider.Plugins()
	now := time.Now().UTC()
	base := PluginInstallRecord{PluginID: "demo", OriginMarketplace: "market", OriginSource: "source", OriginPath: "plugins/demo", ActiveRevisionID: "missing", Enabled: true, CapabilityJSON: `{}`, DataRelativePath: "plugin-data/demo", UpdatedAt: now}
	intent := PluginActivationIntent{IntentID: "missing", PluginID: "demo", ToRevisionID: "missing", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, intent, base); err == nil {
		t.Fatal("ActivatePlugin() missing revision error = nil")
	}
	if pending, err := store.ListIncompletePluginActivations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v, err = %v", pending, err)
	}

	for _, revisionID := range []string{firstPluginRevision, secondPluginRevision} {
		if err := store.PutPluginRevision(ctx, PluginRevisionRecord{PluginID: "demo", RevisionID: revisionID, RelativeRoot: "plugin-revisions/demo/" + revisionID, CapabilityJSON: `{}`, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	base.ActiveRevisionID = firstPluginRevision
	intent.IntentID = "install"
	intent.ToRevisionID = firstPluginRevision
	if err := store.ActivatePlugin(ctx, intent, base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.OriginSource = "other"
	changed.ActiveRevisionID = secondPluginRevision
	upgrade := PluginActivationIntent{IntentID: "upgrade", PluginID: "demo", FromRevisionID: firstPluginRevision, ToRevisionID: secondPluginRevision, Operation: "upgrade", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, upgrade, changed); err == nil {
		t.Fatal("ActivatePlugin() origin change error = nil")
	}
	changed = base
	changed.ActiveRevisionID = "rev-2"
	changed.DataRelativePath = "other-data/demo"
	upgrade.IntentID = "move-data"
	if err := store.ActivatePlugin(ctx, upgrade, changed); err == nil {
		t.Fatal("ActivatePlugin() data path change error = nil")
	}
	stored, _, err := store.GetPluginInstall(ctx, "demo")
	if err != nil || stored.ActiveRevisionID != firstPluginRevision {
		t.Fatalf("stored = %#v, err = %v", stored, err)
	}
	validInstall := base
	validInstall.ActiveRevisionID = secondPluginRevision
	validUpgrade := upgrade
	validUpgrade.IntentID = "upgrade-ok"
	var wait sync.WaitGroup
	wait.Add(2)
	var activationErr, purgeErr error
	go func() { defer wait.Done(); activationErr = store.ActivatePlugin(ctx, validUpgrade, validInstall) }()
	go func() { defer wait.Done(); purgeErr = store.PurgePluginRevision(ctx, "demo", firstPluginRevision) }()
	wait.Wait()
	if activationErr != nil || purgeErr == nil {
		t.Fatalf("concurrent activation error = %v, purge error = %v", activationErr, purgeErr)
	}
	if _, found, err := store.GetPluginRevision(ctx, "demo", firstPluginRevision); err != nil || !found {
		t.Fatalf("referenced old revision found = %t, err = %v", found, err)
	}
}

func TestSQLitePluginStoreRetainsAndSafelyPurgesRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	store := provider.Plugins()
	now := time.Now().UTC()
	for _, id := range []string{"active", "old"} {
		if err := store.PutPluginRevision(ctx, PluginRevisionRecord{PluginID: "demo", RevisionID: id, RelativeRoot: "revisions/" + id, CapabilityJSON: `{}`, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	install := PluginInstallRecord{PluginID: "demo", OriginSource: "source", OriginPath: "demo", ActiveRevisionID: "active", Enabled: true, CapabilityJSON: `{}`, DataRelativePath: "data/demo", UpdatedAt: now}
	intent := PluginActivationIntent{IntentID: "install", PluginID: "demo", ToRevisionID: "active", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, intent, install); err != nil {
		t.Fatal(err)
	}
	if can, err := store.CanPurgePluginRevision(ctx, "demo", "active"); err != nil || can {
		t.Fatalf("active can purge = %t, err = %v", can, err)
	}
	if err := store.RetirePluginRevision(ctx, "demo", "old", now); err != nil {
		t.Fatal(err)
	}
	if can, err := store.CanPurgePluginRevision(ctx, "demo", "old"); err != nil || !can {
		t.Fatalf("old can purge = %t, err = %v", can, err)
	}
	if err := store.PurgePluginRevision(ctx, "demo", "old"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetPluginRevision(ctx, "demo", "old"); err != nil || found {
		t.Fatalf("old found = %t, err = %v", found, err)
	}
}

func TestSQLitePluginStoreRejectsConflictingRevisionAndTraversal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Now().UTC()
	record := PluginRevisionRecord{PluginID: "demo", RevisionID: "rev", RelativeRoot: "revisions/demo/rev", CapabilityJSON: `{}`, CreatedAt: now}
	if err := provider.Plugins().PutPluginRevision(ctx, record); err != nil {
		t.Fatal(err)
	}
	conflict := record
	conflict.CapabilityJSON = `{"skills":1}`
	if err := provider.Plugins().PutPluginRevision(ctx, conflict); err == nil {
		t.Fatal("conflicting immutable revision error = nil")
	}
	record.RevisionID = "escape"
	record.RelativeRoot = "../outside"
	if err := provider.Plugins().PutPluginRevision(ctx, record); err == nil {
		t.Fatal("traversing revision root error = nil")
	}
}

func TestPluginCatalogMigrationRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := up00034PluginCatalogState(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"balda_plugin_revisions", "balda_plugin_installs", "balda_plugin_activation_intents"} {
		exists, err := sqliteTableExists(ctx, db, table)
		if err != nil || !exists {
			t.Fatalf("table %q exists = %t, err = %v", table, exists, err)
		}
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := down00034PluginCatalogState(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"balda_plugin_revisions", "balda_plugin_installs", "balda_plugin_activation_intents"} {
		exists, err := sqliteTableExists(ctx, db, table)
		if err != nil || exists {
			t.Fatalf("table %q exists after down = %t, err = %v", table, exists, err)
		}
	}
}
