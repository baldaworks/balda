//go:build integration && sqlite

package state

import (
	"database/sql"
	"io/fs"
	"slices"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSQLiteRuntimeOperationStateMigration(t *testing.T) {
	db, err := sql.Open(databaseSQLite, "file:"+t.TempDir()+"/runtime-slot.db")
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
	if _, err := provider.UpTo(t.Context(), 70); err != nil {
		t.Fatal(err)
	}
	insertLegacyRuntimeSession(t, db, func(query string) string { return query })
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertSQLiteRuntimeOperationSchema(t, db)
	checkRuntimeOperationStateMigration(t, db, func(query string) string { return query })
}

func assertSQLiteRuntimeOperationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `PRAGMA table_info(balda_runtime_operations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	want := []string{"operation_id", "session_id", "input_hash", "kind", "state", "expected_archive_id", "expected_generation", "target_archive_id", "target_snapshot_id", "target_provider_binding_json", "target_model", "outcome", "reason_code", "created_at", "updated_at", "terminal_at"}
	if !slices.Equal(columns, want) {
		t.Fatalf("operation columns = %v, want %v", columns, want)
	}
	for _, name := range []string{"balda_runtime_operation_unresolved", "balda_runtime_operation_retention"} {
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %q count = %d, err=%v", name, count, err)
		}
	}
}
