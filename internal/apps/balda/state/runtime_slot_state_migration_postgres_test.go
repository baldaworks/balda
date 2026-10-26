//go:build integration && postgres

package state

import (
	"database/sql"
	"io/fs"
	"slices"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestPostgresRuntimeOperationStateMigration(t *testing.T) {
	db := newPostgresTestDB(t)
	migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(goose.NewGoMigration(10,
			&goose.GoFunc{RunTx: up00010PostgresUserConversion}, &goose.GoFunc{RunTx: downUserConversion})))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 35); err != nil {
		t.Fatal(err)
	}
	insertLegacyRuntimeSession(t, db, postgresBind)
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertPostgresRuntimeOperationSchema(t, db)
	checkRuntimeOperationStateMigration(t, db, postgresBind)
}

func assertPostgresRuntimeOperationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'balda_runtime_operations'
		ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
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
		if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %q count = %d, err=%v", name, count, err)
		}
	}
}
