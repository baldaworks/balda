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
	if version != 2 {
		t.Fatalf("PostgreSQL migration version = %d, want 2", version)
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
	if indexes != 27 {
		t.Fatalf("PostgreSQL explicit indexes = %d, want 27", indexes)
	}
}

func TestPostgresUnifiedUserSchemaConstraints(t *testing.T) {
	db := newPostgresTestDB(t)
	if err := migratePostgres(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	insertPostgresUser(t, db, "admin-1", "admin.one", true)
	insertPostgresUser(t, db, "operator-1", "operator.one", false)

	assertPostgresRejected(t, db, `INSERT INTO balda_users
		(user_id, display_name, username, normalized_username, status, role, password_hash,
		 credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES ('bad-role', 'Bad', 'bad', 'bad', 'active', 'owner', 'hash',
		        'active', 0, 0, 1, 1, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
	assertPostgresRejected(t, db, `INSERT INTO balda_users
		(user_id, display_name, username, normalized_username, status, role, password_hash,
		 credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES ('bad-temporary', 'Bad', 'bad-temporary', 'bad-temporary', 'active', 'operator', 'hash',
		        'temporary', 0, 0, 1, 1, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
	assertPostgresRejected(t, db, `UPDATE balda_users SET normalized_username = 'admin.one' WHERE user_id = 'operator-1'`)
	assertPostgresRejected(t, db, `UPDATE balda_users SET normalized_username = ' Mixed ' WHERE user_id = 'operator-1'`)
	assertPostgresRejected(t, db, `UPDATE balda_users SET is_primary = 1 WHERE user_id = 'operator-1'`)

	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_user_bindings
		(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES ('binding-1', 'admin-1', 'telegram', '101', 'Admin', 'migration',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	assertPostgresRejected(t, db, `INSERT INTO balda_user_bindings
		(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES ('binding-2', 'admin-1', 'slack', 'U101', '', '',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
	assertPostgresRejected(t, db, `INSERT INTO balda_user_bindings
		(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES ('binding-3', 'operator-1', 'telegram', '101', '', '',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)

	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_backoffice_sessions
		(session_id, user_id, assurance, credential_version, access_selector,
		 access_verifier_digest, csrf_verifier_digest, created_at, last_seen_at,
		 access_expires_at, refresh_expires_at, revoked_at, revocation_reason, version)
		VALUES ('session-1', 'admin-1', 'normal', 1, 'access-1', decode('01', 'hex'), decode('02', 'hex'),
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z',
		        '2026-09-23T00:15:00Z', '2026-09-24T00:00:00Z', '', '', 1)`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-1', decode('03', 'hex'), 1, 'active',
		        '2026-09-23T00:00:00Z', '', '2026-09-24T00:00:00Z')`); err != nil {
		t.Fatalf("insert refresh: %v", err)
	}
	assertPostgresRejected(t, db, `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-2', decode('04', 'hex'), 2, 'active',
		        '2026-09-23T00:05:00Z', '', '2026-09-24T00:00:00Z')`)
	if _, err := db.ExecContext(t.Context(), `UPDATE balda_backoffice_refresh_tokens
		SET state = 'used', used_at = '2026-09-23T00:10:00Z'
		WHERE selector = 'refresh-1'`); err != nil {
		t.Fatalf("consume active refresh: %v", err)
	}
	assertPostgresRejected(t, db, `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-short', decode('05', 'hex'), 2, 'active',
		        '2026-09-23T00:05:00Z', '', '2026-09-23T12:00:00Z')`)
	assertPostgresRejected(t, db, `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-used', decode('06', 'hex'), 2, 'used',
		        '2026-09-23T00:05:00Z', '2026-09-23T00:06:00Z', '2026-09-24T00:00:00Z')`)
	assertPostgresRejected(t, db, `UPDATE balda_backoffice_refresh_tokens
		SET state = 'active', used_at = '' WHERE selector = 'refresh-1'`)
	assertPostgresRejected(t, db, `UPDATE balda_backoffice_sessions
		SET refresh_expires_at = '2026-09-25T00:00:00Z' WHERE session_id = 'session-1'`)

	if _, err := db.ExecContext(t.Context(), `DELETE FROM balda_users WHERE user_id = 'admin-1'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	for _, table := range []string{"balda_user_bindings", "balda_backoffice_sessions", "balda_backoffice_refresh_tokens"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s rows after user deletion = %d, want 0", table, count)
		}
	}
}

func insertPostgresUser(t *testing.T, db *sql.DB, id, normalizedUsername string, primary bool) {
	t.Helper()
	primaryValue := 0
	role := "operator"
	if primary {
		primaryValue = 1
		role = "administrator"
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_users
		(user_id, display_name, username, normalized_username, status, role, password_hash,
		 credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'active', $5, 'hash', 'active', 0, $6, 1, 1,
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`,
		id, id, normalizedUsername, normalizedUsername, role, primaryValue,
	); err != nil {
		t.Fatalf("insert user %q: %v", id, err)
	}
}

func assertPostgresRejected(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), statement); err == nil {
		t.Fatalf("statement unexpectedly succeeded: %s", statement)
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
