package state

import (
	"context"
	"database/sql"
	"fmt"
)

func up00027QuestionFailures(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE balda_questions ADD COLUMN failure_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE balda_questions ADD COLUMN failed_at TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("add question failure state: %w", err)
		}
	}
	return nil
}

func down00027QuestionFailures(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE balda_questions DROP COLUMN failed_at`,
		`ALTER TABLE balda_questions DROP COLUMN failure_json`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("drop question failure state: %w", err)
		}
	}
	return nil
}
