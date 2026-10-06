package state

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func (s *sqlUserStore) GetMFAProfile(ctx context.Context, userID string) (usercmd.MFAProfile, error) {
	return s.mfaProfile(ctx, s.db, userID)
}

func (s *sqlUserStore) mfaProfile(ctx context.Context, q userQueryer, userID string) (usercmd.MFAProfile, error) {
	p := usercmd.MFAProfile{UserID: userID}
	var enabled int
	var factor sql.NullString
	err := q.QueryRowContext(ctx, s.bind(`SELECT enabled, version, active_factor_id FROM balda_mfa_profiles WHERE user_id = ?`), userID).
		Scan(&enabled, &p.Version, &factor)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, s.wrapError("load MFA profile", err)
	}
	p.Enabled = enabled == 1
	if !p.Enabled {
		return p, nil
	}
	c := &p.Credential
	var backupEligible, backupState int
	var created, used string
	err = q.QueryRowContext(ctx, s.bind(`SELECT factor_id, user_id, rp_id, credential_id, public_key, credential_data,
		sign_count, backup_eligible, backup_state, created_at, last_used_at
		FROM balda_mfa_credentials WHERE factor_id = ? AND user_id = ? AND invalidated_at = ''`), factor.String, userID).
		Scan(&c.ID, &c.UserID, &c.RPID, &c.CredentialID, &c.PublicKey, &c.Data, &c.SignCount,
			&backupEligible, &backupState, &created, &used)
	if err != nil {
		return p, s.wrapError("load active MFA credential", err)
	}
	c.BackupEligible, c.BackupState = backupEligible == 1, backupState == 1
	if c.CreatedAt, err = parseUserTime(created); err != nil {
		return p, s.wrapError("parse MFA creation", err)
	}
	if c.LastUsedAt, err = parseOptionalUserTime(used); err != nil {
		return p, s.wrapError("parse MFA usage", err)
	}
	return p, nil
}

// Lock the canonical user before reading factor authority. All factor mutations
// use this order, including the absent-profile case on first enrollment.
func (s *sqlUserStore) mfaAuthority(ctx context.Context, tx *sql.Tx, userID string, userVersion, credentialVersion, mfaVersion uint64) (usercmd.MFAProfile, error) {
	var role, status, credentialState string
	var uv, cv uint64
	err := tx.QueryRowContext(ctx, s.bind(`SELECT role, status, credential_state, version, credential_version FROM balda_users WHERE user_id = ?`)+s.forUpdate, userID).
		Scan(&role, &status, &credentialState, &uv, &cv)
	if errors.Is(err, sql.ErrNoRows) {
		return usercmd.MFAProfile{}, usercmd.ErrNotFound
	}
	if err != nil {
		return usercmd.MFAProfile{}, s.wrapError("lock MFA authority", err)
	}
	if role != string(usercmd.RoleAdministrator) || status != string(usercmd.StatusActive) || credentialState == string(usercmd.CredentialStateDisabled) {
		return usercmd.MFAProfile{}, usercmd.ErrForbidden
	}
	if uv != userVersion || cv != credentialVersion {
		return usercmd.MFAProfile{}, usercmd.ErrConflict
	}
	p, err := s.mfaProfile(ctx, tx, userID)
	if err != nil {
		return p, err
	}
	if p.Version != mfaVersion {
		return p, usercmd.ErrConflict
	}
	return p, nil
}

