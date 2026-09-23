package backoffice

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/access"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

func TestAccessServiceUserLifecycleAndBotImpact(t *testing.T) {
	t.Parallel()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	admin := createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Primary:    true, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	service := access.NewService(provider.Users())
	actor := access.Actor{User: admin, SessionID: "admin-family"}

	created, err := service.CreateUser(t.Context(), actor, access.CreateInput{
		DisplayName: "Operator", Username: "operator", TemporaryPassword: []byte("temporary password"),
		Role: usercmd.RoleOperator, Status: usercmd.StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Credential.State != usercmd.CredentialStateTemporary || !created.Credential.MustChange {
		t.Fatalf("created credential = %+v", created.Credential)
	}

	claim := usercmd.BindingClaim{ID: "claim", UserID: created.ID, ChannelType: "telegram", ExpiresAt: now.Add(2 * time.Hour)}
	claimAudit := usercmd.AuditEvent{ID: "claim-audit", Action: usercmd.AuditActionBindingClaimCreated, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetBinding, TargetID: claim.ID, Source: "access-test", OccurredAt: now.Add(time.Hour)}
	if err := provider.Users().CreateBindingClaim(t.Context(), claim, now.Add(time.Hour), claimAudit); err != nil {
		t.Fatal(err)
	}
	binding := usercmd.Binding{ID: "binding", UserID: created.ID, ChannelType: "telegram", Principal: "42", CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)}
	bindingAudit := usercmd.AuditEvent{ID: "binding-audit", Action: usercmd.AuditActionBindingAttached, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetBinding, TargetID: binding.ID, Source: "access-test", OccurredAt: now.Add(time.Hour)}
	if err := provider.Users().AttachBinding(t.Context(), claim.ID, binding, now.Add(time.Hour), bindingAudit); err != nil {
		t.Fatal(err)
	}
	created, _, err = provider.Users().GetUser(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUser(t.Context(), actor, access.UpdateInput{
		UserID: created.ID, DisplayName: created.DisplayName, Username: created.Username,
		Role: usercmd.RoleAdministrator, Status: created.Status, ExpectedVersion: created.Version,
	}); !errors.Is(err, usercmd.ErrBotImpactAcknowledgementRequired) {
		t.Fatalf("unacknowledged bot impact error = %v", err)
	}
	updated, err := service.UpdateUser(t.Context(), actor, access.UpdateInput{
		UserID: created.ID, DisplayName: created.DisplayName, Username: created.Username,
		Role: usercmd.RoleAdministrator, Status: created.Status, ExpectedVersion: created.Version,
		AcknowledgeBotAccessImpact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if capability := users.BotCapability(updated); capability != usercmd.BotCapabilityOwner {
		t.Fatalf("updated bot capability = %q", capability)
	}
}

func TestAccessServiceCredentialResetRevokesAccessAndRefreshFamily(t *testing.T) {
	t.Parallel()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	admin := createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Primary:    true, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	securityService, err := security.NewService(provider.Users(), security.Config{AccessTTL: 15 * time.Minute, RefreshTTL: 12 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := securityService.Login(t.Context(), "admin", []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := securityService.ValidateAccess(t.Context(), credentials.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	service := access.NewService(provider.Users())
	err = service.ResetCredential(t.Context(), access.Actor{User: admin, SessionID: principal.FamilyID}, access.CredentialInput{
		UserID: admin.ID, ExpectedUserVersion: admin.Version, ExpectedCredentialVersion: admin.Credential.Version,
		TemporaryPassword: []byte("replacement temporary"), ConfirmCurrent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := securityService.ValidateAccess(t.Context(), credentials.AccessToken); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("old access validation error = %v", err)
	}
	if _, err := securityService.Refresh(t.Context(), credentials.RefreshToken, credentials.CSRFToken); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("old refresh validation error = %v", err)
	}
	updated, found, err := provider.Users().GetUser(t.Context(), admin.ID)
	if err != nil || !found {
		t.Fatalf("GetUser() = found %t, error %v", found, err)
	}
	if updated.Version != 2 || updated.Credential.Version != 2 || !updated.Credential.MustChange {
		t.Fatalf("updated user = %+v", updated)
	}
}

func TestAccessServiceDisablementPermanentlyRevokesAccessAndRefreshFamily(t *testing.T) {
	t.Parallel()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	admin := createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Primary:    true, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	operator := createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "operator", DisplayName: "Operator", Username: "operator", NormalizedUsername: "operator",
		Status: usercmd.StatusActive, Role: usercmd.RoleOperator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Version:    1, CreatedAt: now, UpdatedAt: now,
	})
	securityService, err := security.NewService(provider.Users(), security.Config{AccessTTL: 15 * time.Minute, RefreshTTL: 12 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := securityService.Login(t.Context(), operator.Username, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	service := access.NewService(provider.Users())
	disabled, err := service.UpdateUser(t.Context(), access.Actor{User: admin, SessionID: "admin-family"}, access.UpdateInput{
		UserID: operator.ID, DisplayName: operator.DisplayName, Username: operator.Username,
		Role: operator.Role, Status: usercmd.StatusDisabled, ExpectedVersion: operator.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUser(t.Context(), access.Actor{User: admin, SessionID: "admin-family"}, access.UpdateInput{
		UserID: disabled.ID, DisplayName: disabled.DisplayName, Username: disabled.Username,
		Role: disabled.Role, Status: usercmd.StatusActive, ExpectedVersion: disabled.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := securityService.ValidateAccess(t.Context(), credentials.AccessToken); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("re-enabled old access validation error = %v", err)
	}
	if _, err := securityService.Refresh(t.Context(), credentials.RefreshToken, credentials.CSRFToken); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("re-enabled old refresh validation error = %v", err)
	}
}

func createAccessTestUser(t *testing.T, store usercmd.Store, user usercmd.User) usercmd.User {
	t.Helper()
	hash, err := userpassword.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	audit := usercmd.AuditEvent{
		ID: "create-" + user.ID, Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "access-test", OccurredAt: user.CreatedAt,
	}
	if err := store.CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, audit); err != nil {
		t.Fatal(err)
	}
	return user
}
