package state

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

var _ usercmd.InvitationStore = (*sqlUserStore)(nil)

const invitationColumns = `invitation_id, user_id, channel_type, integration_key, token_digest,
issued_by, created_at, expires_at, consumed_at, revoked_at, revocation_reason, version`

type invitationUser struct {
	role    usercmd.Role
	status  usercmd.UserStatus
	version uint64
}

func (s *sqlUserStore) lockInvitationUser(ctx context.Context, tx *sql.Tx, id string) (invitationUser, error) {
	var user invitationUser
	err := tx.QueryRowContext(ctx, s.bind(`SELECT role, status, version FROM balda_users WHERE user_id = ?`)+s.forUpdate, id).
		Scan(&user.role, &user.status, &user.version)
	if errors.Is(err, sql.ErrNoRows) {
		return user, usercmd.ErrNotFound
	}
	if err != nil {
		return user, s.wrapError("lock invitation user", err)
	}
	return user, nil
}

func (s *sqlUserStore) lockInvitationActors(ctx context.Context, tx *sql.Tx, actorID, targetID string) (invitationUser, error) {
	ids := []string{actorID, targetID}
	sort.Strings(ids)
	locked := make(map[string]invitationUser, 2)
	for _, id := range ids {
		if _, ok := locked[id]; ok {
			continue
		}
		user, err := s.lockInvitationUser(ctx, tx, id)
		if err != nil {
			return invitationUser{}, err
		}
		locked[id] = user
	}
	actor := locked[actorID]
	if actor.status != usercmd.StatusActive || actor.role != usercmd.RoleAdministrator {
		return invitationUser{}, usercmd.ErrForbidden
	}
	target := locked[targetID]
	if target.status != usercmd.StatusActive {
		return invitationUser{}, usercmd.ErrAuthenticationDisabled
	}
	return target, nil
}

