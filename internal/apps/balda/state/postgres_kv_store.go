package state

import (
	"context"
	"database/sql"
	"encoding/json"

	"sort"
	"strings"
	"time"
)

type postgresKVStore struct {
	db        *sql.DB
	namespace string
}

func (s *postgresKVStore) Get(ctx context.Context, key string) (string, bool, error) {
	value, ok, err := s.GetJSON(ctx, key)
	if err != nil || !ok {
		return "", ok, err
	}

	if str, ok := value.(string); ok {
		return str, true, nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return "", false, postgresErrorf("marshal json value for key %q: %w", key, err)
	}
	return string(data), true, nil
}

func (s *postgresKVStore) Set(ctx context.Context, key, value string) error {
	return s.SetJSON(ctx, key, value)
}

func (s *postgresKVStore) SetWithTTL(ctx context.Context, key string, value any, ttl time.Duration) error {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return postgresErrorf("key is required")
	}

	expiresAt := time.Now().UTC().Add(ttl).Format(time.RFC3339)

	encoded, err := json.Marshal(value)
	if err != nil {
		return postgresErrorf("marshal json key %q: %w", trimmedKey, err)
	}

	if _, err := s.db.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_app_kv (namespace, key, value_json, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(namespace, key)
		DO UPDATE SET value_json = excluded.value_json, expires_at = excluded.expires_at, updated_at = excluded.updated_at`), s.namespace, trimmedKey, string(encoded), expiresAt, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return postgresErrorf("set json key with ttl %q: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresKVStore) Delete(ctx context.Context, key string) error {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, postgresBind(`
		DELETE FROM balda_app_kv
		WHERE namespace = ? AND key = ?`), s.namespace, trimmedKey,
	); err != nil {
		return postgresErrorf("delete key %q: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	args := []any{s.namespace}
	query := `
		SELECT key, expires_at
		FROM balda_app_kv
		WHERE namespace = ?`

	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix != "" {
		query += ` AND key ILIKE ?`
		args = append(args, trimmedPrefix+"%")
	}
	query += ` ORDER BY key`

	rows, err := s.db.QueryContext(ctx, postgresBind(query), args...)
	if err != nil {
		return nil, postgresErrorf("list keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := make([]string, 0)
	for rows.Next() {
		var key string
		var expiresAt sql.NullString
		if err := rows.Scan(&key, &expiresAt); err != nil {
			return nil, postgresErrorf("scan key: %w", err)
		}

		if expiresAt.Valid {
			expTime, parseErr := time.Parse(time.RFC3339, expiresAt.String)
			if parseErr == nil && time.Now().UTC().After(expTime) {
				continue
			}
		}

		if strings.TrimSpace(key) == "" {
			continue
		}

		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate keys: %w", err)
	}
	return keys, nil
}

func (s *postgresKVStore) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		DELETE FROM balda_app_kv
		WHERE namespace = ?`), s.namespace,
	); err != nil {
		return postgresErrorf("clear namespace %q: %w", s.namespace, err)
	}
	return nil
}

func (s *postgresKVStore) GetJSON(ctx context.Context, key string) (any, bool, error) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return nil, false, nil
	}

	var raw string
	var expiresAt sql.NullString
	err := s.db.QueryRowContext(ctx, postgresBind(`
		SELECT value_json, expires_at
		FROM balda_app_kv
		WHERE namespace = ? AND key = ?`), s.namespace, trimmedKey,
	).Scan(&raw, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, postgresErrorf("get json key %q: %w", trimmedKey, err)
	}

	if expiresAt.Valid {
		expTime, parseErr := time.Parse(time.RFC3339, expiresAt.String)
		if parseErr == nil && time.Now().UTC().After(expTime) {
			_ = s.Delete(ctx, trimmedKey)
			return nil, false, nil
		}
	}

	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, false, postgresErrorf("unmarshal json key %q: %w", trimmedKey, err)
	}
	return value, true, nil
}

func (s *postgresKVStore) ConsumeJSON(
	ctx context.Context,
	key string,
	shouldConsume func(value any) (bool, error),
) (any, bool, error) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return nil, false, nil
	}
	if shouldConsume == nil {
		return nil, false, postgresErrorf("consume predicate is required")
	}

	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return nil, false, postgresErrorf("begin consume json key %q: %w", trimmedKey, err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw string
	var expiresAt sql.NullString
	err = tx.QueryRowContext(ctx, postgresBind(`
		SELECT value_json, expires_at
		FROM balda_app_kv
		WHERE namespace = ? AND key = ?`), s.namespace, trimmedKey,
	).Scan(&raw, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			if commitErr := tx.Commit(); commitErr != nil {
				return nil, false, postgresErrorf("commit missing consume json key %q: %w", trimmedKey, commitErr)
			}
			return nil, false, nil
		}
		return nil, false, postgresErrorf("get json key %q for consume: %w", trimmedKey, err)
	}

	if expiresAt.Valid {
		expTime, parseErr := time.Parse(time.RFC3339, expiresAt.String)
		if parseErr == nil && time.Now().UTC().After(expTime) {
			if _, err := tx.ExecContext(ctx, postgresBind(`
				DELETE FROM balda_app_kv
				WHERE namespace = ? AND key = ?`), s.namespace, trimmedKey,
			); err != nil {
				return nil, false, postgresErrorf("delete expired json key %q: %w", trimmedKey, err)
			}
			if err := tx.Commit(); err != nil {
				return nil, false, postgresErrorf("commit expired consume json key %q: %w", trimmedKey, err)
			}
			return nil, false, nil
		}
	}

	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, false, postgresErrorf("unmarshal json key %q: %w", trimmedKey, err)
	}
	consume, err := shouldConsume(value)
	if err != nil {
		return nil, false, err
	}
	if !consume {
		if err := tx.Commit(); err != nil {
			return nil, false, postgresErrorf("commit skipped consume json key %q: %w", trimmedKey, err)
		}
		return value, false, nil
	}

	result, err := tx.ExecContext(ctx, postgresBind(`
		DELETE FROM balda_app_kv
		WHERE namespace = ? AND key = ?`), s.namespace, trimmedKey,
	)
	if err != nil {
		return nil, false, postgresErrorf("delete consumed json key %q: %w", trimmedKey, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, false, postgresErrorf("check consumed json key %q: %w", trimmedKey, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, postgresErrorf("commit consumed json key %q: %w", trimmedKey, err)
	}
	return value, rows == 1, nil
}

func (s *postgresKVStore) SetJSON(ctx context.Context, key string, value any) error {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return postgresErrorf("key is required")
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return postgresErrorf("marshal json key %q: %w", trimmedKey, err)
	}

	if _, err := s.db.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_app_kv (namespace, key, value_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(namespace, key)
		DO UPDATE SET value_json = excluded.value_json, expires_at = NULL, updated_at = excluded.updated_at`), s.namespace, trimmedKey, string(encoded), time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return postgresErrorf("set json key %q: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresKVStore) MergeJSON(ctx context.Context, key string, fields map[string]any) (map[string]any, error) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return nil, postgresErrorf("key is required")
	}
	if len(fields) == 0 {
		return map[string]any{}, nil
	}

	current, ok, err := s.GetJSON(ctx, trimmedKey)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]any)
	if ok {
		if currentMap, ok := current.(map[string]any); ok {
			for k, v := range currentMap {
				merged[k] = v
			}
		}
	}

	for k, v := range fields {
		merged[k] = v
	}

	if err := s.SetJSON(ctx, trimmedKey, merged); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]any, len(merged))
	for _, k := range keys {
		ordered[k] = merged[k]
	}

	return ordered, nil
}
