package state

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type sqlUserStore struct {
	db        *sql.DB
	bind      func(string) string
	begin     func(context.Context) (*sql.Tx, error)
	wrapError func(string, error) error
	forUpdate string
}

var _ usercmd.Store = (*sqlUserStore)(nil)

func newSQLiteUserStore(db *sql.DB) usercmd.Store {
	return &sqlUserStore{
		db: db,
		bind: func(query string) string {
			return query
		},
		begin: func(ctx context.Context) (*sql.Tx, error) {
			return db.BeginTx(ctx, nil)
		},
		wrapError: func(operation string, err error) error {
			return fmt.Errorf("%s: %w", operation, err)
		},
	}
}

func newPostgresUserStore(db *sql.DB) usercmd.Store {
	return &sqlUserStore{
		db:   db,
		bind: postgresBind,
		begin: func(ctx context.Context) (*sql.Tx, error) {
			return beginPostgresTx(db, ctx, nil)
		},
		wrapError: func(operation string, err error) error {
			return postgresErrorf("%s: %w", operation, err)
		},
		forUpdate: " FOR UPDATE",
	}
}

func (s *sqlUserStore) CreateUser(ctx context.Context, user usercmd.User, secret usercmd.CredentialSecret, audit usercmd.AuditEvent) error {
	if user.Binding != nil || secret.UserID != user.ID || strings.TrimSpace(secret.PasswordHash) == "" {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin create user", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_users (
			user_id, display_name, username, normalized_username, status, role,
			password_hash, credential_state, must_change, is_primary,
			credential_version, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		user.ID, user.DisplayName, user.Username, user.NormalizedUsername, user.Status, user.Role,
		secret.PasswordHash, user.Credential.State, boolInt(user.Credential.MustChange), boolInt(user.Primary),
		user.Credential.Version, user.Version, formatUserTime(user.CreatedAt), formatUserTime(user.UpdatedAt),
	)
	if err != nil {
		return s.mutationError("insert user", err)
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit create user", err)
	}
	return nil
}

func (s *sqlUserStore) UpdateUser(ctx context.Context, user usercmd.User, expectedVersion uint64, audit usercmd.AuditEvent) error {
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin update user", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentRole, currentStatus, credentialState string
	var currentVersion, credentialVersion uint64
	var mustChange int
	err = tx.QueryRowContext(ctx, s.bind(`
		SELECT role, status, version, credential_state, must_change, credential_version
		FROM balda_users WHERE user_id = ?`)+s.forUpdate, user.ID).
		Scan(&currentRole, &currentStatus, &currentVersion, &credentialState, &mustChange, &credentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.ErrNotFound
	}
	if err != nil {
		return s.wrapError("load user for update", err)
	}
	if currentVersion != expectedVersion {
		return usercmd.ErrConflict
	}
	if user.Version != expectedVersion+1 {
		return usercmd.ErrInvalid
	}
	if user.Credential.State != usercmd.CredentialState(credentialState) ||
		user.Credential.MustChange != intBool(mustChange) || user.Credential.Version != credentialVersion {
		return usercmd.ErrConflict
	}
	removesActiveAdministrator := currentRole == string(usercmd.RoleAdministrator) && currentStatus == string(usercmd.StatusActive) &&
		(user.Role != usercmd.RoleAdministrator || user.Status != usercmd.StatusActive)
	if removesActiveAdministrator {
		var others int
		if err := tx.QueryRowContext(ctx, s.bind(`
			SELECT COUNT(*) FROM balda_users
			WHERE user_id <> ? AND role = 'administrator' AND status = 'active'`), user.ID).Scan(&others); err != nil {
			return s.wrapError("count remaining administrators", err)
		}
		if others == 0 {
			return usercmd.ErrLastAdministrator
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_users SET
			display_name = ?, username = ?, normalized_username = ?, status = ?, role = ?,
			is_primary = ?, version = ?, updated_at = ?
		WHERE user_id = ? AND version = ?`),
		user.DisplayName, user.Username, user.NormalizedUsername, user.Status, user.Role,
		boolInt(user.Primary), user.Version, formatUserTime(user.UpdatedAt), user.ID, expectedVersion,
	)
	if err != nil {
		return s.mutationError("update user", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return s.wrapError("inspect updated user", err)
	} else if affected != 1 {
		return usercmd.ErrConflict
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit update user", err)
	}
	return nil
}

func (s *sqlUserStore) ChangeCredential(
	ctx context.Context,
	userID string,
	expectedUserVersion, expectedCredentialVersion uint64,
	credential usercmd.Credential,
	secret usercmd.CredentialSecret,
	revokedAt time.Time,
	audit usercmd.AuditEvent,
) error {
	if strings.TrimSpace(userID) == "" || secret.UserID != userID || strings.TrimSpace(secret.PasswordHash) == "" ||
		credential.Version != expectedCredentialVersion+1 || revokedAt.IsZero() || !credential.State.Valid() ||
		(credential.State == usercmd.CredentialStateTemporary) != credential.MustChange {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin credential change", err)
	}
	defer func() { _ = tx.Rollback() }()
	var userVersion, credentialVersion uint64
	err = tx.QueryRowContext(ctx, s.bind(`
		SELECT version, credential_version FROM balda_users WHERE user_id = ?`)+s.forUpdate, userID).
		Scan(&userVersion, &credentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.ErrNotFound
	}
	if err != nil {
		return s.wrapError("load user credential", err)
	}
	if userVersion != expectedUserVersion || credentialVersion != expectedCredentialVersion {
		return usercmd.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_users SET password_hash = ?, credential_state = ?, must_change = ?,
			credential_version = ?, version = version + 1, updated_at = ?
		WHERE user_id = ? AND version = ? AND credential_version = ?`),
		secret.PasswordHash, credential.State, boolInt(credential.MustChange), credential.Version,
		formatUserTime(revokedAt), userID, expectedUserVersion, expectedCredentialVersion,
	); err != nil {
		return s.mutationError("change credential", err)
	}
	if err := s.revokeUserSessionsTx(ctx, tx, userID, revokedAt, "credential changed"); err != nil {
		return err
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit credential change", err)
	}
	return nil
}

