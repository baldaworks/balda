package backoffice

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/stretchr/testify/require"
)

func TestRecoveryRequiresConfirmationAndExistingEnrolledAdministrator(t *testing.T) {
	p, _ := newHTTPAppTestState(t)
	s, err := NewRecoveryService(p.Users())
	require.NoError(t, err)
	_, err = s.Recover(t.Context(), RecoveryInput{Username: "admin"})
	require.ErrorIs(t, err, usercmd.ErrInvalid)
	_, err = s.Recover(t.Context(), RecoveryInput{Username: "missing", Confirm: true})
	require.ErrorIs(t, err, usercmd.ErrNotFound)
	page, err := p.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, page.Users)
}

func TestRecoveryRevokesFamiliesPreservesPasswordAndRollsBackFailedAudit(t *testing.T) {
	p, _ := newHTTPAppTestState(t)
	store := p.Users()
	now := time.Now().UTC()
	u := createAccessTestUser(t, store, usercmd.User{ID: "recovery-admin", DisplayName: "Recovery", Username: "recovery", NormalizedUsername: "recovery", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	secret, found, err := store.GetCredentialSecret(t.Context(), u.ID)
	require.NoError(t, err)
	require.True(t, found)
	login, err := security.NewService(store, security.Config{AccessTTL: time.Minute, RefreshTTL: time.Hour})
	require.NoError(t, err)
	credentials, err := login.Login(t.Context(), u.Username, []byte("correct horse battery staple"))
	require.NoError(t, err)
	principal, err := login.ValidateAccess(t.Context(), credentials.AccessToken)
	require.NoError(t, err)
	family, _, err := store.GetSession(t.Context(), principal.FamilyID)
	require.NoError(t, err)
	family.ID = "enrolled-family"
	family.CredentialVersion++
	family.Access.Selector = "enrolled-access"
	family.RefreshTokens[0].Selector = "enrolled-refresh"
	family.WebAuthnVerifiedAt = family.CreatedAt
	family.MFAFactorID = "recovery-key"
	err = store.ApplyMFAChange(t.Context(), usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: 1, ExpectedCredentialVersion: 1, Purpose: usercmd.MFAEnable, BoundSessionID: principal.FamilyID, ExpectedSessionVersion: principal.Version, ChangedAt: now,
		Session:    &family,
		Credential: usercmd.MFACredential{ID: "recovery-key", UserID: u.ID, RPID: "localhost", CredentialID: []byte("credential"), PublicKey: []byte("public-key"), Data: []byte(`{}`), CreatedAt: now},
		Audit:      usercmd.AuditEvent{ID: "enable-recovery", Action: usercmd.AuditActionMFAEnabled, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: u.ID, Source: "test", OccurredAt: now}})
	require.NoError(t, err)
	s, err := NewRecoveryService(store)
	require.NoError(t, err)
	s.newID = func() string { return "enable-recovery" }
	_, err = s.Recover(t.Context(), RecoveryInput{Username: u.Username, Confirm: true})
	require.Error(t, err)
	profile, err := store.GetMFAProfile(t.Context(), u.ID)
	require.NoError(t, err)
	require.True(t, profile.Enabled)
	family, _, err = store.GetSession(t.Context(), "enrolled-family")
	require.NoError(t, err)
	require.True(t, family.RevokedAt.IsZero())
	s.store = staleRecoveryStore{store}
	_, err = s.Recover(t.Context(), RecoveryInput{Username: u.Username, Confirm: true})
	require.ErrorIs(t, err, usercmd.ErrConflict)
	s.store = store
	s.newID = func() string { return "recover-audit" }
	result, err := s.Recover(t.Context(), RecoveryInput{Username: u.Username, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, u.Username, result.Username)
	profile, err = store.GetMFAProfile(t.Context(), u.ID)
	require.NoError(t, err)
	require.False(t, profile.Enabled)
	current, _, err := store.GetCredentialSecret(t.Context(), u.ID)
	require.NoError(t, err)
	require.Equal(t, secret.PasswordHash, current.PasswordHash)
	family, _, err = store.GetSession(t.Context(), "enrolled-family")
	require.NoError(t, err)
	require.False(t, family.RevokedAt.IsZero())
	page, err := store.ListActiveSessions(t.Context(), u.ID, usercmd.PageRequest{Limit: 10}, time.Now())
	require.NoError(t, err)
	require.Empty(t, page.Sessions)
	_, err = s.Recover(t.Context(), RecoveryInput{Username: u.Username, Confirm: true})
	require.ErrorIs(t, err, usercmd.ErrInvalid)
	credentials, err = login.Login(t.Context(), u.Username, []byte("correct horse battery staple"))
	require.NoError(t, err)
	require.Nil(t, credentials.Pending)
	require.NotEmpty(t, credentials.AccessToken)
}

type staleRecoveryStore struct{ recoveryStore }

func (s staleRecoveryStore) GetUserByNormalizedUsername(ctx context.Context, username string) (usercmd.User, bool, error) {
	u, found, err := s.recoveryStore.GetUserByNormalizedUsername(ctx, username)
	u.Version--
	return u, found, err
}

func TestRecoveryRejectsIneligibleTargetsWithoutMutation(t *testing.T) {
	p, _ := newHTTPAppTestState(t)
	now := time.Now().UTC()
	for _, tc := range []struct {
		name     string
		role     usercmd.Role
		status   usercmd.UserStatus
		username string
		want     error
	}{
		{"disabled", usercmd.RoleAdministrator, usercmd.StatusDisabled, "disabled", usercmd.ErrForbidden},
		{"operator", usercmd.RoleOperator, usercmd.StatusActive, "operator", usercmd.ErrForbidden},
		{"normalized", usercmd.RoleAdministrator, usercmd.StatusActive, "Normalized", usercmd.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := createAccessTestUser(t, p.Users(), usercmd.User{ID: tc.name, DisplayName: tc.name, Username: tc.name, NormalizedUsername: tc.name, Role: tc.role, Status: tc.status, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
			s, err := NewRecoveryService(p.Users())
			require.NoError(t, err)
			_, err = s.Recover(t.Context(), RecoveryInput{Username: tc.username, Confirm: true})
			require.ErrorIs(t, err, tc.want)
			current, _, err := p.Users().GetUser(t.Context(), u.ID)
			require.NoError(t, err)
			require.Equal(t, u.Version, current.Version)
			require.Equal(t, u.Credential.Version, current.Credential.Version)
		})
	}
}
