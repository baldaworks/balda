package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func recordScheduledOutput(ctx context.Context, db *sql.DB, bind func(string) string, jobID, output string) error {
	jobID = strings.TrimSpace(jobID)
	if !strings.HasPrefix(jobID, "scheduled-") || output == "" {
		return fmt.Errorf("scheduled job id and output are required")
	}
	result, err := db.ExecContext(ctx, bind(`UPDATE execution_jobs SET result = ?, updated_at = ?
		WHERE id = ? AND COALESCE(result, '') = ''`), output, time.Now().UTC().Format(time.RFC3339), jobID)
	if err != nil {
		return fmt.Errorf("record scheduled output: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check scheduled output update: %w", err)
	}
	if changed == 1 {
		return nil
	}
	var existing string
	if err := db.QueryRowContext(ctx, bind(`SELECT COALESCE(result, '') FROM execution_jobs WHERE id = ?`), jobID).Scan(&existing); err != nil {
		return fmt.Errorf("load scheduled output: %w", err)
	}
	if existing != output {
		return fmt.Errorf("scheduled output conflicts with prior result")
	}
	return nil
}