func (s *sqlUserStore) CreateMFACeremony(ctx context.Context, c usercmd.MFACeremony) error {
	if len(c.TokenDigest) != 32 || len(c.BrowserDigest) != 32 || len(c.CSRFDigest) != 32 ||
		len(c.Data) == 0 || len(c.Data) > 32768 || c.CreatedAt.IsZero() || !c.ExpiresAt.After(c.CreatedAt) ||
		c.ExpiresAt.Sub(c.CreatedAt) > 15*time.Minute || !c.Purpose.Valid() || c.Purpose == usercmd.MFARecover {
		return usercmd.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin MFA ceremony", err)
	}
	defer func() { _ = tx.Rollback() }()
	p, err := s.mfaAuthority(ctx, tx, c.UserID, c.UserVersion, c.CredentialVersion, c.MFAVersion)
	if err != nil {
		return err
	}
	if (c.Purpose == usercmd.MFAEnable) == p.Enabled || (c.Purpose != usercmd.MFALogin && c.SessionID == "") {
		return usercmd.ErrForbidden
	}
	if c.SessionID != "" {
		if _, err := s.liveMFASession(ctx, tx, c.SessionID, c.UserID, c.CredentialVersion, c.CreatedAt); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO balda_mfa_ceremonies
		(token_digest, browser_digest, csrf_digest, user_id, purpose, session_id, user_version, credential_version, mfa_version, ceremony_data, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), c.TokenDigest, c.BrowserDigest, c.CSRFDigest,
		c.UserID, c.Purpose, c.SessionID, c.UserVersion, c.CredentialVersion, c.MFAVersion, c.Data,
		formatMFATime(c.CreatedAt), formatMFATime(c.ExpiresAt))
	if err != nil {
		return s.mutationError("insert MFA ceremony", err)
	}
	return tx.Commit()
}

