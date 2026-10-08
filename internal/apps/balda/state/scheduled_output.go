package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func recordScheduledOutput(ctx context.Context, db *sql.DB, bind func(string) string, jobID, output string) error {
	if !strings.HasPrefix(strings.TrimSpace(jobID), "scheduled-") {
		return fmt.Errorf("scheduled job id is required")
	}
	return recordPrivateOutput(ctx, db, bind, jobID, output, false)
}

func recordPrivateOutput(ctx context.Context, db *sql.DB, bind func(string) string, jobID, output string, failed bool) error {
	jobID = strings.TrimSpace(jobID)
	if !strings.HasPrefix(jobID, "scheduled-") && !strings.HasPrefix(jobID, "webhook-") || strings.TrimSpace(output) == "" {
		return fmt.Errorf("private job id and output are required")
	}
	failureCode := ""
	if failed {
		failureCode = "private run failed"
	}
	result, err := db.ExecContext(ctx, bind(`UPDATE execution_jobs SET result = ?, error = ?, updated_at = ?
		WHERE id = ? AND COALESCE(result, '') = ''`), output, failureCode, time.Now().UTC().Format(time.RFC3339), jobID)
	if err != nil {
		return fmt.Errorf("record private output: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check private output update: %w", err)
	}
	if changed == 1 {
		return nil
	}
	var existing, existingError string
	if err := db.QueryRowContext(ctx, bind(`SELECT COALESCE(result, ''), COALESCE(error, '') FROM execution_jobs WHERE id = ?`), jobID).
		Scan(&existing, &existingError); err != nil {
		return fmt.Errorf("load private output: %w", err)
	}
	if existing != output || existingError != failureCode {
		return fmt.Errorf("private output conflicts with prior result")
	}
	return nil
}
