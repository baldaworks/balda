//go:build integration && sqlite

package state

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteUnifiedUserSchemaConstraints(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	db := provider.(*sqliteProvider).db

	insertSQLiteUser(t, db, "admin-1", "admin.one", true)
	insertSQLiteUser(t, db, "operator-1", "operator.one", false)

	assertSQLiteRejected(t, db, `INSERT INTO balda_users
		(user_id, display_name, username, normalized_username, status, role, password_hash,
		 credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES ('bad-role', 'Bad', 'bad', 'bad', 'active', 'owner', 'hash',
		        'active', 0, 0, 1, 1, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
	assertSQLiteRejected(t, db, `INSERT INTO balda_users
		(user_id, display_name, username, normalized_username, status, role, password_hash,
		 credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES ('bad-temporary', 'Bad', 'bad-temporary', 'bad-temporary', 'active', 'operator', 'hash',
		        'temporary', 0, 0, 1, 1, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
	assertSQLiteRejected(t, db, `UPDATE balda_users SET normalized_username = 'admin.one' WHERE user_id = 'operator-1'`)
	assertSQLiteRejected(t, db, `UPDATE balda_users SET normalized_username = ' Mixed ' WHERE user_id = 'operator-1'`)
	assertSQLiteRejected(t, db, `UPDATE balda_users SET is_primary = 1 WHERE user_id = 'operator-1'`)

	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_user_bindings
		(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES ('binding-1', 'admin-1', 'telegram', '101', 'Admin', 'migration',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	assertSQLiteRejected(t, db, `INSERT INTO balda_user_bindings
		(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES ('binding-2', 'admin-1', 'slack', 'U101', '', '',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
	assertSQLiteRejected(t, db, `INSERT INTO balda_user_bindings
		(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES ('binding-3', 'operator-1', 'telegram', '101', '', '',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)

	insertSQLiteSession(t, db)
	insertSQLiteRefresh(t, db, "refresh-1", 1, "active", "", "2026-09-24T00:00:00Z")
	assertSQLiteRejected(t, db, `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-2', X'02', 2, 'active',
		        '2026-09-23T00:05:00Z', '', '2026-09-24T00:00:00Z')`)
	if _, err := db.ExecContext(t.Context(), `UPDATE balda_backoffice_refresh_tokens
		SET state = 'used', used_at = '2026-09-23T00:10:00Z'
		WHERE selector = 'refresh-1'`); err != nil {
		t.Fatalf("consume active refresh: %v", err)
	}
	assertSQLiteRejected(t, db, `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-short', X'03', 2, 'active',
		        '2026-09-23T00:05:00Z', '', '2026-09-23T12:00:00Z')`)
	assertSQLiteRejected(t, db, `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', 'refresh-used', X'04', 2, 'used',
		        '2026-09-23T00:05:00Z', '2026-09-23T00:06:00Z', '2026-09-24T00:00:00Z')`)
	assertSQLiteRejected(t, db, `UPDATE balda_backoffice_refresh_tokens
		SET state = 'active', used_at = '' WHERE selector = 'refresh-1'`)
	assertSQLiteRejected(t, db, `UPDATE balda_backoffice_sessions
		SET refresh_expires_at = '2026-09-25T00:00:00Z' WHERE session_id = 'session-1'`)

	if _, err := db.ExecContext(t.Context(), `DELETE FROM balda_users WHERE user_id = 'admin-1'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	for _, table := range []string{"balda_user_bindings", "balda_backoffice_sessions", "balda_backoffice_refresh_tokens"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s rows after user deletion = %d, want 0", table, count)
		}
	}
}

func insertSQLiteUser(t *testing.T, db *sql.DB, id, normalizedUsername string, primary bool) {
	t.Helper()
	primaryValue := 0
	if primary {
		primaryValue = 1
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_users
		(user_id, display_name, username, normalized_username, status, role, password_hash,
		 credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, 'hash', 'active', 0, ?, 1, 1,
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`,
		id, id, normalizedUsername, normalizedUsername,
		map[bool]string{true: "administrator", false: "operator"}[primary], primaryValue,
	); err != nil {
		t.Fatalf("insert user %q: %v", id, err)
	}
}

func insertSQLiteSession(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_backoffice_sessions
		(session_id, user_id, assurance, credential_version, access_selector,
		 access_verifier_digest, csrf_verifier_digest, created_at, last_seen_at,
		 access_expires_at, refresh_expires_at, revoked_at, revocation_reason, version)
		VALUES ('session-1', 'admin-1', 'normal', 1, 'access-1', X'01', X'02',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z',
		        '2026-09-23T00:15:00Z', '2026-09-24T00:00:00Z', '', '', 1)`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func insertSQLiteRefresh(t *testing.T, db *sql.DB, selector string, generation int, state, usedAt, expiresAt string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_backoffice_refresh_tokens
		(session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at)
		VALUES ('session-1', ?, X'03', ?, ?, '2026-09-23T00:00:00Z', ?, ?)`,
		selector, generation, state, usedAt, expiresAt,
	); err != nil {
		t.Fatalf("insert refresh %q: %v", selector, err)
	}
}

func assertSQLiteRejected(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), statement); err == nil {
		t.Fatalf("statement unexpectedly succeeded: %s", statement)
	}
}
