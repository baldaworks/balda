//go:build integration && (sqlite || postgres)

package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkUserStoreActiveSessions(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	user := contractUser("session-list-user", "session.list", true, now.Add(-time.Hour))
	if err := store.CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit("create-session-list-user", usercmd.AuditActionCredentialChanged, user.ID, now)); err != nil {
		t.Fatal(err)
	}
	for index := range 103 {
		family := contractSessionFamily(user.ID, now.Add(-time.Hour))
		family.ID = fmt.Sprintf("family-%03d", index)
		family.Access.Selector = fmt.Sprintf("access-%03d", index)
		family.RefreshTokens[0].Selector = fmt.Sprintf("refresh-%03d", index)
		family.LastSeenAt = now.Add(time.Duration(index-103) * time.Millisecond)
		if index == 0 {
			family.DeviceLabel = "Firefox on Linux"
			family.ConnectionPeer = "192.0.2.1"
		}
		if err := store.CreateSession(t.Context(), family, contractAudit(fmt.Sprintf("login-%03d", index), usercmd.AuditActionLoginSucceeded, family.ID, now)); err != nil {
			t.Fatalf("CreateSession(%d): %v", index, err)
		}
	}
	expired := contractSessionFamily(user.ID, now.Add(-25*time.Hour))
	expired.ID = "expired-family"
	expired.Access.Selector = "expired-access"
	expired.RefreshTokens[0].Selector = "expired-refresh"
	if err := store.CreateSession(t.Context(), expired, contractAudit("login-expired", usercmd.AuditActionLoginSucceeded, expired.ID, now)); err != nil {
		t.Fatal(err)
	}
	revoked := contractSessionFamily(user.ID, now.Add(-time.Hour))
	revoked.ID = "revoked-family"
	revoked.Access.Selector = "revoked-access"
	revoked.RefreshTokens[0].Selector = "revoked-refresh"
	if err := store.CreateSession(t.Context(), revoked, contractAudit("login-revoked", usercmd.AuditActionLoginSucceeded, revoked.ID, now)); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeSession(t.Context(), revoked.ID, revoked.Version, now, "test revocation", contractAudit("revoke-family", usercmd.AuditActionSessionRevoked, revoked.ID, now)); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListActiveSessions(t.Context(), user.ID, usercmd.PageRequest{Limit: 100}, now)
	if err != nil || len(page.Sessions) != 100 || page.NextAfterID == "" || page.Sessions[0].ID != "family-102" {
		t.Fatalf("first active page: %+v, %v", page, err)
	}
	next, err := store.ListActiveSessions(t.Context(), user.ID, usercmd.PageRequest{AfterID: page.NextAfterID, Limit: 100}, now)
	if err != nil || len(next.Sessions) != 3 || next.NextAfterID != "" || next.Sessions[2].ID != "family-000" {
		t.Fatalf("second active page: %+v, %v", next, err)
	}
	if next.Sessions[2].DeviceLabel != "Firefox on Linux" || next.Sessions[2].ConnectionPeer != "192.0.2.1" || next.Sessions[1].DeviceLabel != "" {
		t.Fatalf("session metadata: %+v", next.Sessions)
	}
}
