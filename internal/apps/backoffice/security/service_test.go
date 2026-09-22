package security

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
)

func TestLoginCreatesOpaqueNormalAndRestrictedFamilies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		state     usercmd.CredentialState
		assurance usercmd.SessionAssurance
	}{
		{name: "normal", state: usercmd.CredentialStateActive, assurance: usercmd.SessionAssuranceNormal},
		{name: "temporary", state: usercmd.CredentialStateTemporary, assurance: usercmd.SessionAssuranceRestricted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, service, now := newSecurityTestService(t)
			user := createSecurityTestUser(t, provider.Users(), "user-"+tt.name, "user."+tt.name, tt.state, usercmd.RoleAdministrator, tt.name == "normal", now)
			credentials, err := service.Login(t.Context(), user.Username, []byte(testPassword))
			if err != nil {
				t.Fatalf("Login() error = %v", err)
			}
			if credentials.Assurance != tt.assurance || credentials.AccessToken == "" || credentials.RefreshToken == "" || credentials.CSRFToken == "" {
				t.Fatalf("Login() = %+v", credentials)
			}
			principal, err := service.ValidateAccess(t.Context(), credentials.AccessToken)
			if err != nil || principal.User.ID != user.ID || principal.FamilyID == "" || principal.Assurance != tt.assurance {
				t.Fatalf("ValidateAccess() = %+v, %v", principal, err)
			}
			if err := service.ValidateCSRF(t.Context(), credentials.AccessToken, credentials.CSRFToken); err != nil {
				t.Fatalf("ValidateCSRF() error = %v", err)
			}
			if err := service.ValidateCSRF(t.Context(), credentials.AccessToken, "wrong-csrf"); !errors.Is(err, ErrForbidden) {
				t.Fatalf("ValidateCSRF(wrong) error = %v", err)
			}
			selector, verifier, err := parseOpaqueToken(credentials.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			stored, found, err := provider.Users().GetSessionByAccessSelector(t.Context(), selector)
			if err != nil || !found {
				t.Fatalf("GetSessionByAccessSelector() found=%t error=%v", found, err)
			}
			if string(stored.Family.Access.VerifierDigest) == verifier || string(stored.Family.CSRFVerifierDigest) == credentials.CSRFToken {
				t.Fatal("plaintext browser credential reached persistence")
			}
			refreshSelector, refreshVerifier, err := parseOpaqueToken(credentials.RefreshToken)
			if err != nil {
				t.Fatal(err)
			}
			refreshStored, found, err := provider.Users().GetSessionByRefreshSelector(t.Context(), refreshSelector)
			if err != nil || !found || string(refreshStored.Token.VerifierDigest) == refreshVerifier {
				t.Fatalf("persisted refresh credential = %+v, found=%t, error=%v", refreshStored.Token, found, err)
			}
			if tt.assurance == usercmd.SessionAssuranceRestricted {
				service.now = func() time.Time { return now.Add(time.Minute) }
				rotated, err := service.Refresh(t.Context(), credentials.RefreshToken, credentials.CSRFToken)
				if err != nil || rotated.Assurance != usercmd.SessionAssuranceRestricted {
					t.Fatalf("Refresh(restricted) = %+v, %v", rotated, err)
				}
			}
		})
	}
}

func TestLoginFailuresAreUniformAndBindingCannotAuthenticate(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "disabled", "disabled", usercmd.CredentialStateDisabled, usercmd.RoleAdministrator, true, now)
	for _, attempt := range []struct {
		username string
		password string
	}{
		{username: "missing", password: testPassword},
		{username: "disabled", password: testPassword},
		{username: "disabled", password: "incorrect password value"},
	} {
		if _, err := service.Login(t.Context(), attempt.username, []byte(attempt.password)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Login(%q) error = %v, want ErrUnauthenticated", attempt.username, err)
		}
	}
}

