package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func updateScheduledJobRuntime(
	ctx context.Context, db *sql.DB, bind func(string) string, update ScheduledJobRuntimeUpdate,
) (bool, error) {
	if strings.TrimSpace(update.JobID) == "" || update.DefinitionVersion == 0 ||
		update.ExpectedNextRunAt.IsZero() || update.NextRunAt.IsZero() {
		return false, fmt.Errorf("scheduled job runtime update requires an id, version, and slot")
	}
	if update.Status != ScheduledJobStatusActive && update.Status != ScheduledJobStatusPaused {
		return false, fmt.Errorf("unsupported scheduled job status")
	}
	lastRunAt := ""
	if !update.LastRunAt.IsZero() {
		lastRunAt = update.LastRunAt.UTC().Format(time.RFC3339)
	}
	result, err := db.ExecContext(ctx, bind(`UPDATE balda_scheduled_jobs SET
		status = ?, retry_count = ?, last_dispatch_key = ?, next_run_at = ?,
		last_run_at = ?, last_error = ?, updated_at = ?
		WHERE job_id = ? AND definition_version = ? AND next_run_at = ?
		AND last_dispatch_key = ? AND enabled = 1 AND deleted = 0`),
		update.Status, update.RetryCount, update.LastDispatchKey,
		update.NextRunAt.UTC().Format(time.RFC3339), lastRunAt,
		update.LastError, time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(update.JobID), update.DefinitionVersion,
		update.ExpectedNextRunAt.UTC().Format(time.RFC3339),
		update.ExpectedLastDispatchKey)
	if err != nil {
		return false, fmt.Errorf("update scheduled job runtime: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count scheduled job runtime updates: %w", err)
	}
	return count == 1, nil
}
