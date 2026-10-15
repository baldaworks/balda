package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func up00045UserConversion(ctx context.Context, tx *sql.Tx) error {
	return convertUsersTx(ctx, tx, func(query string) string { return query })
}

func up00010PostgresUserConversion(ctx context.Context, tx *sql.Tx) error {
	return convertUsersTx(ctx, tx, postgresBind)
}

func downUserConversion(context.Context, *sql.Tx) error {
	return fmt.Errorf("user conversion is forward-only; restore a pre-upgrade database backup")
}

// Conversion and Goose's version marker share a transaction. Never open a
// provider or start a nested store transaction from a migration callback.
func convertUsersTx(ctx context.Context, tx *sql.Tx, bind func(string) string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM balda_user_migrations`).Scan(&count); err != nil {
		return fmt.Errorf("check completed user conversion: %w", err)
	}
	if count != 0 {
		return nil
	}
	input, err := readUserConversionTx(ctx, tx, bind)
	if err != nil {
		return err
	}
	if input.Owner == nil && len(input.Collaborators) == 0 {
		return nil
	}
	prepared, err := prepareUserConversion(input)
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM balda_users`).Scan(&count); err != nil {
		return fmt.Errorf("check canonical users before conversion: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("convert users: %w: unmarked source mixed with canonical users", usercmd.ErrConflict)
	}
	completedAt := formatUserTime(time.Now().UTC())
	for _, entry := range prepared.users {
		user, binding := entry.user, entry.binding
		if _, err := tx.ExecContext(ctx, bind(`
			INSERT INTO balda_users (user_id, display_name, username, normalized_username, status, role,
				password_hash, credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', 'disabled', 0, ?, 1, 1, ?, ?)`),
			user.ID, user.DisplayName, user.Username, user.NormalizedUsername, user.Status, user.Role,
			boolInt(user.Primary), completedAt, completedAt); err != nil {
			return fmt.Errorf("insert converted user: %w", err)
		}
		if _, err := tx.ExecContext(ctx, bind(`
			INSERT INTO balda_user_bindings (binding_id, user_id, channel_type, principal, display_name,
				provider_username, provider_first_name, provenance, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			binding.ID, binding.UserID, binding.ChannelType, binding.Principal, binding.DisplayName,
			binding.ProviderUsername, binding.ProviderFirstName, binding.Provenance, completedAt, completedAt); err != nil {
			return fmt.Errorf("insert converted binding: %w", err)
		}
		for _, target := range []struct {
			id   string
			kind usercmd.AuditTargetType
		}{{user.ID, usercmd.AuditTargetUser}, {binding.ID, usercmd.AuditTargetBinding}} {
			if _, err := tx.ExecContext(ctx, bind(`
				INSERT INTO balda_security_audit_events
					(event_id, action, target_type, target_id, outcome, reason, source, correlation_id, occurred_at)
				VALUES (?, 'user.migrated', ?, ?, 'succeeded', 'legacy authorization migration',
					'legacy-user-migration', ?, ?)`),
				userConversionID("audit", prepared.fingerprint+":"+string(target.kind)+":"+target.id),
				target.kind, target.id, prepared.fingerprint, completedAt); err != nil {
				return fmt.Errorf("insert user conversion audit: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, bind(`
		INSERT INTO balda_user_migrations (migration_id, source_fingerprint, source_counts_json,
			generated_user_count, generated_binding_count, primary_user_id, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`),
		userConversionID("migration", prepared.fingerprint), prepared.fingerprint, prepared.sourceCountsJSON,
		len(prepared.users), len(prepared.users), prepared.primaryUserID, completedAt); err != nil {
		return fmt.Errorf("record user conversion: %w", err)
	}
	return nil
}

func readUserConversionTx(ctx context.Context, tx *sql.Tx, bind func(string) string) (userConversionInput, error) {
	var input userConversionInput
	var raw string
	var expiresAt sql.NullString
	err := tx.QueryRowContext(ctx, bind(`SELECT value_json, expires_at FROM balda_app_kv
		WHERE namespace = ? AND key = 'owner'`), NamespaceApp).Scan(&raw, &expiresAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return input, fmt.Errorf("read conversion owner: %w", err)
	}
	if err == nil {
		// Match the KV store's expiry semantics without mutating retained source.
		expires, parseErr := time.Parse(time.RFC3339, expiresAt.String)
		if !expiresAt.Valid || parseErr != nil || !time.Now().UTC().After(expires) {
			if err := json.Unmarshal([]byte(raw), &input.Owner); err != nil {
				return input, fmt.Errorf("decode conversion owner: %w", err)
			}
			if input.Owner == nil {
				return input, fmt.Errorf("conversion owner record is null")
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id, username, first_name, added_by, added_at
		FROM balda_collaborators ORDER BY added_at DESC, user_id`)
	if err != nil {
		return input, fmt.Errorf("read conversion collaborators: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var collaborator authcmd.Collaborator
		var addedAt string
		if err := rows.Scan(&collaborator.UserID, &collaborator.Username, &collaborator.FirstName, &collaborator.AddedBy, &addedAt); err != nil {
			return input, fmt.Errorf("scan conversion collaborator: %w", err)
		}
		collaborator.AddedAt, err = time.Parse(time.RFC3339, addedAt)
		if err != nil {
			return input, fmt.Errorf("parse conversion collaborator timestamp: %w", err)
		}
		input.Collaborators = append(input.Collaborators, collaborator)
	}
	if err := rows.Err(); err != nil {
		return input, fmt.Errorf("iterate conversion collaborators: %w", err)
	}
	return input, nil
}
