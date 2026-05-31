package state

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var baldaMigrationsFS embed.FS

var requiredBaldaSQLiteTables = []string{
	"balda_app_kv",
	"balda_session_metadata",
	"balda_telegram_offsets",
	"balda_collaborators",
	"balda_runtime_app_state",
	"balda_runtime_user_state",
	"balda_runtime_sessions",
	"balda_runtime_events",
	"balda_scheduled_tasks",
	"swarm_tasks",
	"swarm_task_events",
	"swarm_delivery_outbox",
	"swarm_agent_steps",
}

func migrate(ctx context.Context, db *sql.DB) error {
	migrationsDir, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open balda migrations fs: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationsDir)
	if err != nil {
		return fmt.Errorf("create balda migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply balda migrations: %w", err)
	}
	if err := reconcileRuntimeSessionTables(ctx, db); err != nil {
		return err
	}
	if err := validateBaldaSQLiteSchema(ctx, db); err != nil {
		return err
	}
	return nil
}

func reconcileRuntimeSessionTables(ctx context.Context, db *sql.DB) error {
	runtimeExists, err := sqliteTableExists(ctx, db, "balda_runtime_app_state")
	if err != nil {
		return fmt.Errorf("inspect runtime app state table: %w", err)
	}
	if runtimeExists {
		return ensureRuntimeSessionIndexes(ctx, db)
	}
	previousSchemaExists, err := sqliteTableExists(ctx, db, "balda_adk_app_state")
	if err != nil {
		return fmt.Errorf("inspect pre-migration app state table: %w", err)
	}
	if !previousSchemaExists {
		return fmt.Errorf("runtime session tables are missing")
	}

	steps := []string{
		"ALTER TABLE balda_adk_app_state RENAME TO balda_runtime_app_state",
		"ALTER TABLE balda_adk_user_state RENAME TO balda_runtime_user_state",
		"ALTER TABLE balda_adk_sessions RENAME TO balda_runtime_sessions",
		"ALTER TABLE balda_adk_events RENAME TO balda_runtime_events",
	}
	for _, stmt := range steps {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("reconcile runtime session tables: %w", err)
		}
	}
	return ensureRuntimeSessionIndexes(ctx, db)
}

func ensureRuntimeSessionIndexes(ctx context.Context, db *sql.DB) error {
	hasRuntimeSessionColumns, err := sqliteTableHasColumn(ctx, db, "balda_runtime_sessions", "app_name")
	if err != nil {
		return fmt.Errorf("inspect runtime session columns: %w", err)
	}
	if !hasRuntimeSessionColumns {
		return nil
	}
	stmts := []string{
		"DROP INDEX IF EXISTS idx_balda_adk_sessions_app_user",
		"DROP INDEX IF EXISTS idx_balda_adk_events_session_order",
		"CREATE INDEX IF NOT EXISTS idx_balda_runtime_sessions_app_user ON balda_runtime_sessions(app_name, user_id)",
		"CREATE INDEX IF NOT EXISTS idx_balda_runtime_events_session_order ON balda_runtime_events(app_name, user_id, session_id, timestamp, ordinal)",
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure runtime session indexes: %w", err)
		}
	}
	return nil
}

func sqliteTableHasColumn(ctx context.Context, db *sql.DB, table string, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dfltVal sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltVal, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(column)) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func validateBaldaSQLiteSchema(ctx context.Context, db *sql.DB) error {
	for _, table := range requiredBaldaSQLiteTables {
		exists, err := sqliteTableExists(ctx, db, table)
		if err != nil {
			return fmt.Errorf("inspect %s table: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("balda state schema missing %s; back up and delete the Balda state database, then run balda init again", table)
		}
	}
	return nil
}

func sqliteTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `
		SELECT 1
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
		LIMIT 1`,
		name,
	).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
