//go:build integration && postgres

package state

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var postgresTestSchemaSequence atomic.Uint64

func newPostgresTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := stdlib.OpenDB(*newPostgresTestConfig(t))
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newPostgresTestConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	dsn := os.Getenv("BALDA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("BALDA_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("balda_test_%d_%d", time.Now().UnixNano(), postgresTestSchemaSequence.Add(1))
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(redactPostgresError(err))
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(redactPostgresError(err))
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	return cfg
}

func TestPostgresMigrations(t *testing.T) {
	db := newPostgresTestDB(t)
	// Register SQLite's Go migrations first to exercise registry isolation.
	registerBaldaGoMigrations()
	for range 2 {
		if err := migratePostgres(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}
	var version int
	if err := db.QueryRowContext(t.Context(), "SELECT MAX(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("PostgreSQL migration version = %d, want 1", version)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_plugin_installs
		(plugin_id, origin_marketplace, origin_source, origin_path, active_revision_id, enabled, capability_json, data_relative_path, updated_at)
		VALUES ('missing', '', '', '', 'missing', 1, '{}', '', '')`); err == nil {
		t.Fatal("missing plugin revision did not violate foreign key")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_plugin_activation_intents
		(intent_id, plugin_id, to_revision_id, operation, state, created_at, updated_at)
		VALUES ('invalid', '', '', '', 'invalid', '', '')`); err == nil {
		t.Fatal("invalid intent state did not violate check constraint")
	}
	var indexes int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'idx_%'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 19 {
		t.Fatalf("PostgreSQL explicit indexes = %d, want 19", indexes)
	}
}

func TestPostgresMigrationFailureIsRedacted(t *testing.T) {
	db := newPostgresTestDB(t)
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE balda_app_kv (sentinel TEXT)"); err != nil {
		t.Fatal(err)
	}
	err := migratePostgres(t.Context(), db)
	if err == nil {
		t.Fatal("migration succeeded over conflicting schema")
	}
	if !strings.Contains(err.Error(), "postgres") || strings.Contains(err.Error(), os.Getenv("BALDA_TEST_POSTGRES_DSN")) {
		t.Fatalf("migration error missing context or exposing credentials: %v", err)
	}
	var exists bool
	if err := db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'balda_collaborators')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed migration did not roll back")
	}
}
