package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	libwebauthn "github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/require"
)

func TestEnrolledLoginRequiresAssertion(t *testing.T) {
	_, s, u, private, key, _ := enrolledTestService(t)
	ctx := withMFABrowser(t.Context(), "browser", "csrf")
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	require.Empty(t, pending.AccessToken)
	require.Empty(t, pending.RefreshToken)
	require.NotNil(t, pending.Pending)
	response := assertionResponseForStart(t, *pending.Pending, key, private, 0x05, 1)
	credentials, err := s.FinishLogin(ctx, pending.Pending.Transaction, "browser", "csrf", response)
	require.NoError(t, err)
	principal, err := s.ValidateAccess(ctx, credentials.AccessToken)
	require.NoError(t, err)
	require.Equal(t, u.ID, principal.User.ID)
	require.NoError(t, s.RequireFresh(ctx, credentials.AccessToken))
	_, err = s.FinishLogin(ctx, pending.Pending.Transaction, "browser", "csrf", response)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestMFARefreshPreservesProofAndStepUpPreservesRefresh(t *testing.T) {
	p, s, u, private, key, now := enrolledTestService(t)
	s.config.RefreshTTL = 30 * 24 * time.Hour
	ctx := withMFABrowser(t.Context(), "browser", "csrf")
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	credentials, err := s.FinishLogin(ctx, pending.Pending.Transaction, "browser", "csrf", assertionResponseForStart(t, *pending.Pending, key, private, 0x05, 1))
	require.NoError(t, err)
	s.now = func() time.Time { return now.Add(29 * 24 * time.Hour) }
	rotated, err := s.Refresh(ctx, credentials.RefreshToken, credentials.CSRFToken)
	require.NoError(t, err)
	require.Equal(t, now.Add(59*24*time.Hour), rotated.RefreshExpiresAt)
	principal, err := s.ValidateAccess(ctx, rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, now, principal.WebAuthnVerifiedAt)
	require.ErrorIs(t, s.RequireFresh(ctx, rotated.AccessToken), ErrStepUp)
	start, err := s.BeginStepUp(ctx, rotated.AccessToken, "step-browser", rotated.CSRFToken)
	require.NoError(t, err)
	stepped, err := s.FinishStepUp(ctx, rotated.AccessToken, start.Transaction, "step-browser", rotated.CSRFToken, assertionResponseForStart(t, start, key, private, 0x05, 2))
	require.NoError(t, err)
	require.Empty(t, stepped.RefreshToken)
	require.Equal(t, rotated.RefreshExpiresAt, stepped.RefreshExpiresAt)
	require.NoError(t, s.RequireFresh(ctx, stepped.AccessToken))
	require.ErrorIs(t, func() error { _, err := s.ValidateAccess(ctx, rotated.AccessToken); return err }(), ErrUnauthenticated)
	principal, err = s.ValidateAccess(ctx, stepped.AccessToken)
	require.NoError(t, err)
	family, found, err := p.Users().GetSession(ctx, principal.FamilyID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, family.RefreshTokens, 2)
	// A duplicate refresh remains a conflict and does not revoke the stepped family.
	_, err = s.Refresh(ctx, credentials.RefreshToken, credentials.CSRFToken)
	require.ErrorIs(t, err, ErrRefreshConcurrent)
	require.NoError(t, s.RequireFresh(ctx, stepped.AccessToken))
	s.now = func() time.Time { return now.Add(29*24*time.Hour + 31*time.Second) }
	_, err = s.Refresh(ctx, credentials.RefreshToken, credentials.CSRFToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = s.ValidateAccess(ctx, stepped.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestMFAPasswordResetAndReplacementPreserveFactor(t *testing.T) {
	p, s, u, private, key, now := enrolledTestService(t)
	secret, found, err := p.Users().GetCredentialSecret(t.Context(), u.ID)
	require.NoError(t, err)
	require.True(t, found)
	err = p.Users().ChangeCredential(t.Context(), u.ID, u.Version, u.Credential.Version,
		usercmd.Credential{State: usercmd.CredentialStateTemporary, MustChange: true, Version: u.Credential.Version + 1}, secret, now,
		s.audit(usercmd.AuditActionCredentialChanged, usercmd.AuditTargetUser, u.ID, "temporary reset", now))
	require.NoError(t, err)
	ctx := withMFABrowser(t.Context(), "browser", "csrf")
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	require.Empty(t, pending.AccessToken)
	credentials, err := s.FinishLogin(ctx, pending.Pending.Transaction, "browser", "csrf", assertionResponseForStart(t, *pending.Pending, key, private, 0x05, 1))
	require.NoError(t, err)
	require.Equal(t, usercmd.SessionAssuranceRestricted, credentials.Assurance)
	replacement, err := s.ReplacePassword(ctx, credentials.AccessToken, []byte(testPassword), []byte("a sufficiently long replacement password"))
	require.NoError(t, err)
	require.Equal(t, usercmd.SessionAssuranceNormal, replacement.Assurance)
	require.NoError(t, s.RequireFresh(ctx, replacement.AccessToken))
	profile, err := p.Users().GetMFAProfile(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, profile.Enabled)
	require.Equal(t, key.ID, profile.Credential.ID)
	_, err = s.ValidateAccess(ctx, credentials.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestMFARoleTransitionsRetainOptInAndRejectStaleSessions(t *testing.T) {
	p, s, u, private, key, now := enrolledTestService(t)
	ctx := withMFABrowser(t.Context(), "browser", "csrf")
	login, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	verified, err := s.FinishLogin(ctx, login.Pending.Transaction, "browser", "csrf", assertionResponseForStart(t, *login.Pending, key, private, 0x05, 1))
	require.NoError(t, err)
	u.Role, u.Primary, u.Version, u.UpdatedAt = usercmd.RoleOperator, false, u.Version+1, now
	// Keep a separate active administrator so canonical last-admin constraints hold.
	createSecurityTestUser(t, p.Users(), "other-admin", "other-admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, false, now)
	err = p.Users().UpdateUser(ctx, u, u.Version-1, s.audit(usercmd.AuditActionUserRoleChanged, usercmd.AuditTargetUser, u.ID, "demote", now))
	require.NoError(t, err)
	_, err = s.ValidateAccess(ctx, verified.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	operatorCredentials, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	require.Nil(t, operatorCredentials.Pending)
	require.NotEmpty(t, operatorCredentials.AccessToken)
	u.Role, u.Version = usercmd.RoleAdministrator, u.Version+1
	err = p.Users().UpdateUser(ctx, u, u.Version-1, s.audit(usercmd.AuditActionUserRoleChanged, usercmd.AuditTargetUser, u.ID, "promote", now))
	require.NoError(t, err)
	_, err = s.ValidateAccess(ctx, operatorCredentials.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	require.NotNil(t, pending.Pending)
	s.webauthn = nil
	_, err = s.Login(ctx, u.Username, []byte(testPassword))
	require.ErrorIs(t, err, ErrMFAUnavailable)
}

func enrolledTestService(t *testing.T) (state.Provider, *Service, usercmd.User, *ecdsa.PrivateKey, usercmd.MFACredential, time.Time) {
	t.Helper()
	p, s, _ := newSecurityTestService(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	u := createSecurityTestUser(t, p.Users(), "mfa-admin", "mfa-admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	credentials, err := s.Login(t.Context(), u.Username, []byte(testPassword))
	require.NoError(t, err)
	principal, err := s.ValidateAccess(t.Context(), credentials.AccessToken)
	require.NoError(t, err)
	s.webauthn, err = newWebAuthnEngine("https://example.org")
	require.NoError(t, err)
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: private.X.FillBytes(make([]byte, 32)), -3: private.Y.FillBytes(make([]byte, 32))})
	require.NoError(t, err)
	key, err := s.webauthn.credential(u.ID, "factor", &libwebauthn.Credential{ID: []byte("mfa-credential"), PublicKey: public}, now, time.Time{})
	require.NoError(t, err)
	err = p.Users().ApplyMFAChange(t.Context(), usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: u.Version,
		ExpectedCredentialVersion: u.Credential.Version, Purpose: usercmd.MFAEnable, BoundSessionID: principal.FamilyID,
		ExpectedSessionVersion: principal.Version, Credential: key, ChangedAt: now,
		Audit: s.audit(usercmd.AuditActionCredentialChanged, usercmd.AuditTargetUser, u.ID, "enable test", now)})
	require.NoError(t, err)
	u, _, err = p.Users().GetUser(t.Context(), u.ID)
	require.NoError(t, err)
	return p, s, u, private, key, now
}

func assertionResponseForStart(t *testing.T, start CeremonyStart, key usercmd.MFACredential, private *ecdsa.PrivateKey, flags byte, counter uint32) []byte {
	t.Helper()
	client, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": base64.RawURLEncoding.EncodeToString(start.Options.(*protocol.CredentialAssertion).Response.Challenge), "origin": "https://example.org", "crossOrigin": false})
	require.NoError(t, err)
	rpHash := sha256.Sum256([]byte("example.org"))
	auth := append(append([]byte(nil), rpHash[:]...), flags)
	auth = binary.BigEndian.AppendUint32(auth, counter)
	clientHash := sha256.Sum256(client)
	message := sha256.Sum256(append(append([]byte(nil), auth...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, private, message[:])
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"id": base64.RawURLEncoding.EncodeToString(key.CredentialID), "rawId": base64.RawURLEncoding.EncodeToString(key.CredentialID), "type": "public-key", "response": map[string]any{
		"clientDataJSON": base64.RawURLEncoding.EncodeToString(client), "authenticatorData": base64.RawURLEncoding.EncodeToString(auth), "signature": base64.RawURLEncoding.EncodeToString(signature)}})
	require.NoError(t, err)
	return body
}