func (s *sqlUserStore) GetUser(ctx context.Context, userID string) (usercmd.User, bool, error) {
	return s.getUser(ctx, `u.user_id = ?`, userID)
}

func (s *sqlUserStore) GetUserByNormalizedUsername(ctx context.Context, normalizedUsername string) (usercmd.User, bool, error) {
	return s.getUser(ctx, `u.normalized_username = ?`, normalizedUsername)
}

func (s *sqlUserStore) GetUserByBinding(ctx context.Context, channelType, principal string) (usercmd.User, bool, error) {
	return s.getUser(ctx, `b.channel_type = ? AND b.principal = ?`, channelType, principal)
}

func (s *sqlUserStore) getUser(ctx context.Context, predicate string, args ...any) (usercmd.User, bool, error) {
	query := s.bind(userSelectSQL + " WHERE " + predicate)
	user, err := scanUser(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.User{}, false, nil
	}
	if err != nil {
		return usercmd.User{}, false, s.wrapError("get user", err)
	}
	return user, true, nil
}

func (s *sqlUserStore) GetCredentialSecret(ctx context.Context, userID string) (usercmd.CredentialSecret, bool, error) {
	var secret usercmd.CredentialSecret
	err := s.db.QueryRowContext(ctx, s.bind(`
		SELECT user_id, password_hash FROM balda_users WHERE user_id = ?`), userID).
		Scan(&secret.UserID, &secret.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.CredentialSecret{}, false, nil
	}
	if err != nil {
		return usercmd.CredentialSecret{}, false, s.wrapError("get credential secret", err)
	}
	return secret, true, nil
}

func (s *sqlUserStore) ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error) {
	limit, err := pageLimit(page.Limit)
	if err != nil {
		return usercmd.UserPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(userSelectSQL+`
		WHERE u.user_id > ? ORDER BY u.user_id LIMIT ?`), page.AfterID, limit+1)
	if err != nil {
		return usercmd.UserPage{}, s.wrapError("list users", err)
	}
	defer func() { _ = rows.Close() }()
	users := make([]usercmd.User, 0, limit+1)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return usercmd.UserPage{}, s.wrapError("scan listed user", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return usercmd.UserPage{}, s.wrapError("iterate users", err)
	}
	result := usercmd.UserPage{Users: users}
	if len(users) > limit {
		result.Users = users[:limit]
		result.NextAfterID = result.Users[len(result.Users)-1].ID
	}
	return result, nil
}