func (s *sqlUserStore) IssueBindingInvitation(ctx context.Context, change usercmd.InvitationIssue) error {
	i := change.Invitation
	if i.ID == "" || i.UserID == "" || i.IssuedBy == "" || strings.TrimSpace(i.Integration.Key) == "" ||
		len(i.TokenDigest) != 32 || i.CreatedAt.IsZero() || !i.ExpiresAt.After(i.CreatedAt) ||
		!i.ConsumedAt.IsZero() || !i.RevokedAt.IsZero() || i.RevocationReason != "" || i.Version != 1 || change.ExpectedUserVersion == 0 {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(change.Audit); err != nil {
		return err
	}
	if change.Audit.ActorUserID != i.IssuedBy || change.Audit.TargetID != i.UserID || change.Audit.Action != usercmd.AuditActionInvitationIssued {
		return usercmd.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin invitation issuance", err)
	}
	defer func() { _ = tx.Rollback() }()
	target, err := s.lockInvitationActors(ctx, tx, i.IssuedBy, i.UserID)
	if err != nil {
		return err
	}
	if target.version != change.ExpectedUserVersion {
		return usercmd.ErrConflict
	}
	current, found, err := s.loadInvitation(ctx, tx, `user_id = ? AND channel_type = ? AND integration_key = ? AND consumed_at = '' AND revoked_at = ''`, true, i.UserID, i.Integration.ChannelType, i.Integration.Key)
	if err != nil {
		return err
	}
	if found {
		if !change.Replace && i.CreatedAt.Before(current.ExpiresAt) {
			return usercmd.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_binding_invitations SET revoked_at = ?, revocation_reason = 'replaced', version = version + 1 WHERE invitation_id = ?`), formatUserTime(i.CreatedAt), current.ID); err != nil {
			return s.mutationError("replace invitation", err)
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO balda_binding_invitations
        (invitation_id, user_id, channel_type, integration_key, token_digest, issued_by, created_at, expires_at, version)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`), i.ID, i.UserID, i.Integration.ChannelType, i.Integration.Key,
		i.TokenDigest, i.IssuedBy, formatUserTime(i.CreatedAt), formatUserTime(i.ExpiresAt), i.Version); err != nil {
		return s.mutationError("insert invitation", err)
	}
	if err := s.insertAudit(ctx, tx, change.Audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit invitation issuance", err)
	}
	return nil
}

func (s *sqlUserStore) CancelBindingInvitation(ctx context.Context, change usercmd.InvitationCancel) error {
	if change.ID == "" || change.UserID == "" || change.ActorUserID == "" || change.ExpectedVersion == 0 || change.CancelledAt.IsZero() {
		return usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(change.Audit); err != nil {
		return err
	}
	if change.Audit.ActorUserID != change.ActorUserID || change.Audit.TargetID != change.UserID || change.Audit.Action != usercmd.AuditActionInvitationRevoked {
		return usercmd.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return s.wrapError("begin invitation cancellation", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.lockInvitationActors(ctx, tx, change.ActorUserID, change.UserID); err != nil {
		return err
	}
	i, found, err := s.loadInvitation(ctx, tx, `invitation_id = ? AND user_id = ?`, true, change.ID, change.UserID)
	if err != nil {
		return err
	}
	if !found || !i.ConsumedAt.IsZero() || !i.RevokedAt.IsZero() {
		return usercmd.ErrBindingInvitationUnavailable
	}
	if i.Version != change.ExpectedVersion {
		return usercmd.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_binding_invitations SET revoked_at = ?, revocation_reason = 'cancelled', version = version + 1 WHERE invitation_id = ?`), formatUserTime(change.CancelledAt), i.ID); err != nil {
		return s.mutationError("cancel invitation", err)
	}
	if err := s.insertAudit(ctx, tx, change.Audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.wrapError("commit invitation cancellation", err)
	}
	return nil
}

func (s *sqlUserStore) ConsumeBindingInvitation(ctx context.Context, change usercmd.InvitationConsume) (string, error) {
	if len(change.TokenDigest) != 32 || change.Binding.ID == "" || change.Binding.Principal == "" || change.ConsumedAt.IsZero() || change.Binding.ChannelType != change.Integration.ChannelType {
		return "", usercmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(change.Audit); err != nil {
		return "", err
	}
	if change.Audit.Action != usercmd.AuditActionBindingAttached || change.Audit.TargetID != change.Binding.ID {
		return "", usercmd.ErrInvalid
	}
	// Read the immutable target first, then lock the user before the invitation.
	// Issue, cancel and disable use the same lock order.
	i, found, err := s.loadInvitation(ctx, s.db, `token_digest = ?`, false, change.TokenDigest)
	if err != nil {
		return "", err
	}
	if !found {
		return "", usercmd.ErrBindingInvitationUnavailable
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return "", s.wrapError("begin invitation consumption", err)
	}
	defer func() { _ = tx.Rollback() }()
	user, err := s.lockInvitationUser(ctx, tx, i.UserID)
	if err != nil {
		return "", err
	}
	i, found, err = s.loadInvitation(ctx, tx, `token_digest = ?`, true, change.TokenDigest)
	if err != nil {
		return "", err
	}
	if !found || !i.ConsumedAt.IsZero() || !i.RevokedAt.IsZero() || !change.ConsumedAt.Before(i.ExpiresAt) {
		return "", usercmd.ErrBindingInvitationUnavailable
	}
	if i.Integration != change.Integration {
		return "", usercmd.ErrBindingInvitationScope
	}
	if user.status != usercmd.StatusActive {
		return "", usercmd.ErrAuthenticationDisabled
	}
	var count int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM balda_user_bindings WHERE channel_type = ? AND principal = ?`), change.Binding.ChannelType, change.Binding.Principal).Scan(&count); err != nil {
		return "", s.wrapError("check invitation principal", err)
	}
	if count != 0 {
		return "", usercmd.ErrBindingPrincipalInUse
	}
	b := change.Binding
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO balda_user_bindings
        (binding_id, user_id, channel_type, principal, display_name, provider_username, provider_first_name, provenance, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), b.ID, i.UserID, b.ChannelType, b.Principal, b.DisplayName,
		b.ProviderUsername, b.ProviderFirstName, b.Provenance, formatUserTime(change.ConsumedAt), formatUserTime(change.ConsumedAt)); err != nil {
		return "", s.mutationError("attach invitation binding", err)
	}
	if err := s.advanceBindingUserVersion(ctx, tx, i.UserID, user.version, change.ConsumedAt); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_binding_invitations SET consumed_at = ?, version = version + 1 WHERE invitation_id = ?`), formatUserTime(change.ConsumedAt), i.ID); err != nil {
		return "", s.mutationError("consume invitation", err)
	}
	audit := change.Audit
	audit.ActorUserID = i.UserID
	if err := s.insertAudit(ctx, tx, audit); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", s.wrapError("commit invitation binding", err)
	}
	return i.UserID, nil
}

func (s *sqlUserStore) ListBindingInvitations(ctx context.Context, userID string) ([]usercmd.BindingInvitation, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+invitationColumns+` FROM balda_binding_invitations WHERE user_id = ? AND consumed_at = '' AND revoked_at = '' ORDER BY channel_type, integration_key LIMIT ?`), userID, usercmd.MaxPageSize)
	if err != nil {
		return nil, s.wrapError("list binding invitations", err)
	}
	defer func() { _ = rows.Close() }()
	var invitations []usercmd.BindingInvitation
	for rows.Next() {
		i, err := scanInvitation(rows)
		if err != nil {
			return nil, s.wrapError("scan binding invitation", err)
		}
		invitations = append(invitations, i)
	}
	return invitations, rows.Err()
}

func (s *sqlUserStore) loadInvitation(ctx context.Context, q userQueryer, predicate string, lock bool, args ...any) (usercmd.BindingInvitation, bool, error) {
	query := s.bind(`SELECT ` + invitationColumns + ` FROM balda_binding_invitations WHERE ` + predicate)
	if lock {
		query += s.forUpdate
	}
	i, err := scanInvitation(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return i, false, nil
	}
	if err != nil {
		return i, false, s.wrapError("load binding invitation", err)
	}
	return i, true, nil
}

func scanInvitation(row interface{ Scan(dest ...any) error }) (usercmd.BindingInvitation, error) {
	var i usercmd.BindingInvitation
	var created, expires, consumed, revoked string
	if err := row.Scan(&i.ID, &i.UserID, &i.Integration.ChannelType, &i.Integration.Key, &i.TokenDigest,
		&i.IssuedBy, &created, &expires, &consumed, &revoked, &i.RevocationReason, &i.Version); err != nil {
		return i, err
	}
	for _, field := range []struct {
		text string
		dest *time.Time
	}{{created, &i.CreatedAt}, {expires, &i.ExpiresAt}, {consumed, &i.ConsumedAt}, {revoked, &i.RevokedAt}} {
		parsed, err := parseOptionalUserTime(field.text)
		if err != nil {
			return i, err
		}
		*field.dest = parsed
	}
	return i, nil
}
