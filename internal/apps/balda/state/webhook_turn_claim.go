package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *sqliteJobStore) ClaimWebhookTurn(ctx context.Context, jobID string) (bool, error) {
	return claimWebhookTurn(ctx, s.db, func(query string) string { return query }, jobID)
}

func (s *postgresJobStore) ClaimWebhookTurn(ctx context.Context, jobID string) (bool, error) {
	claimed, err := claimWebhookTurn(ctx, s.db, postgresBind, jobID)
	return claimed, redactPostgresError(err)
}

func claimWebhookTurn(ctx context.Context, db *sql.DB, bind func(string) string, jobID string) (bool, error) {
	jobID = strings.TrimSpace(jobID)
	if !strings.HasPrefix(jobID, "webhook-") {
		return false, fmt.Errorf("webhook job id is required")
	}
	claimedAt := time.Now().UTC().Format(time.RFC3339)
	result, err := db.ExecContext(ctx, bind(`UPDATE execution_jobs
		SET webhook_turn_claimed_at = ?, updated_at = ?
		WHERE id = ? AND private_run_kind = ? AND webhook_turn_claimed_at = ''
		  AND status NOT IN ('completed', 'failed', 'canceled', 'deadlettered')
		  AND COALESCE(result, '') = ''`), claimedAt, claimedAt, jobID, PrivateRunKindWebhook)
	if err != nil {
		return false, fmt.Errorf("claim webhook turn: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check webhook turn claim: %w", err)
	}
	if changed == 1 {
		return true, nil
	}
	var kind string
	if err := db.QueryRowContext(ctx, bind(`SELECT private_run_kind FROM execution_jobs WHERE id = ?`), jobID).Scan(&kind); err != nil {
		return false, fmt.Errorf("load webhook turn claim: %w", err)
	}
	if kind != PrivateRunKindWebhook {
		return false, fmt.Errorf("job %q is not a webhook private run", jobID)
	}
	return false, nil
}
