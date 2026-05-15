package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const defaultBaldaBotOffsetKey = "balda-default"

type sqliteOffsetStore struct {
	db *sql.DB
}

func (s *sqliteOffsetStore) Load(ctx context.Context) (int, error) {
	var offset int
	err := s.db.QueryRowContext(ctx, `
		SELECT offset
		FROM balda_telegram_offsets
		WHERE bot_key = ?`,
		defaultBaldaBotOffsetKey,
	).Scan(&offset)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("load telegram offset: %w", err)
	}
	return offset, nil
}

func (s *sqliteOffsetStore) Save(ctx context.Context, offset int) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO balda_telegram_offsets (bot_key, offset, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(bot_key) DO UPDATE SET
			offset = excluded.offset,
			updated_at = excluded.updated_at`,
		defaultBaldaBotOffsetKey,
		offset,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("save telegram offset: %w", err)
	}
	return nil
}
