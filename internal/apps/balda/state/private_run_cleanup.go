package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func listUnclosedTerminalWebhookJobs(ctx context.Context, db *sql.DB, bind func(string) string,
	after time.Time, afterID string, before time.Time, limit int) ([]JobRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("private run cleanup limit must be positive")
	}
	if before.IsZero() {
		return nil, fmt.Errorf("private run cleanup cutoff is required")
	}
	cursor := ""
	if !after.IsZero() {
		cursor = after.UTC().Format(time.RFC3339)
	}
	rows, err := db.QueryContext(ctx, bind(executionJobSelectSQL+`
		WHERE private_run_kind = ? AND private_run_closed_at = ''
		  AND status IN (?, ?, ?, ?)
		  AND created_at <= ?
		  AND (created_at > ? OR (created_at = ? AND id > ?))
		ORDER BY created_at, id LIMIT ?`),
		PrivateRunKindWebhook, JobStatusCompleted, JobStatusFailed, JobStatusCanceled,
		JobStatusDeadLettered, before.UTC().Format(time.RFC3339), cursor, cursor, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unclosed webhook runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]JobRecord, 0, limit)
	for rows.Next() {
		job, _, err := scanExecutionJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read unclosed webhook runs: %w", err)
	}
	return jobs, nil
}

func markWebhookRunClosed(ctx context.Context, db *sql.DB, bind func(string) string, jobID string, closedAt time.Time) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || closedAt.IsZero() {
		return fmt.Errorf("webhook job id and close time are required")
	}
	result, err := db.ExecContext(ctx, bind(`UPDATE execution_jobs SET private_run_closed_at = ?, updated_at = ?
		WHERE id = ? AND private_run_kind = ? AND private_run_closed_at = ''
		  AND status IN (?, ?, ?, ?)`),
		closedAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339),
		jobID, PrivateRunKindWebhook, JobStatusCompleted, JobStatusFailed, JobStatusCanceled, JobStatusDeadLettered)
	if err != nil {
		return fmt.Errorf("mark webhook run closed: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check webhook run close: %w", err)
	}
	if changed == 1 {
		return nil
	}
	var kind, prior string
	if err := db.QueryRowContext(ctx, bind(`SELECT private_run_kind, private_run_closed_at
		FROM execution_jobs WHERE id = ?`), jobID).Scan(&kind, &prior); err != nil {
		return fmt.Errorf("load webhook run close state: %w", err)
	}
	if kind != PrivateRunKindWebhook || prior == "" {
		return fmt.Errorf("webhook run is not eligible for close")
	}
	return nil
}
