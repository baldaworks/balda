//go:build integration && sqlite

package state

import (
	"context"
	"database/sql"
	"path/filepath"

	"testing"
)

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
	if err := up00035PluginManifestMetadata(ctx, tx); err != nil {
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
	if err := down00035PluginManifestMetadata(ctx, tx); err != nil {
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
