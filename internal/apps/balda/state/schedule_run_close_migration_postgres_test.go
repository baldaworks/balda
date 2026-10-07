//go:build integration && postgres

package state

import (
	"database/sql"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestPostgresScheduleRunCloseMigrationPreservesOptionalReport(t *testing.T) {
	db, provider := newPostgresScheduleRunCloseMigrationDB(t)
	if _, err := provider.UpTo(t.Context(), 16); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_scheduled_jobs
		(job_id, session_id, channel_type, address_key, address_json, report_to_enabled,
		 content, schedule_spec, next_run_at, created_at, updated_at, source, target_kind, target_key)
		VALUES ('daily', 'tg-1-0', 'telegram', '1:0', '{}', 0,
		 'frozen input', '0 9 * * *', '2026-10-08T09:00:00Z',
		 '2026-10-07T00:00:00Z', '2026-10-07T00:00:00Z', 'managed', 'locator', 'telegram:1:0')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_schedule_runs
		(run_id, schedule_id, trigger, trigger_key, definition_version, requested_at,
		 dispatch_state, execution_job_id, payload_json, created_at, updated_at)
		VALUES ('run-1', 'daily', 'manual', 'manual:one', 1, '2026-10-07T00:00:00Z',
		 'dispatched', 'scheduled-daily-one', '{"content":"frozen input"}',
		 '2026-10-07T00:00:00Z', '2026-10-07T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var reportEnabled bool
	if err := db.QueryRowContext(t.Context(), `SELECT report_to_enabled FROM balda_scheduled_jobs
		WHERE job_id = 'daily'`).Scan(&reportEnabled); err != nil || reportEnabled {
		t.Fatalf("report flag = %v, err=%v", reportEnabled, err)
	}
	var snapshot, closedAt string
	if err := db.QueryRowContext(t.Context(), `SELECT payload_json, closed_at FROM balda_schedule_runs
		WHERE run_id = 'run-1'`).Scan(&snapshot, &closedAt); err != nil ||
		snapshot != `{"content":"frozen input"}` || closedAt != "" {
		t.Fatalf("run after migration = %q/%q, err=%v", snapshot, closedAt, err)
	}
}

func newPostgresScheduleRunCloseMigrationDB(t *testing.T) (*sql.DB, *goose.Provider) {
	t.Helper()
	db := newPostgresTestDB(t)
	migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithDisableGlobalRegistry(true), goose.WithGoMigrations(goose.NewGoMigration(10,
			&goose.GoFunc{RunTx: up00010PostgresUserConversion}, &goose.GoFunc{RunTx: downUserConversion})))
	if err != nil {
		t.Fatal(err)
	}
	return db, provider
}
