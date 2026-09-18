package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDatabaseDefaultPreservesExistingSQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db, err := sql.Open(databaseSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture, err := os.ReadFile("testdata/sqlite_v36.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatal(err)
	}
	seedSQLiteCompatibilityFixture(t, db)
	before := snapshotSQLiteFixture(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	originalFile, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := (DatabaseConfig{}).Resolve(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := openProvider(t.Context(), cfg, NewSQLiteProvider,
		func(context.Context, PostgresConfig) (Provider, error) {
			t.Fatal("default configuration tried PostgreSQL")
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	reopenedFile, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(originalFile, reopenedFile) {
		t.Fatal("opening the default database replaced the existing file")
	}
	after := snapshotSQLiteFixture(t, p.(*sqliteProvider).db)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("opening the default database changed existing rows or migration history")
	}
	user, found, err := p.Collaborators().GetCollaborator(t.Context(), "fixture")
	if err != nil || !found || user.UserID != "fixture" {
		t.Fatalf("existing collaborator was not preserved: found=%t err=%v", found, err)
	}
	var foreignKeys int
	if err := p.(*sqliteProvider).db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatal("foreign keys are disabled")
	}
}

func seedSQLiteCompatibilityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), "PRAGMA defer_foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	for _, table := range requiredBaldaSQLiteTables {
		rows, err := tx.QueryContext(t.Context(), "PRAGMA table_info("+table+")")
		if err != nil {
			t.Fatal(err)
		}
		var columns, placeholders []string
		var values []any
		for rows.Next() {
			var ordinal, notNull, primaryKey int
			var name, kind string
			var defaultValue sql.NullString
			if err := rows.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, `"`+name+`"`)
			placeholders = append(placeholders, "?")
			values = append(values, sqliteFixtureValue(table, name, kind))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		query := "INSERT INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(placeholders, ",") + ")"
		if _, err := tx.ExecContext(t.Context(), query, values...); err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func sqliteFixtureValue(table, column, kind string) any {
	if kind == "INTEGER" {
		return 1
	}
	if strings.HasSuffix(column, "_at") || column == "timestamp" {
		return "2026-09-18T00:00:00Z"
	}
	if strings.HasSuffix(column, "_json") {
		return "{}"
	}
	if column == "state" && table == "balda_plugin_activation_intents" {
		return PluginActivationIntentPending
	}
	return "fixture"
}

func snapshotSQLiteFixture(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	snapshot := make(map[string][]string)
	tables := append([]string{"goose_db_version"}, requiredBaldaSQLiteTables...)
	for _, table := range tables {
		rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1") //nolint:unqueryvet // The compatibility snapshot must compare every stored column.
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			snapshot[table] = append(snapshot[table], string(encoded))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}