func (s *sqlUserStore) CreateBindingClaim(ctx context.Context, claim usercmd.BindingClaim, createdAt time.Time, audit usercmd.AuditEvent) error {
	if strings.TrimSpace(claim.ID) == "" || strings.TrimSpace(claim.UserID) == "" ||
		strings.TrimSpace(claim.ChannelType) == "" || createdAt.IsZero() || !claim.ExpiresAt.After(createdAt) || !claim.ConsumedAt.IsZero() {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin binding claim", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_user_binding_claims
			(claim_id, user_id, channel_type, expires_at, consumed_at, created_at)
		VALUES (?, ?, ?, ?, '', ?)`),
		claim.ID, claim.UserID, claim.ChannelType, formatUserTime(claim.ExpiresAt), formatUserTime(createdAt),
	); err != nil {
		return s.mutationError("create binding claim", err)
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit binding claim", err)
	}
	return nil
}

func (s *sqlUserStore) AttachBinding(ctx context.Context, claimID string, binding usercmd.Binding, consumedAt time.Time, audit usercmd.AuditEvent) error {
	if strings.TrimSpace(claimID) == "" || consumedAt.IsZero() || binding.UserID == "" || binding.ID == "" {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin attach binding", err)
	}
	defer func() { _ = tx.Rollback() }()
	var userID, channelType, expiresAt, alreadyConsumed string
	err = tx.QueryRowContext(ctx, s.bind(`
		SELECT user_id, channel_type, expires_at, consumed_at
		FROM balda_user_binding_claims WHERE claim_id = ?`)+s.forUpdate, claimID).
		Scan(&userID, &channelType, &expiresAt, &alreadyConsumed)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.ErrBindingClaimUnavailable
	}
	if err != nil {
		return s.wrapError("load binding claim", err)
	}
	expires, err := parseUserTime(expiresAt)
	if err != nil {
		return s.wrapError("parse binding claim expiry", err)
	}
	if alreadyConsumed != "" || !consumedAt.Before(expires) {
		return usercmd.ErrBindingClaimUnavailable
	}
	if userID != binding.UserID || channelType != binding.ChannelType {
		return usercmd.ErrBindingClaimScope
	}
	var count int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM balda_user_bindings WHERE user_id = ?`), binding.UserID).Scan(&count); err != nil {
		return s.wrapError("check user binding", err)
	}
	if count != 0 {
		return usercmd.ErrBindingAlreadyAssigned
	}
	if err := tx.QueryRowContext(ctx, s.bind(`
		SELECT COUNT(*) FROM balda_user_bindings WHERE channel_type = ? AND principal = ?`),
		binding.ChannelType, binding.Principal).Scan(&count); err != nil {
		return s.wrapError("check binding principal", err)
	}
	if count != 0 {
		return usercmd.ErrBindingPrincipalInUse
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_user_bindings
			(binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		binding.ID, binding.UserID, binding.ChannelType, binding.Principal, binding.DisplayName,
		binding.Provenance, formatUserTime(binding.CreatedAt), formatUserTime(binding.UpdatedAt),
	); err != nil {
		return s.mutationError("insert user binding", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_user_binding_claims SET consumed_at = ?
		WHERE claim_id = ? AND consumed_at = ''`), formatUserTime(consumedAt), claimID); err != nil {
		return s.mutationError("consume binding claim", err)
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit attach binding", err)
	}
	return nil
}

func (s *sqlUserStore) CreateSession(ctx context.Context, family usercmd.SessionFamily, audit usercmd.AuditEvent) error {
	if err := usercmd.ValidateSessionFamily(family); err != nil {
		return err
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin create session", err)
	}
	defer func() { _ = tx.Rollback() }()
	var status, credentialState string
	var credentialVersion uint64
	err = tx.QueryRowContext(ctx, s.bind(`
		SELECT status, credential_state, credential_version
		FROM balda_users WHERE user_id = ?`)+s.forUpdate, family.UserID).
		Scan(&status, &credentialState, &credentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.ErrNotFound
	}
	if err != nil {
		return s.wrapError("load session user", err)
	}
	if status != string(usercmd.StatusActive) || credentialState == string(usercmd.CredentialStateDisabled) ||
		credentialVersion != family.CredentialVersion {
		return usercmd.ErrSessionUnavailable
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_backoffice_sessions (
			session_id, user_id, assurance, credential_version,
			access_selector, access_verifier_digest, csrf_verifier_digest,
			created_at, last_seen_at, access_expires_at, refresh_expires_at,
			revoked_at, revocation_reason, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		family.ID, family.UserID, family.Assurance, family.CredentialVersion,
		family.Access.Selector, family.Access.VerifierDigest, family.CSRFVerifierDigest,
		formatUserTime(family.CreatedAt), formatUserTime(family.LastSeenAt), formatUserTime(family.Access.ExpiresAt),
		formatUserTime(family.RefreshExpiresAt), formatOptionalUserTime(family.RevokedAt), family.RevocationReason, family.Version,
	); err != nil {
		return s.mutationError("insert session family", err)
	}
	for _, token := range family.RefreshTokens {
		if err := s.insertRefreshToken(ctx, tx, family.ID, token); err != nil {
			return err
		}
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit create session", err)
	}
	return nil
}

func (s *sqlUserStore) GetSessionByAccessSelector(ctx context.Context, selector string) (usercmd.AccessSession, bool, error) {
	family, found, err := s.loadSessionFamily(ctx, s.db, `access_selector = ?`, selector)
	if err != nil || !found {
		return usercmd.AccessSession{}, found, err
	}
	user, found, err := s.GetUser(ctx, family.UserID)
	if err != nil || !found {
		if err == nil {
			err = usercmd.ErrNotFound
		}
		return usercmd.AccessSession{}, false, err
	}
	return usercmd.AccessSession{User: user, Family: family}, true, nil
}

func (s *sqlUserStore) GetSessionByRefreshSelector(ctx context.Context, selector string) (usercmd.RefreshSession, bool, error) {
	var sessionID string
	var token usercmd.RefreshToken
	var state, issuedAt, usedAt, expiresAt string
	err := s.db.QueryRowContext(ctx, s.bind(`
		SELECT session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at
		FROM balda_backoffice_refresh_tokens WHERE selector = ?`), selector).
		Scan(&sessionID, &token.Selector, &token.VerifierDigest, &token.Generation, &state, &issuedAt, &usedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.RefreshSession{}, false, nil
	}
	if err != nil {
		return usercmd.RefreshSession{}, false, s.wrapError("get refresh generation", err)
	}
	if err := scanRefreshTimes(&token, state, issuedAt, usedAt, expiresAt); err != nil {
		return usercmd.RefreshSession{}, false, s.wrapError("parse refresh generation", err)
	}
	family, found, err := s.loadSessionFamily(ctx, s.db, `session_id = ?`, sessionID)
	if err != nil || !found {
		return usercmd.RefreshSession{}, false, err
	}
	user, found, err := s.GetUser(ctx, family.UserID)
	if err != nil || !found {
		if err == nil {
			err = usercmd.ErrNotFound
		}
		return usercmd.RefreshSession{}, false, err
	}
	return usercmd.RefreshSession{User: user, Family: family, Token: token}, true, nil
}

func (s *sqlUserStore) ListSessions(ctx context.Context, userID string, page usercmd.PageRequest) (usercmd.SessionPage, error) {
	limit, err := pageLimit(page.Limit)
	if err != nil {
		return usercmd.SessionPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT session_id, assurance, created_at, last_seen_at, refresh_expires_at, revoked_at
		FROM balda_backoffice_sessions
		WHERE user_id = ? AND session_id > ? ORDER BY session_id LIMIT ?`), userID, page.AfterID, limit+1)
	if err != nil {
		return usercmd.SessionPage{}, s.wrapError("list user sessions", err)
	}
	defer func() { _ = rows.Close() }()
	summaries := make([]usercmd.SessionSummary, 0, limit+1)
	for rows.Next() {
		var summary usercmd.SessionSummary
		var assurance, createdAt, lastSeenAt, expiresAt, revokedAt string
		if err := rows.Scan(&summary.ID, &assurance, &createdAt, &lastSeenAt, &expiresAt, &revokedAt); err != nil {
			return usercmd.SessionPage{}, s.wrapError("scan user session", err)
		}
		summary.Assurance = usercmd.SessionAssurance(assurance)
		if summary.CreatedAt, err = parseUserTime(createdAt); err != nil {
			return usercmd.SessionPage{}, s.wrapError("parse session created time", err)
		}
		if summary.LastSeenAt, err = parseUserTime(lastSeenAt); err != nil {
			return usercmd.SessionPage{}, s.wrapError("parse session last seen time", err)
		}
		if summary.ExpiresAt, err = parseUserTime(expiresAt); err != nil {
			return usercmd.SessionPage{}, s.wrapError("parse session expiry", err)
		}
		if summary.RevokedAt, err = parseOptionalUserTime(revokedAt); err != nil {
			return usercmd.SessionPage{}, s.wrapError("parse session revocation", err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return usercmd.SessionPage{}, s.wrapError("iterate user sessions", err)
	}
	result := usercmd.SessionPage{Sessions: summaries}
	if len(summaries) > limit {
		result.Sessions = summaries[:limit]
		result.NextAfterID = result.Sessions[len(result.Sessions)-1].ID
	}
	return result, nil
}

func (s *sqlUserStore) RotateRefresh(ctx context.Context, rotation usercmd.RefreshRotation) (usercmd.RefreshRotationResult, error) {
	if strings.TrimSpace(rotation.Selector) == "" || len(rotation.PresentedVerifierDigest) == 0 || rotation.RotatedAt.IsZero() {
		return "", usercmd.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return "", s.wrapError("begin refresh rotation", err)
	}
	defer func() { _ = tx.Rollback() }()

	loaded, found, err := s.loadRefreshForRotation(ctx, tx, rotation.Selector)
	if err != nil {
		return "", err
	}
	if !found || subtle.ConstantTimeCompare(loaded.token.VerifierDigest, rotation.PresentedVerifierDigest) != 1 {
		if err := tx.Commit(); err != nil {
			return "", s.wrapError("commit unavailable refresh lookup", err)
		}
		return usercmd.RefreshRotationUnavailable, nil
	}
	if loaded.token.State == usercmd.RefreshTokenStateUsed && loaded.family.RevokedAt.IsZero() {
		if err := usercmd.ValidateAuditEvent(rotation.ReplayAudit); err != nil {
			return "", err
		}
		if err := s.revokeFamilyTx(ctx, tx, loaded.family.ID, rotation.RotatedAt, "refresh replay"); err != nil {
			return "", err
		}
		if err := s.insertAudit(ctx, tx, rotation.ReplayAudit); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", s.wrapError("commit refresh replay revocation", err)
		}
		return usercmd.RefreshRotationReplayRevoked, nil
	}
	if loaded.token.State != usercmd.RefreshTokenStateActive || !loaded.family.RevokedAt.IsZero() ||
		!rotation.RotatedAt.Before(loaded.family.RefreshExpiresAt) || loaded.userStatus != usercmd.StatusActive ||
		loaded.credentialState == usercmd.CredentialStateDisabled || loaded.userCredentialVersion != loaded.family.CredentialVersion {
		if err := tx.Commit(); err != nil {
			return "", s.wrapError("commit unavailable refresh state", err)
		}
		return usercmd.RefreshRotationUnavailable, nil
	}
	if err := validateRotation(rotation, loaded); err != nil {
		return "", err
	}
	if err := usercmd.ValidateAuditEvent(rotation.SuccessAudit); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_backoffice_refresh_tokens
		SET state = 'used', used_at = ?
		WHERE session_id = ? AND generation = ? AND state = 'active'`),
		formatUserTime(rotation.RotatedAt), loaded.family.ID, loaded.token.Generation,
	); err != nil {
		return "", s.mutationError("consume refresh generation", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_backoffice_sessions SET
			access_selector = ?, access_verifier_digest = ?, access_expires_at = ?,
			last_seen_at = ?, version = version + 1
		WHERE session_id = ? AND version = ? AND revoked_at = ''`),
		rotation.Access.Selector, rotation.Access.VerifierDigest, formatUserTime(rotation.Access.ExpiresAt),
		formatUserTime(rotation.RotatedAt), loaded.family.ID, loaded.family.Version,
	)
	if err != nil {
		return "", s.mutationError("replace access credential", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return "", s.wrapError("inspect access replacement", err)
	} else if affected != 1 {
		return "", usercmd.ErrConflict
	}
	if err := s.insertRefreshToken(ctx, tx, loaded.family.ID, rotation.Refresh); err != nil {
		return "", err
	}
	if err := s.insertAudit(ctx, tx, rotation.SuccessAudit); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", s.wrapError("commit refresh rotation", err)
	}
	return usercmd.RefreshRotationSucceeded, nil
}

