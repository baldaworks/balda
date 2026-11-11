//go:build integration && sqlite

package state

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestSQLiteBackofficeAndBotOperationsRemainAtomicAcrossProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	firstStore := first.Users()
	secondStore := second.Users()
	now := time.Date(2026, 9, 23, 5, 0, 0, 0, time.UTC)
	user := contractUser("admin-1", "admin.one", true, now)
	if err := firstStore.CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit("audit-create", usercmd.AuditActionUserCreated, user.ID, now)); err != nil {
		t.Fatal(err)
	}
	claim := usercmd.BindingClaim{ID: "claim-1", UserID: user.ID, ChannelType: "telegram", ExpiresAt: now.Add(time.Hour)}
	if err := firstStore.CreateBindingClaim(t.Context(), claim, now, contractAudit("audit-claim", usercmd.AuditActionBindingClaimCreated, claim.ID, now)); err != nil {
		t.Fatal(err)
	}
	family := contractSessionFamily(user.ID, now)
	if err := firstStore.CreateSession(t.Context(), family, contractAudit("audit-login", usercmd.AuditActionLoginSucceeded, family.ID, now)); err != nil {
		t.Fatal(err)
	}

	binding := usercmd.Binding{
		ID: "binding-1", UserID: user.ID, ChannelType: "telegram", Principal: "101",
		DisplayName: "Admin", Provenance: "verified", CreatedAt: now, UpdatedAt: now,
	}
	updated := user
	updated.DisplayName = "Updated Administrator"
	updated.Version = 2
	updated.UpdatedAt = now.Add(time.Minute)
	rotation := usercmd.RefreshRotation{
		Selector: family.RefreshTokens[0].Selector, PresentedVerifierDigest: family.RefreshTokens[0].VerifierDigest,
		Access: usercmd.AccessCredential{Selector: "access-2", VerifierDigest: []byte("access-digest-2"), ExpiresAt: now.Add(30 * time.Minute)},
		Refresh: usercmd.RefreshToken{
			Selector: "refresh-2", VerifierDigest: []byte("refresh-digest-2"), Generation: 2,
			State: usercmd.RefreshTokenStateActive, IssuedAt: now.Add(15 * time.Minute), ExpiresAt: family.RefreshExpiresAt,
		},
		RotatedAt:    now.Add(15 * time.Minute),
		SuccessAudit: contractAudit("audit-refresh", usercmd.AuditActionRefreshSucceeded, family.ID, now.Add(15*time.Minute)),
		ReplayAudit:  contractAudit("audit-replay", usercmd.AuditActionRefreshReplay, family.ID, now.Add(15*time.Minute)),
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	var attachErr, updateErr, revokeErr error
	var lookupErr error
	var rotationResult usercmd.RefreshRotationResult
	var rotationErr error
	wait.Add(5)
	go func() {
		defer wait.Done()
		<-start
		_, _, lookupErr = firstStore.GetUserByBinding(t.Context(), "telegram", "101")
	}()
	go func() {
		defer wait.Done()
		<-start
		attachErr = secondStore.AttachBinding(t.Context(), claim.ID, binding, now.Add(time.Minute), contractAudit("audit-binding", usercmd.AuditActionBindingAttached, binding.ID, now.Add(time.Minute)))
	}()
	go func() {
		defer wait.Done()
		<-start
		updateErr = firstStore.UpdateUser(t.Context(), updated, 1, contractAudit("audit-update", usercmd.AuditActionUserUpdated, user.ID, updated.UpdatedAt))
	}()
	go func() {
		defer wait.Done()
		<-start
		rotationResult, rotationErr = secondStore.RotateRefresh(t.Context(), rotation)
	}()
	go func() {
		defer wait.Done()
		<-start
		revokeErr = firstStore.RevokeSession(
			t.Context(), family.ID, 1, now.Add(20*time.Minute), "administrative revocation",
			contractAudit("audit-revoke", usercmd.AuditActionSessionRevoked, family.ID, now.Add(20*time.Minute)),
		)
	}()
	close(start)
	wait.Wait()

	if lookupErr != nil || attachErr != nil || updateErr != nil || rotationErr != nil {
		t.Fatalf("concurrent lookup/attach/update/rotation errors = %v / %v / %v / %v", lookupErr, attachErr, updateErr, rotationErr)
	}
	if rotationResult != usercmd.RefreshRotationSucceeded && rotationResult != usercmd.RefreshRotationUnavailable {
		t.Fatalf("rotation result = %q", rotationResult)
	}
	if revokeErr != nil && !errors.Is(revokeErr, usercmd.ErrConflict) {
		t.Fatalf("concurrent revocation error = %v", revokeErr)
	}
	if errors.Is(revokeErr, usercmd.ErrConflict) {
		current, found, err := secondStore.GetSession(t.Context(), family.ID)
		if err != nil || !found {
			t.Fatalf("load session for retry = %+v, %t, %v", current, found, err)
		}
		if err := firstStore.RevokeSession(
			t.Context(), current.ID, current.Version, now.Add(21*time.Minute), "administrative revocation retry",
			contractAudit("audit-revoke-retry", usercmd.AuditActionSessionRevoked, family.ID, now.Add(21*time.Minute)),
		); err != nil {
			t.Fatalf("retry revocation: %v", err)
		}
	}

	bound, found, err := firstStore.GetUserByBinding(t.Context(), "telegram", "101")
	if err != nil || !found || bound.ID != user.ID || bound.DisplayName != updated.DisplayName || bound.Binding == nil {
		t.Fatalf("canonical binding lookup = %+v, %t, %v", bound, found, err)
	}
	storedFamily, found, err := secondStore.GetSession(t.Context(), family.ID)
	if err != nil || !found || storedFamily.RevokedAt.IsZero() {
		t.Fatalf("revoked family = %+v, %t, %v", storedFamily, found, err)
	}
	for _, token := range storedFamily.RefreshTokens {
		if token.State == usercmd.RefreshTokenStateActive {
			t.Fatalf("refresh generation %d state = %q", token.Generation, token.State)
		}
		if !token.ExpiresAt.Equal(family.RefreshExpiresAt) {
			t.Fatalf("refresh generation %d expiry = %s, want %s", token.Generation, token.ExpiresAt, family.RefreshExpiresAt)
		}
	}
}