func (s *sqlUserStore) ConsumeMFACeremony(ctx context.Context, token, browser, csrf []byte, purpose usercmd.MFAPurpose, now time.Time) (usercmd.MFACeremony, error) {
	var c usercmd.MFACeremony
	if len(token) != 32 || len(browser) != 32 || len(csrf) != 32 || now.IsZero() {
		return c, usercmd.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return c, s.wrapError("begin consume MFA ceremony", err)
	}
	defer func() { _ = tx.Rollback() }()
	var created, expires, consumed string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT token_digest, browser_digest, csrf_digest, user_id, purpose, session_id,
		user_version, credential_version, mfa_version, ceremony_data, created_at, expires_at, consumed_at
		FROM balda_mfa_ceremonies WHERE token_digest = ?`), token).Scan(&c.TokenDigest, &c.BrowserDigest, &c.CSRFDigest,
		&c.UserID, &c.Purpose, &c.SessionID, &c.UserVersion, &c.CredentialVersion, &c.MFAVersion, &c.Data, &created, &expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return c, usercmd.ErrSessionUnavailable
	}
	if err != nil {
		return c, s.wrapError("load MFA ceremony", err)
	}
	if c.CreatedAt, err = parseUserTime(created); err != nil {
		return c, err
	}
	if c.ExpiresAt, err = parseUserTime(expires); err != nil {
		return c, err
	}
	if consumed != "" || now.Before(c.CreatedAt) || !now.Before(c.ExpiresAt) || c.Purpose != purpose ||
		subtle.ConstantTimeCompare(c.BrowserDigest, browser) != 1 || subtle.ConstantTimeCompare(c.CSRFDigest, csrf) != 1 {
		return c, usercmd.ErrSessionUnavailable
	}
	if _, err := s.mfaAuthority(ctx, tx, c.UserID, c.UserVersion, c.CredentialVersion, c.MFAVersion); err != nil {
		return c, err
	}
	if c.SessionID != "" {
		if _, err := s.liveMFASession(ctx, tx, c.SessionID, c.UserID, c.CredentialVersion, now); err != nil {
			return c, err
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_mfa_ceremonies SET consumed_at = ? WHERE token_digest = ? AND consumed_at = ''`), formatUserTime(now), token)
	if err != nil {
		return c, s.mutationError("consume MFA ceremony", err)
	}
	if err := requireOneMFAUpdate(result); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *sqlUserStore) DeleteExpiredMFACeremonies(ctx context.Context, before time.Time, limit int) (int, error) {
	if before.IsZero() || limit < 1 || limit > 1000 {
		return 0, usercmd.ErrInvalid
	}
	// Fixed-width UTC values make expiry ordering independent of fractional seconds.
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM balda_mfa_ceremonies WHERE token_digest IN
		(SELECT token_digest FROM balda_mfa_ceremonies WHERE expires_at <= ? ORDER BY expires_at LIMIT ?)`), formatMFATime(before), limit)
	if err != nil {
		return 0, s.wrapError("delete expired MFA ceremonies", err)
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func formatMFATime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func validMFACredential(c usercmd.MFACredential, userID string) bool {
	return c.ID != "" && c.UserID == userID && c.RPID != "" && len(c.CredentialID) > 0 && len(c.CredentialID) <= 1024 &&
		len(c.PublicKey) > 0 && len(c.PublicKey) <= 4096 && len(c.Data) > 0 && len(c.Data) <= 65536 &&
		!c.CreatedAt.IsZero() && (!c.BackupState || c.BackupEligible)
}

func (s *sqlUserStore) ApplyMFAChange(ctx context.Context, c usercmd.MFAChange) error {
	registration := c.Purpose == usercmd.MFAEnable || c.Purpose == usercmd.MFAReplace
	if (!registration && c.Purpose != usercmd.MFADisable && c.Purpose != usercmd.MFARecover) || c.ChangedAt.IsZero() ||
		(registration && (!validMFACredential(c.Credential, c.UserID) || c.Credential.CreatedAt.After(c.ChangedAt))) {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(c.Audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin MFA change", err)
	}
	defer func() { _ = tx.Rollback() }()
	p, err := s.mfaAuthority(ctx, tx, c.UserID, c.ExpectedUserVersion, c.ExpectedCredentialVersion, c.ExpectedMFAVersion)
	if err != nil {
		return err
	}
	if (c.Purpose == usercmd.MFAEnable) == p.Enabled {
		return usercmd.ErrConflict
	}
	if c.Purpose != usercmd.MFARecover {
		f, err := s.liveMFASession(ctx, tx, c.BoundSessionID, c.UserID, c.ExpectedCredentialVersion, c.ChangedAt)
		if err != nil {
			return err
		}
		if f.Version != c.ExpectedSessionVersion || f.Assurance != usercmd.SessionAssuranceNormal {
			return usercmd.ErrConflict
		}
	}
	if p.Enabled {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_mfa_credentials SET invalidated_at = ? WHERE factor_id = ? AND invalidated_at = ''`), formatUserTime(c.ChangedAt), p.Credential.ID); err != nil {
			return s.mutationError("invalidate MFA credential", err)
		}
	}
	var factor any
	if registration {
		key := c.Credential
		factor = key.ID
		_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO balda_mfa_credentials
			(factor_id, user_id, rp_id, credential_id, public_key, credential_data, sign_count, backup_eligible, backup_state, created_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), key.ID, c.UserID, key.RPID, key.CredentialID, key.PublicKey, key.Data,
			key.SignCount, boolInt(key.BackupEligible), boolInt(key.BackupState), formatUserTime(key.CreatedAt), formatOptionalUserTime(key.LastUsedAt))
		if err != nil {
			return s.mutationError("insert MFA credential", err)
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO balda_mfa_profiles (user_id, enabled, active_factor_id, version) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET enabled = excluded.enabled, active_factor_id = excluded.active_factor_id, version = excluded.version`),
		c.UserID, boolInt(registration), factor, p.Version+1)
	if err != nil {
		return s.mutationError("change MFA profile", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_users SET version = version + 1, credential_version = credential_version + 1, updated_at = ? WHERE user_id = ? AND version = ? AND credential_version = ?`),
		formatUserTime(c.ChangedAt), c.UserID, c.ExpectedUserVersion, c.ExpectedCredentialVersion)
	if err != nil {
		return s.mutationError("advance MFA authority", err)
	}
	if err := requireOneMFAUpdate(result); err != nil {
		return err
	}
	if err := s.revokeUserSessionsTx(ctx, tx, c.UserID, c.ChangedAt, "MFA changed"); err != nil {
		return err
	}
	if c.Session != nil {
		if c.Session.UserID != c.UserID || c.Session.CredentialVersion != c.ExpectedCredentialVersion+1 || c.Purpose == usercmd.MFARecover {
			return usercmd.ErrInvalid
		}
		if err := usercmd.ValidateSessionFamily(*c.Session); err != nil {
			return err
		}
		if err := s.insertSessionTx(ctx, tx, *c.Session); err != nil {
			return err
		}
	}
	if err := s.insertAudit(ctx, tx, c.Audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlUserStore) liveMFASession(ctx context.Context, tx *sql.Tx, sessionID, userID string, credentialVersion uint64, now time.Time) (usercmd.SessionFamily, error) {
	f, found, err := s.loadSessionFamily(ctx, tx, "session_id = ?"+s.forUpdate, sessionID)
	if err != nil {
		return f, err
	}
	if !found || f.UserID != userID || f.CredentialVersion != credentialVersion || !f.RevokedAt.IsZero() || !now.Before(f.RefreshExpiresAt) || !now.Before(f.Access.ExpiresAt) {
		return f, usercmd.ErrSessionUnavailable
	}
	return f, nil
}

