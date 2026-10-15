package state

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// This catches a migration that loses existing recurring jobs or exposes
// internal one-shot timers as administrator-managed schedules.
func TestScheduleManagementMigrationClassifiesExistingJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	registerBaldaGoMigrations()
	migrations, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id   string
		spec string
	}{
		{id: "daily-review", spec: "0 9 * * *"},
		{id: "question-timeout-1", spec: "@once"},
	} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_scheduled_jobs
			(job_id, session_id, channel_type, address_key, address_json, content,
			 schedule_spec, next_run_at, created_at, updated_at)
			VALUES (?, 'tg-1-0', 'telegram', '1:0', '{}', 'review', ?,
			 '2026-10-08T09:00:00Z', '2026-10-07T00:00:00Z', '2026-10-07T00:00:00Z')`, row.id, row.spec); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = check.Close() })

	for _, row := range []struct {
		id     string
		source string
	}{
		{id: "daily-review", source: "config"},
		{id: "question-timeout-1", source: "internal"},
	} {
		var source, targetKind, targetKey string
		var enabled, deleted, version int
		if err := check.QueryRowContext(t.Context(), `SELECT source, enabled, deleted, definition_version, target_kind, target_key
			FROM balda_scheduled_jobs WHERE job_id = ?`, row.id).Scan(&source, &enabled, &deleted, &version, &targetKind, &targetKey); err != nil {
			t.Fatal(err)
		}
		if source != row.source || enabled != 1 || deleted != 0 || version != 1 {
			t.Errorf("job %q metadata = %q/%d/%d/%d, want %q/1/0/1", row.id, source, enabled, deleted, version, row.source)
		}
		if targetKind != "locator" || targetKey != "telegram:1:0" {
			t.Errorf("job %q target = %q/%q, want locator/telegram:1:0", row.id, targetKind, targetKey)
		}
	}

	var runTable string
	if err := check.QueryRowContext(t.Context(), `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'balda_schedule_runs'`).Scan(&runTable); err != nil {
		t.Fatalf("run ledger missing: %v", err)
	}
}