func TestRefreshRotatesPairWithFixedExpiryAndReplayRevokesFamily(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "admin", "admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	initial, err := service.Login(t.Context(), "admin", []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(10 * time.Minute) }
	rotated, err := service.Refresh(t.Context(), initial.RefreshToken, initial.CSRFToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if rotated.AccessToken == initial.AccessToken || rotated.RefreshToken == initial.RefreshToken || !rotated.RefreshExpiresAt.Equal(initial.RefreshExpiresAt) {
		t.Fatalf("rotated credentials = %+v, initial=%+v", rotated, initial)
	}
	if _, err := service.Refresh(t.Context(), initial.RefreshToken, initial.CSRFToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Refresh(replay) error = %v, want ErrUnauthenticated", err)
	}
	if _, err := service.ValidateAccess(t.Context(), rotated.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ValidateAccess(after replay) error = %v, want ErrUnauthenticated", err)
	}
}

func TestAccessUsesCurrentCanonicalStateAndExpires(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "primary", "primary", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	operator := createSecurityTestUser(t, provider.Users(), "operator", "operator", usercmd.CredentialStateActive, usercmd.RoleOperator, false, now)
	credentials, err := service.Login(t.Context(), operator.Username, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	operator.Role = usercmd.RoleAdministrator
	operator.Version++
	operator.UpdatedAt = now.Add(time.Minute)
	if err := provider.Users().UpdateUser(t.Context(), operator, 1, securityTestAudit("role-change", usercmd.AuditActionUserRoleChanged, operator.ID, now)); err != nil {
		t.Fatal(err)
	}
	principal, err := service.ValidateAccess(t.Context(), credentials.AccessToken)
	if err != nil || principal.User.Role != usercmd.RoleAdministrator {
		t.Fatalf("ValidateAccess(current role) = %+v, %v", principal, err)
	}
	service.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, err := service.ValidateAccess(t.Context(), credentials.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ValidateAccess(expired) error = %v, want ErrUnauthenticated", err)
	}
}

func TestAccessRejectsDisabledCanonicalUser(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "primary", "primary", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	operator := createSecurityTestUser(t, provider.Users(), "operator", "operator", usercmd.CredentialStateActive, usercmd.RoleOperator, false, now)
	credentials, err := service.Login(t.Context(), operator.Username, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	operator.Status = usercmd.StatusDisabled
	operator.Version++
	operator.UpdatedAt = now.Add(time.Minute)
	if err := provider.Users().UpdateUser(t.Context(), operator, 1, securityTestAudit("disable", usercmd.AuditActionUserStatusChanged, operator.ID, now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateAccess(t.Context(), credentials.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ValidateAccess(disabled user) error = %v", err)
	}
}

func TestRefreshRejectsExpiredFamilyAndWrongCSRF(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "admin", "admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	credentials, err := service.Login(t.Context(), "admin", []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(t.Context(), credentials.RefreshToken, "wrong-csrf"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Refresh(wrong CSRF) error = %v", err)
	}
	service.now = func() time.Time { return now.Add(12 * time.Hour) }
	if _, err := service.Refresh(t.Context(), credentials.RefreshToken, credentials.CSRFToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Refresh(expired) error = %v", err)
	}
}

func TestTemporaryPasswordReplacementRevokesOldRefreshAndIssuesNormalFamily(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	user := createSecurityTestUser(t, provider.Users(), "temporary", "temporary", usercmd.CredentialStateTemporary, usercmd.RoleAdministrator, true, now)
	restricted, err := service.Login(t.Context(), user.Username, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(time.Minute) }
	normal, err := service.ReplacePassword(t.Context(), restricted.AccessToken, nil, []byte(replacementPassword))
	if err != nil {
		t.Fatalf("ReplacePassword() error = %v", err)
	}
	if normal.Assurance != usercmd.SessionAssuranceNormal {
		t.Fatalf("replacement assurance = %q", normal.Assurance)
	}
	if _, err := service.ValidateAccess(t.Context(), restricted.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old restricted access error = %v", err)
	}
	refreshSelector, _, err := parseOpaqueToken(restricted.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	revoked, found, err := provider.Users().GetSessionByRefreshSelector(t.Context(), refreshSelector)
	if err != nil || !found || revoked.Token.State != usercmd.RefreshTokenStateRevoked {
		t.Fatalf("old refresh = %+v, found=%t, error=%v", revoked, found, err)
	}
	if _, err := service.Login(t.Context(), user.Username, []byte(testPassword)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Login(old password) error = %v", err)
	}
	if _, err := service.Login(t.Context(), user.Username, []byte(replacementPassword)); err != nil {
		t.Fatalf("Login(new password) error = %v", err)
	}
}

func TestNormalPasswordRotationRequiresCurrentPasswordAndRevokesOldFamily(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "admin", "admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	initial, err := service.Login(t.Context(), "admin", []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplacePassword(t.Context(), initial.AccessToken, []byte("incorrect password"), []byte(replacementPassword)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ReplacePassword(wrong current) error = %v", err)
	}
	if _, err := service.ReplacePassword(t.Context(), initial.AccessToken, []byte(testPassword), []byte("short")); !errors.Is(err, usercmd.ErrInvalid) {
		t.Fatalf("ReplacePassword(short replacement) error = %v", err)
	}
	if _, err := service.ValidateAccess(t.Context(), initial.AccessToken); err != nil {
		t.Fatalf("failed password rotation mutated session: %v", err)
	}
	rotated, err := service.ReplacePassword(t.Context(), initial.AccessToken, []byte(testPassword), []byte(replacementPassword))
	if err != nil {
		t.Fatalf("ReplacePassword() error = %v", err)
	}
	if rotated.Assurance != usercmd.SessionAssuranceNormal {
		t.Fatalf("rotated assurance = %q", rotated.Assurance)
	}
	if _, err := service.ValidateAccess(t.Context(), initial.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old access after password rotation = %v", err)
	}
	if _, err := service.Refresh(t.Context(), initial.RefreshToken, initial.CSRFToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old refresh after password rotation = %v", err)
	}
	if _, err := service.ValidateAccess(t.Context(), rotated.AccessToken); err != nil {
		t.Fatalf("new access after password rotation = %v", err)
	}
}

func TestLogoutRevokesFamily(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "admin", "admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	credentials, err := service.Login(t.Context(), "admin", []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), credentials.AccessToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.ValidateAccess(t.Context(), credentials.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ValidateAccess(after logout) error = %v", err)
	}
}

