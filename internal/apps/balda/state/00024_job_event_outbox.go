package state

import (
	"context"
	"database/sql"
	"fmt"
)

func up00024JobEventOutbox(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS execution_job_event_outbox (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			subject TEXT NOT NULL,
			envelope_json TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			created_at TEXT NOT NULL,
			published_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_execution_job_event_outbox_pending
			ON execution_job_event_outbox(published_at, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create job event outbox: %w", err)
		}
	}
	return nil
}

func down00024JobEventOutbox(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS execution_job_event_outbox`); err != nil {
		return fmt.Errorf("drop job event outbox: %w", err)
	}
	return nil
}
