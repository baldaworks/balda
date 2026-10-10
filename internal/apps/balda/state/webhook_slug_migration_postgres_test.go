//go:build integration && postgres

package state

import (
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestPostgresWebhookSlugMigrationPreservesData(t *testing.T) {
	db := newPostgresTestDB(t)
	migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	checkWebhookSlugMigration(t, db, provider, 25, postgresBind, `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_balda_webhook_routes_active_path'`)
}