func TestSessionAdministrationHonorsOwnershipAndCurrentConfirmation(t *testing.T) {
	t.Parallel()
	provider, service, now := newSecurityTestService(t)
	createSecurityTestUser(t, provider.Users(), "admin", "admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	createSecurityTestUser(t, provider.Users(), "operator", "operator", usercmd.CredentialStateActive, usercmd.RoleOperator, false, now)
	admin, err := service.Login(t.Context(), "admin", []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	operator, err := service.Login(t.Context(), "operator", []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	operatorPrincipal, err := service.ValidateAccess(t.Context(), operator.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeSession(t.Context(), operator.AccessToken, operatorPrincipal.FamilyID, false); !errors.Is(err, usercmd.ErrCurrentSessionConfirmationRequired) {
		t.Fatalf("RevokeSession(current) error = %v", err)
	}
	adminPrincipal, err := service.ValidateAccess(t.Context(), admin.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeSession(t.Context(), operator.AccessToken, adminPrincipal.FamilyID, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("RevokeSession(other by operator) error = %v", err)
	}
	if err := service.RevokeSession(t.Context(), admin.AccessToken, operatorPrincipal.FamilyID, false); err != nil {
		t.Fatalf("RevokeSession(other by admin) error = %v", err)
	}
	if _, err := service.ValidateAccess(t.Context(), operator.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ValidateAccess(revoked operator) error = %v", err)
	}
}

const (
	testPassword        = "correct horse battery staple"
	replacementPassword = "replacement battery staple value"
)

func newSecurityTestService(t *testing.T) (state.Provider, *Service, time.Time) {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	service, err := NewService(provider.Users(), Config{AccessTTL: 15 * time.Minute, RefreshTTL: 12 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return provider, service, now
}

func createSecurityTestUser(
	t *testing.T,
	store usercmd.Store,
	id string,
	username string,
	credentialState usercmd.CredentialState,
	role usercmd.Role,
	primary bool,
	now time.Time,
) usercmd.User {
	t.Helper()
	hash, err := userpassword.Hash([]byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	user := usercmd.User{
		ID: id, DisplayName: id, Username: username, NormalizedUsername: username,
		Status: usercmd.StatusActive, Role: role,
		Credential: usercmd.Credential{
			State: credentialState, MustChange: credentialState == usercmd.CredentialStateTemporary, Version: 1,
		},
		Primary: primary, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: id, PasswordHash: hash}, securityTestAudit("create-"+id, usercmd.AuditActionCredentialChanged, id, now)); err != nil {
		t.Fatal(err)
	}
	return user
}

func securityTestAudit(id string, action usercmd.AuditAction, targetID string, now time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: targetID, Source: "security-test", OccurredAt: now,
	}
}
