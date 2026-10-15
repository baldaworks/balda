//go:build integration && sqlite

package state

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSQLiteWebhookSlugMigrationPreservesData(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	registerBaldaGoMigrations()
	migrations, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	checkWebhookSlugMigration(t, db, provider, 60, func(q string) string { return q }, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_balda_webhook_routes_active_path'`)
}
