//go:build integration && (sqlite || postgres)

package state

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func checkWebhookSlugMigration(t *testing.T, db *sql.DB, migrations *goose.Provider, previousVersion int64, bind func(string) string, indexQuery string) {
	t.Helper()
	if _, err := migrations.UpTo(t.Context(), previousVersion); err != nil {
		t.Fatal(err)
	}
	const columns = `name, source, prompt_template, report_to_kind, report_to_key, ack_on_delivery, dedupe_source, dedupe_header, auth_type, auth_header, secret_verifier, enabled, deleted, definition_version, created_at, updated_at`
	for _, row := range []struct {
		name, source, verifier string
		enabled, deleted       int
	}{
		{"managed", "managed", strings.Repeat("a", 64), 1, 0},
		{"disabled", "managed", strings.Repeat("b", 64), 0, 0},
		{"configured", "config", "", 1, 0},
		{"archived", "config", "", 0, 1},
		{"archived-managed", "managed", strings.Repeat("c", 64), 0, 1},
	} {
		_, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_webhook_routes (`+columns+`, path) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), row.name, row.source, "prompt", "alias", "main", 1, "header", "X-Request-ID", "header", "X-Token", row.verifier, row.enabled, row.deleted, 7, "2026-10-09T12:00:00Z", "2026-10-10T12:00:00Z", "/custom/"+row.name)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_webhook_admissions (route_name, dedupe_key, request_id, prompt, job_id, session_id, created_at, source, raw_body) VALUES ('managed', 'key', 'request', 'rendered', 'job', 'session', '2026-10-10T12:00:00Z', 'external', ?)`), []byte("request input\x00\xff")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO execution_jobs (id, objective, status, result, created_at, updated_at) VALUES ('job', 'rendered', 'completed', 'reported output', '2026-10-10T12:00:00Z', '2026-10-10T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	output := webhookMigrationRows(t, db, `SELECT id, result FROM execution_jobs WHERE id = 'job'`)
	before := webhookMigrationRows(t, db, `SELECT `+columns+` FROM balda_webhook_routes ORDER BY name`)
	history := webhookMigrationRows(t, db, `SELECT * FROM balda_webhook_admissions ORDER BY job_id`)
	if _, err := migrations.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := webhookMigrationRows(t, db, `SELECT `+columns+` FROM balda_webhook_routes ORDER BY name`); !reflect.DeepEqual(before, after) {
		t.Fatalf("route metadata changed: before=%v after=%v", before, after)
	}
	if after := webhookMigrationRows(t, db, `SELECT * FROM balda_webhook_admissions ORDER BY job_id`); !reflect.DeepEqual(history, after) {
		t.Fatalf("history changed: before=%v after=%v", history, after)
	}
	if after := webhookMigrationRows(t, db, `SELECT id, result FROM execution_jobs WHERE id = 'job'`); !reflect.DeepEqual(output, after) {
		t.Fatalf("output changed: before=%v after=%v", output, after)
	}
	rows, err := db.QueryContext(t.Context(), `SELECT * FROM balda_webhook_routes`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := rows.Columns()
	_ = rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "path" {
			t.Fatal("route schema still persists a path")
		}
	}
	var indexes int
	if err := db.QueryRowContext(t.Context(), indexQuery).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Fatalf("path indexes = %d, want 0", indexes)
	}
	if _, err := migrations.Down(t.Context()); err == nil {
		t.Fatal("downgrade unexpectedly reconstructed discarded callback paths")
	}
	if after := webhookMigrationRows(t, db, `SELECT `+columns+` FROM balda_webhook_routes ORDER BY name`); !reflect.DeepEqual(before, after) {
		t.Fatal("refused downgrade changed route metadata")
	}
}

func webhookMigrationRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
