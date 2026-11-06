package state

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSQLiteWebAuthAuditActorMigrationPreservesValues(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteConnectionString(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registerBaldaGoMigrations()
	migrations, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	checkWebAuthAuditActorMigration(t, db, provider, 72, 73, func(query string) string { return query })
}

func checkWebAuthAuditActorMigration(t *testing.T, db *sql.DB, provider *goose.Provider, previous, version int64, bind func(string) string) {
	t.Helper()
	if _, err := provider.UpTo(t.Context(), previous); err != nil {
		t.Fatal(err)
	}
	const actor = "web-auth-actor-sentinel"
	if _, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_security_audit_events
		(event_id,actor_session_id,action,target_type,target_id,outcome,source,occurred_at)
		VALUES (?,?,?,?,?,?,?,?)`), "migration-sentinel", actor, "session.login.succeeded", "session", actor, "succeeded", "migration-test", "2026-11-06T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), version); err != nil {
		t.Fatal(err)
	}
	assertAuditActorColumnValue(t, db, "actor_web_auth_session_id", actor)
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAuditActorColumnValue(t, db, "actor_session_id", actor)
	var action, target string
	if err := db.QueryRowContext(t.Context(), `SELECT action,target_type FROM balda_security_audit_events WHERE event_id='migration-sentinel'`).Scan(&action, &target); err != nil {
		t.Fatal(err)
	}
	if action != "session.login.succeeded" || target != "session" {
		t.Fatalf("migration rewrote immutable audit vocabulary: action=%q target=%q", action, target)
	}
}

func assertAuditActorColumnValue(t *testing.T, db *sql.DB, column, want string) {
	t.Helper()
	var got string
	if err := db.QueryRowContext(t.Context(), `SELECT `+column+` FROM balda_security_audit_events WHERE event_id='migration-sentinel'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", column, got, want)
	}
}
