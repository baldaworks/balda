package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestEqualAIAndWebAuthSessionIDsRemainIsolated(t *testing.T) {
	providerValue, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = providerValue.Close() })
	provider := providerValue.(*sqliteProvider)
	now := time.Now().UTC().Truncate(time.Second)
	const sharedID = "same-raw-id"
	if err := provider.Sessions().Upsert(t.Context(), SessionRecord{SessionID: sharedID, ChannelType: ChannelTypeTelegram,
		AddressKey: "1:0", AddressJSON: `{}`, Status: SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	user := usercmd.User{ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Primary: true, Version: 1,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	if err := provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: "hash"},
		testAudit("create-user", usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, user.ID, now)); err != nil {
		t.Fatal(err)
	}
	store := provider.users.(*sqlUserStore)
	tx, err := store.begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.checkBrowserAuthority(t.Context(), tx, browserAuthority{userID: user.ID, sessionID: sharedID,
		userVersion: 1, credentialVersion: 1, sessionVersion: 1, at: now})
	_ = tx.Rollback()
	if !errors.Is(err, usercmd.ErrWebAuthSessionUnavailable) {
		t.Fatalf("AI session authorized browser request: %v", err)
	}
	webAuth := usercmd.WebAuthSession{ID: sharedID, UserID: user.ID, Assurance: usercmd.WebAuthAssuranceNormal,
		CredentialVersion: 1, Version: 1, Access: usercmd.WebAuthAccessCredential{Selector: "access", VerifierDigest: []byte("access-digest"), ExpiresAt: now.Add(time.Minute)},
		CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour),
		RefreshTokens: []usercmd.WebAuthRefreshToken{{Selector: "refresh", VerifierDigest: []byte("refresh-digest"), Generation: 1,
			State: usercmd.WebAuthRefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	if err := provider.Users().CreateWebAuthSession(t.Context(), webAuth,
		testAudit("login", usercmd.AuditActionWebAuthLoginSucceeded, usercmd.AuditTargetWebAuthSession, sharedID, now)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := provider.Sessions().GetBySessionID(t.Context(), sharedID); err != nil || !found {
		t.Fatalf("AI session missing after WebAuth create: found=%t err=%v", found, err)
	}
	if _, found, err := provider.Users().GetWebAuthSession(t.Context(), sharedID); err != nil || !found {
		t.Fatalf("WebAuth session missing beside AI session: found=%t err=%v", found, err)
	}
	if err := provider.Users().RevokeWebAuthSession(t.Context(), sharedID, 1, now.Add(time.Second), "test",
		testAudit("revoke", usercmd.AuditActionWebAuthRevoked, usercmd.AuditTargetWebAuthSession, sharedID, now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, found, err := provider.Sessions().GetBySessionID(t.Context(), sharedID); err != nil || !found {
		t.Fatalf("WebAuth revoke cascaded to AI session: found=%t err=%v", found, err)
	}
}

func testAudit(id string, action usercmd.AuditAction, targetType usercmd.AuditTargetType, targetID string, at time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: targetType, TargetID: targetID, Source: "domain-isolation-test", OccurredAt: at}
}