func (s *sqlUserStore) RevokeSession(ctx context.Context, sessionID string, expectedVersion uint64, revokedAt time.Time, reason string, audit usercmd.AuditEvent) error {
	if strings.TrimSpace(sessionID) == "" || expectedVersion == 0 || revokedAt.IsZero() || strings.TrimSpace(reason) == "" {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin session revocation", err)
	}
	defer func() { _ = tx.Rollback() }()
	var version uint64
	err = tx.QueryRowContext(ctx, s.bind(`
		SELECT version FROM balda_backoffice_sessions WHERE session_id = ?`)+s.forUpdate, sessionID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.ErrNotFound
	}
	if err != nil {
		return s.wrapError("load session for revocation", err)
	}
	if version != expectedVersion {
		return usercmd.ErrConflict
	}
	if err := s.revokeFamilyTx(ctx, tx, sessionID, revokedAt, reason); err != nil {
		return err
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit session revocation", err)
	}
	return nil
}

func (s *sqlUserStore) RevokeUserSessions(ctx context.Context, userID string, revokedAt time.Time, reason string, audit usercmd.AuditEvent) error {
	if strings.TrimSpace(userID) == "" || revokedAt.IsZero() || strings.TrimSpace(reason) == "" {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin user session revocation", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM balda_users WHERE user_id = ?`), userID).Scan(&exists); err != nil {
		return s.wrapError("check session user", err)
	}
	if exists == 0 {
		return usercmd.ErrNotFound
	}
	if err := s.revokeUserSessionsTx(ctx, tx, userID, revokedAt, reason); err != nil {
		return err
	}
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit user session revocation", err)
	}
	return nil
}

