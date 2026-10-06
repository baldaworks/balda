package security

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/stretchr/testify/require"
)

func TestEnableMFARequiresPasswordAndVerifiedRegistration(t *testing.T) {
	p, s, _ := newSecurityTestService(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	u := createSecurityTestUser(t, p.Users(), "enable-admin", "enable-admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	credentials, err := s.Login(t.Context(), u.Username, []byte(testPassword))
	require.NoError(t, err)
	s.webauthn, err = newWebAuthnEngine("https://example.org")
	require.NoError(t, err)
	_, err = s.BeginEnable(t.Context(), credentials.AccessToken, []byte("incorrect"), "browser", credentials.CSRFToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	start, err := s.BeginEnable(t.Context(), credentials.AccessToken, []byte(testPassword), "browser", credentials.CSRFToken)
	require.NoError(t, err)
	profile, err := p.Users().GetMFAProfile(t.Context(), u.ID)
	require.NoError(t, err)
	require.False(t, profile.Enabled)
	// A persistence failure after verification must leave both policy and the old family intact.
	newID := s.newID
	s.newID = func() string { return "create-enable-admin" }
	_, err = s.FinishEnable(t.Context(), credentials.AccessToken, start.Transaction, "browser", credentials.CSRFToken, registrationResponseForStart(t, start))
	require.Error(t, err)
	s.newID = newID
	profile, err = p.Users().GetMFAProfile(t.Context(), u.ID)
	require.NoError(t, err)
	require.False(t, profile.Enabled)
	_, err = s.ValidateAccess(t.Context(), credentials.AccessToken)
	require.NoError(t, err)
	start, err = s.BeginEnable(t.Context(), credentials.AccessToken, []byte(testPassword), "browser", credentials.CSRFToken)
	require.NoError(t, err)
	result, err := s.FinishEnable(t.Context(), credentials.AccessToken, start.Transaction, "browser", credentials.CSRFToken, registrationResponseForStart(t, start))
	require.NoError(t, err)
	require.NotEmpty(t, result.AccessToken)
	profile, err = p.Users().GetMFAProfile(t.Context(), u.ID)
	require.NoError(t, err)
	require.True(t, profile.Enabled)
	_, err = s.ValidateAccess(t.Context(), credentials.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestDisableMFARequiresPasswordAndConfirmation(t *testing.T) {
	p, s, u, private, key, _ := enrolledTestService(t)
	ctx := withMFABrowser(t.Context(), "login-browser", "csrf")
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	credentials, err := s.FinishLogin(ctx, pending.Pending.Transaction, "login-browser", "csrf", assertionResponseForStart(t, *pending.Pending, key, private, 0x05, 1))
	require.NoError(t, err)
	_, err = s.Disable(ctx, credentials.AccessToken, []byte(testPassword), false)
	require.ErrorIs(t, err, usercmd.ErrInvalid)
	_, err = s.Disable(ctx, credentials.AccessToken, []byte("incorrect"), true)
	require.ErrorIs(t, err, ErrUnauthenticated)
	result, err := s.Disable(ctx, credentials.AccessToken, []byte(testPassword), true)
	require.NoError(t, err)
	status, err := s.MFAStatus(ctx, result.AccessToken)
	require.NoError(t, err)
	require.False(t, status.Enabled)
	_, err = s.ValidateAccess(ctx, credentials.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	passwordOnly, err := s.Login(t.Context(), u.Username, []byte(testPassword))
	require.NoError(t, err)
	require.Nil(t, passwordOnly.Pending)
	require.NotEmpty(t, passwordOnly.AccessToken)
	profile, err := p.Users().GetMFAProfile(ctx, u.ID)
	require.NoError(t, err)
	require.False(t, profile.Enabled)
}

func TestReplaceMFARequiresPasswordConfirmationAndNewRegistration(t *testing.T) {
	p, s, u, private, key, _ := enrolledTestService(t)
	ctx := withMFABrowser(t.Context(), "login-browser", "csrf")
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	credentials, err := s.FinishLogin(ctx, pending.Pending.Transaction, "login-browser", "csrf", assertionResponseForStart(t, *pending.Pending, key, private, 0x05, 1))
	require.NoError(t, err)
	_, err = s.BeginReplace(ctx, credentials.AccessToken, []byte(testPassword), false, "replace-browser", credentials.CSRFToken)
	require.ErrorIs(t, err, usercmd.ErrInvalid)
	_, err = s.BeginReplace(ctx, credentials.AccessToken, []byte("incorrect"), true, "replace-browser", credentials.CSRFToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	start, err := s.BeginReplace(ctx, credentials.AccessToken, []byte(testPassword), true, "replace-browser", credentials.CSRFToken)
	require.NoError(t, err)
	require.IsType(t, &protocol.CredentialCreation{}, start.Options)
	_, err = s.FinishReplace(ctx, credentials.AccessToken, start.Transaction, "wrong-browser", credentials.CSRFToken, registrationResponseForStart(t, start))
	require.ErrorIs(t, err, ErrUnauthenticated)
	profile, err := p.Users().GetMFAProfile(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, key.ID, profile.Credential.ID)
	result, err := s.FinishReplace(ctx, credentials.AccessToken, start.Transaction, "replace-browser", credentials.CSRFToken, registrationResponseForStart(t, start))
	require.NoError(t, err)
	require.NotEmpty(t, result.AccessToken)
	profile, err = p.Users().GetMFAProfile(ctx, u.ID)
	require.NoError(t, err)
	require.NotEqual(t, key.ID, profile.Credential.ID)
	require.True(t, profile.Enabled)
	_, err = s.ValidateAccess(ctx, credentials.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	principal, err := s.ValidateAccess(ctx, result.AccessToken)
	require.NoError(t, err)
	require.Equal(t, profile.Credential.ID, principal.MFAFactorID)
}
