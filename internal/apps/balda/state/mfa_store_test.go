package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestSQLiteMFAUpgradeDefaultsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	db := provider.(*sqliteProvider).db
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM balda_mfa_profiles`).Scan(&count); err != nil {
		t.Fatalf("open must install optional MFA schema: %v", err)
	}
	if count != 0 {
		t.Fatalf("fresh users must not be opted in: profiles=%d", count)
	}
	store := provider.Users()
	user := mfaTestUser()
	if err := store.CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: "retained-password"}, mfaTestAudit("create")); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err = NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	secret, found, err := provider.Users().GetCredentialSecret(t.Context(), user.ID)
	if err != nil || !found || secret.PasswordHash != "retained-password" {
		t.Fatalf("reopen changed password: found=%t, error=%v", found, err)
	}
}

func mfaTestUser() usercmd.User {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	return usercmd.User{
		ID: "9d095254-4a1b-45c6-8274-363ea9f19228", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin",
		Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func mfaTestAudit(id string) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: id, Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: mfaTestUser().ID, Source: "mfa-test", OccurredAt: mfaTestUser().CreatedAt}
}