func (s *sqlUserStore) DeleteExpiredSessions(ctx context.Context, before time.Time, limit int) (int, error) {
	if before.IsZero() || limit <= 0 || limit > usercmd.MaxPageSize {
		return 0, usercmd.ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, s.bind(`
		DELETE FROM balda_backoffice_sessions
		WHERE session_id IN (
			SELECT session_id FROM balda_backoffice_sessions
			WHERE refresh_expires_at < ? OR (revoked_at <> '' AND revoked_at < ?)
			ORDER BY session_id LIMIT ?
		)`), formatUserTime(before), formatUserTime(before), limit)
	if err != nil {
		return 0, s.wrapError("delete expired sessions", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, s.wrapError("inspect deleted sessions", err)
	}
	return int(count), nil
}

func (s *sqlUserStore) ListAuditEvents(ctx context.Context, page usercmd.PageRequest) (usercmd.AuditPage, error) {
	limit, err := pageLimit(page.Limit)
	if err != nil {
		return usercmd.AuditPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT event_id, action, outcome, actor_user_id, actor_session_id,
			target_type, target_id, reason, request_id, source, correlation_id, occurred_at
		FROM balda_security_audit_events
		WHERE event_id > ? ORDER BY event_id LIMIT ?`), page.AfterID, limit+1)
	if err != nil {
		return usercmd.AuditPage{}, s.wrapError("list audit events", err)
	}
	defer func() { _ = rows.Close() }()
	events := make([]usercmd.AuditEvent, 0, limit+1)
	for rows.Next() {
		var event usercmd.AuditEvent
		var action, outcome, targetType, occurredAt string
		if err := rows.Scan(
			&event.ID, &action, &outcome, &event.ActorUserID, &event.ActorSessionID,
			&targetType, &event.TargetID, &event.Reason, &event.RequestID, &event.Source,
			&event.CorrelationID, &occurredAt,
		); err != nil {
			return usercmd.AuditPage{}, s.wrapError("scan audit event", err)
		}
		event.Action = usercmd.AuditAction(action)
		event.Outcome = usercmd.AuditOutcome(outcome)
		event.TargetType = usercmd.AuditTargetType(targetType)
		if event.OccurredAt, err = parseUserTime(occurredAt); err != nil {
			return usercmd.AuditPage{}, s.wrapError("parse audit occurrence", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return usercmd.AuditPage{}, s.wrapError("iterate audit events", err)
	}
	result := usercmd.AuditPage{Events: events}
	if len(events) > limit {
		result.Events = events[:limit]
		result.NextAfterID = result.Events[len(result.Events)-1].ID
	}
	return result, nil
}

const userSelectSQL = `
	SELECT
		u.user_id, u.display_name, u.username, u.normalized_username, u.status, u.role,
		u.credential_state, u.must_change, u.credential_version, u.is_primary, u.version,
		u.created_at, u.updated_at,
		b.binding_id, b.user_id, b.channel_type, b.principal, b.display_name,
		b.provenance, b.created_at, b.updated_at
	FROM balda_users u
	LEFT JOIN balda_user_bindings b ON b.user_id = u.user_id`

type userRowScanner interface {
	Scan(dest ...any) error
}

type userQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanUser(scanner userRowScanner) (usercmd.User, error) {
	var user usercmd.User
	var status, role, credentialState, createdAt, updatedAt string
	var mustChange, primary int
	var bindingID, bindingUserID, channelType, principal, bindingDisplayName sql.NullString
	var provenance, bindingCreatedAt, bindingUpdatedAt sql.NullString
	err := scanner.Scan(
		&user.ID, &user.DisplayName, &user.Username, &user.NormalizedUsername, &status, &role,
		&credentialState, &mustChange, &user.Credential.Version, &primary, &user.Version,
		&createdAt, &updatedAt,
		&bindingID, &bindingUserID, &channelType, &principal, &bindingDisplayName,
		&provenance, &bindingCreatedAt, &bindingUpdatedAt,
	)
	if err != nil {
		return usercmd.User{}, err
	}
	user.Status = usercmd.UserStatus(status)
	user.Role = usercmd.Role(role)
	user.Credential.State = usercmd.CredentialState(credentialState)
	user.Credential.MustChange = intBool(mustChange)
	user.Primary = intBool(primary)
	if user.CreatedAt, err = parseUserTime(createdAt); err != nil {
		return usercmd.User{}, err
	}
	if user.UpdatedAt, err = parseUserTime(updatedAt); err != nil {
		return usercmd.User{}, err
	}
	if bindingID.Valid {
		binding := usercmd.Binding{
			ID: bindingID.String, UserID: bindingUserID.String, ChannelType: channelType.String,
			Principal: principal.String, DisplayName: bindingDisplayName.String, Provenance: provenance.String,
		}
		if binding.CreatedAt, err = parseUserTime(bindingCreatedAt.String); err != nil {
			return usercmd.User{}, err
		}
		if binding.UpdatedAt, err = parseUserTime(bindingUpdatedAt.String); err != nil {
			return usercmd.User{}, err
		}
		user.Binding = &binding
	}
	return user, nil
}

func (s *sqlUserStore) insertAudit(ctx context.Context, tx *sql.Tx, audit usercmd.AuditEvent) error {
	_, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_security_audit_events (
			event_id, actor_user_id, actor_session_id, action, target_type, target_id,
			outcome, reason, metadata_json, request_id, source, correlation_id, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		audit.ID, audit.ActorUserID, audit.ActorSessionID, audit.Action, audit.TargetType, audit.TargetID,
		audit.Outcome, audit.Reason, "{}", audit.RequestID, audit.Source, audit.CorrelationID,
		formatUserTime(audit.OccurredAt),
	)
	if err != nil {
		return s.mutationError("insert security audit", err)
	}
	return nil
}

func (s *sqlUserStore) insertRefreshToken(ctx context.Context, tx *sql.Tx, sessionID string, token usercmd.RefreshToken) error {
	_, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_backoffice_refresh_tokens (
			session_id, selector, verifier_digest, generation, state, issued_at, used_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		sessionID, token.Selector, token.VerifierDigest, token.Generation, token.State,
		formatUserTime(token.IssuedAt), formatOptionalUserTime(token.UsedAt), formatUserTime(token.ExpiresAt),
	)
	if err != nil {
		return s.mutationError("insert refresh generation", err)
	}
	return nil
}

func (s *sqlUserStore) loadSessionFamily(ctx context.Context, q userQueryer, predicate string, args ...any) (usercmd.SessionFamily, bool, error) {
	var family usercmd.SessionFamily
	var assurance, createdAt, lastSeenAt, accessExpiresAt, refreshExpiresAt, revokedAt string
	err := q.QueryRowContext(ctx, s.bind(`
		SELECT session_id, user_id, assurance, credential_version,
			access_selector, access_verifier_digest, csrf_verifier_digest,
			created_at, last_seen_at, access_expires_at, refresh_expires_at,
			revoked_at, revocation_reason, version
		FROM balda_backoffice_sessions WHERE `+predicate), args...).Scan(
		&family.ID, &family.UserID, &assurance, &family.CredentialVersion,
		&family.Access.Selector, &family.Access.VerifierDigest, &family.CSRFVerifierDigest,
		&createdAt, &lastSeenAt, &accessExpiresAt, &refreshExpiresAt,
		&revokedAt, &family.RevocationReason, &family.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.SessionFamily{}, false, nil
	}
	if err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("load session family", err)
	}
	family.Assurance = usercmd.SessionAssurance(assurance)
	if family.CreatedAt, err = parseUserTime(createdAt); err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("parse session creation", err)
	}
	if family.LastSeenAt, err = parseUserTime(lastSeenAt); err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("parse session last seen", err)
	}
	if family.Access.ExpiresAt, err = parseUserTime(accessExpiresAt); err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("parse access expiry", err)
	}
	if family.RefreshExpiresAt, err = parseUserTime(refreshExpiresAt); err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("parse refresh expiry", err)
	}
	if family.RevokedAt, err = parseOptionalUserTime(revokedAt); err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("parse session revocation", err)
	}
	rows, err := q.QueryContext(ctx, s.bind(`
		SELECT selector, verifier_digest, generation, state, issued_at, used_at, expires_at
		FROM balda_backoffice_refresh_tokens
		WHERE session_id = ? ORDER BY generation`), family.ID)
	if err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("load refresh lineage", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var token usercmd.RefreshToken
		var state, issuedAt, usedAt, expiresAt string
		if err := rows.Scan(&token.Selector, &token.VerifierDigest, &token.Generation, &state, &issuedAt, &usedAt, &expiresAt); err != nil {
			return usercmd.SessionFamily{}, false, s.wrapError("scan refresh lineage", err)
		}
		if err := scanRefreshTimes(&token, state, issuedAt, usedAt, expiresAt); err != nil {
			return usercmd.SessionFamily{}, false, s.wrapError("parse refresh lineage", err)
		}
		family.RefreshTokens = append(family.RefreshTokens, token)
	}
	if err := rows.Err(); err != nil {
		return usercmd.SessionFamily{}, false, s.wrapError("iterate refresh lineage", err)
	}
	return family, true, nil
}

type refreshRotationState struct {
	token                 usercmd.RefreshToken
	family                usercmd.SessionFamily
	userStatus            usercmd.UserStatus
	credentialState       usercmd.CredentialState
	userCredentialVersion uint64
}

func (s *sqlUserStore) loadRefreshForRotation(ctx context.Context, tx *sql.Tx, selector string) (refreshRotationState, bool, error) {
	var loaded refreshRotationState
	var tokenState, issuedAt, usedAt, tokenExpiresAt string
	var assurance, createdAt, lastSeenAt, accessExpiresAt, refreshExpiresAt, revokedAt string
	var userStatus, credentialState string
	err := tx.QueryRowContext(ctx, s.bind(`
		SELECT
			r.selector, r.verifier_digest, r.generation, r.state, r.issued_at, r.used_at, r.expires_at,
			s.session_id, s.user_id, s.assurance, s.credential_version,
			s.access_selector, s.access_verifier_digest, s.csrf_verifier_digest,
			s.created_at, s.last_seen_at, s.access_expires_at, s.refresh_expires_at,
			s.revoked_at, s.revocation_reason, s.version,
			u.status, u.credential_state, u.credential_version
		FROM balda_backoffice_refresh_tokens r
		JOIN balda_backoffice_sessions s ON s.session_id = r.session_id
		JOIN balda_users u ON u.user_id = s.user_id
		WHERE r.selector = ?`)+s.forUpdate, selector).Scan(
		&loaded.token.Selector, &loaded.token.VerifierDigest, &loaded.token.Generation,
		&tokenState, &issuedAt, &usedAt, &tokenExpiresAt,
		&loaded.family.ID, &loaded.family.UserID, &assurance, &loaded.family.CredentialVersion,
		&loaded.family.Access.Selector, &loaded.family.Access.VerifierDigest, &loaded.family.CSRFVerifierDigest,
		&createdAt, &lastSeenAt, &accessExpiresAt, &refreshExpiresAt,
		&revokedAt, &loaded.family.RevocationReason, &loaded.family.Version,
		&userStatus, &credentialState, &loaded.userCredentialVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return refreshRotationState{}, false, nil
	}
	if err != nil {
		return refreshRotationState{}, false, s.wrapError("load refresh rotation state", err)
	}
	loaded.family.Assurance = usercmd.SessionAssurance(assurance)
	loaded.userStatus = usercmd.UserStatus(userStatus)
	loaded.credentialState = usercmd.CredentialState(credentialState)
	if err := scanRefreshTimes(&loaded.token, tokenState, issuedAt, usedAt, tokenExpiresAt); err != nil {
		return refreshRotationState{}, false, s.wrapError("parse refresh rotation generation", err)
	}
	if loaded.family.CreatedAt, err = parseUserTime(createdAt); err != nil {
		return refreshRotationState{}, false, s.wrapError("parse rotation session creation", err)
	}
	if loaded.family.LastSeenAt, err = parseUserTime(lastSeenAt); err != nil {
		return refreshRotationState{}, false, s.wrapError("parse rotation session last seen", err)
	}
	if loaded.family.Access.ExpiresAt, err = parseUserTime(accessExpiresAt); err != nil {
		return refreshRotationState{}, false, s.wrapError("parse rotation access expiry", err)
	}
	if loaded.family.RefreshExpiresAt, err = parseUserTime(refreshExpiresAt); err != nil {
		return refreshRotationState{}, false, s.wrapError("parse rotation refresh expiry", err)
	}
	if loaded.family.RevokedAt, err = parseOptionalUserTime(revokedAt); err != nil {
		return refreshRotationState{}, false, s.wrapError("parse rotation revocation", err)
	}
	return loaded, true, nil
}

func validateRotation(rotation usercmd.RefreshRotation, loaded refreshRotationState) error {
	if strings.TrimSpace(rotation.Access.Selector) == "" || len(rotation.Access.VerifierDigest) == 0 ||
		!rotation.Access.ExpiresAt.After(rotation.RotatedAt) || !rotation.Access.ExpiresAt.Before(loaded.family.RefreshExpiresAt) ||
		strings.TrimSpace(rotation.Refresh.Selector) == "" || len(rotation.Refresh.VerifierDigest) == 0 ||
		rotation.Refresh.Generation != loaded.token.Generation+1 || rotation.Refresh.State != usercmd.RefreshTokenStateActive ||
		!rotation.Refresh.UsedAt.IsZero() || !rotation.Refresh.IssuedAt.Equal(rotation.RotatedAt) ||
		!rotation.Refresh.ExpiresAt.Equal(loaded.family.RefreshExpiresAt) {
		return usercmd.ErrInvalid
	}
	return nil
}

func (s *sqlUserStore) revokeFamilyTx(ctx context.Context, tx *sql.Tx, sessionID string, revokedAt time.Time, reason string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_backoffice_refresh_tokens SET state = 'revoked'
		WHERE session_id = ? AND state = 'active'`), sessionID); err != nil {
		return s.mutationError("revoke refresh generations", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_backoffice_sessions
		SET revoked_at = ?, revocation_reason = ?, version = version + 1
		WHERE session_id = ? AND revoked_at = ''`),
		formatUserTime(revokedAt), reason, sessionID,
	); err != nil {
		return s.mutationError("revoke session family", err)
	}
	return nil
}

func (s *sqlUserStore) revokeUserSessionsTx(ctx context.Context, tx *sql.Tx, userID string, revokedAt time.Time, reason string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_backoffice_refresh_tokens SET state = 'revoked'
		WHERE state = 'active' AND session_id IN (
			SELECT session_id FROM balda_backoffice_sessions WHERE user_id = ? AND revoked_at = ''
		)`), userID); err != nil {
		return s.mutationError("revoke user refresh generations", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE balda_backoffice_sessions
		SET revoked_at = ?, revocation_reason = ?, version = version + 1
		WHERE user_id = ? AND revoked_at = ''`),
		formatUserTime(revokedAt), reason, userID,
	); err != nil {
		return s.mutationError("revoke user session families", err)
	}
	return nil
}

func scanRefreshTimes(token *usercmd.RefreshToken, state, issuedAt, usedAt, expiresAt string) error {
	var err error
	token.State = usercmd.RefreshTokenState(state)
	if token.IssuedAt, err = parseUserTime(issuedAt); err != nil {
		return err
	}
	if token.UsedAt, err = parseOptionalUserTime(usedAt); err != nil {
		return err
	}
	if token.ExpiresAt, err = parseUserTime(expiresAt); err != nil {
		return err
	}
	return nil
}

func (s *sqlUserStore) mutationError(operation string, err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "constraint") || strings.Contains(message, "duplicate key") || strings.Contains(message, "unique") {
		return usercmd.ErrConflict
	}
	return s.wrapError(operation, err)
}

func pageLimit(limit int) (int, error) {
	if limit <= 0 || limit > usercmd.MaxPageSize {
		return 0, usercmd.ErrInvalid
	}
	return limit, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intBool(value int) bool {
	return value != 0
}

func formatUserTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalUserTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatUserTime(value)
}

func parseUserTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseOptionalUserTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return parseUserTime(value)
}
