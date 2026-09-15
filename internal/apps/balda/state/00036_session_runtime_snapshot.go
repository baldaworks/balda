package state

import (
	"context"
	"database/sql"
	"fmt"
)

func up00036SessionRuntimeSnapshot(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		ALTER TABLE balda_session_metadata
		ADD COLUMN runtime_snapshot_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add session runtime snapshot: %w", err)
	}
	return nil
}

func down00036SessionRuntimeSnapshot(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		ALTER TABLE balda_session_metadata
		DROP COLUMN runtime_snapshot_id`); err != nil {
		return fmt.Errorf("drop session runtime snapshot: %w", err)
	}
	return nil
}