func (s *sqlUserStore) VerifyMFACredential(ctx context.Context, v usercmd.MFAVerification) error {
	if v.VerifiedAt.IsZero() || !validMFACredential(v.Credential, v.UserID) {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(v.Audit); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin MFA verification", err)
	}
	defer func() { _ = tx.Rollback() }()
	p, err := s.mfaAuthority(ctx, tx, v.UserID, v.ExpectedUserVersion, v.ExpectedCredentialVersion, v.ExpectedMFAVersion)
	if err != nil {
		return err
	}
	old, key := p.Credential, v.Credential
	if !p.Enabled || old.ID != key.ID || old.SignCount != v.ExpectedSignCount || old.RPID != key.RPID ||
		old.BackupEligible != key.BackupEligible || !bytes.Equal(old.CredentialID, key.CredentialID) || !bytes.Equal(old.PublicKey, key.PublicKey) ||
		((key.SignCount != 0 || old.SignCount != 0) && key.SignCount <= old.SignCount) {
		return usercmd.ErrConflict
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_mfa_credentials SET credential_data = ?, sign_count = ?, backup_state = ?, last_used_at = ?
		WHERE factor_id = ? AND sign_count = ? AND invalidated_at = ''`), key.Data, key.SignCount, boolInt(key.BackupState), formatUserTime(v.VerifiedAt), key.ID, v.ExpectedSignCount)
	if err != nil {
		return s.mutationError("update verified MFA credential", err)
	}
	if err := requireOneMFAUpdate(result); err != nil {
		return err
	}
	if v.Session != nil {
		if v.Session.UserID != v.UserID || v.Session.CredentialVersion != v.ExpectedCredentialVersion ||
			v.Session.MFAFactorID != key.ID || !v.Session.WebAuthnVerifiedAt.Equal(v.VerifiedAt) {
			return usercmd.ErrInvalid
		}
		if err := usercmd.ValidateSessionFamily(*v.Session); err != nil {
			return err
		}
		if err := s.insertSessionTx(ctx, tx, *v.Session); err != nil {
			return err
		}
	}
	if err := s.insertAudit(ctx, tx, v.Audit); err != nil {
		return err
	}
	return tx.Commit()
}

func requireOneMFAUpdate(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return usercmd.ErrConflict
	}
	return nil
}

func (s *sqlUserStore) checkSessionMFA(ctx context.Context, tx *sql.Tx, f usercmd.SessionFamily) error {
	var role string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT role FROM balda_users WHERE user_id = ?`), f.UserID).Scan(&role); err != nil {
		return s.wrapError("load session MFA role", err)
	}
	if role != string(usercmd.RoleAdministrator) {
		return nil
	}
	p, err := s.mfaProfile(ctx, tx, f.UserID)
	if err != nil {
		return err
	}
	if p.Enabled && (f.MFAFactorID != p.Credential.ID || f.WebAuthnVerifiedAt.IsZero()) {
		return usercmd.ErrSessionUnavailable
	}
	return nil
}
