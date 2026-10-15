//go:build integration && (sqlite || postgres)

package state

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkMFAStoreLifecycle(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	s := p.Users()
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	u := contractUser("mfa-admin", "mfa-admin", true, now)
	audit := func(id string) usercmd.AuditEvent {
		return contractAudit(id, usercmd.AuditActionCredentialChanged, u.ID, now)
	}
	if err := s.CreateUser(t.Context(), u, contractSecret(u.ID), audit("create")); err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetMFAProfile(t.Context(), u.ID)
	if err != nil || profile.Enabled || profile.Version != 0 {
		t.Fatalf("default profile: %+v, %v", profile, err)
	}
	f := contractSessionFamily(u.ID, now)
	if err := s.CreateSession(t.Context(), f, audit("password-login")); err != nil {
		t.Fatal(err)
	}
	ceremony := usercmd.MFACeremony{TokenDigest: bytes.Repeat([]byte{1}, 32), BrowserDigest: bytes.Repeat([]byte{2}, 32),
		CSRFDigest: bytes.Repeat([]byte{3}, 32), UserID: u.ID, Purpose: usercmd.MFAEnable, SessionID: f.ID,
		UserVersion: 1, CredentialVersion: 1, Data: []byte(`{"challenge":"public-state"}`), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.CreateMFACeremony(t.Context(), ceremony); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeMFACeremony(t.Context(), ceremony.TokenDigest, bytes.Repeat([]byte{4}, 32), ceremony.CSRFDigest, ceremony.Purpose, now); !errors.Is(err, usercmd.ErrSessionUnavailable) {
		t.Fatalf("wrong browser: %v", err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := s.ConsumeMFACeremony(t.Context(), ceremony.TokenDigest, ceremony.BrowserDigest, ceremony.CSRFDigest, ceremony.Purpose, now); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent consume successes=%d", successes.Load())
	}
	key := usercmd.MFACredential{ID: "factor-1", UserID: u.ID, RPID: "localhost", CredentialID: []byte("credential-1"),
		PublicKey: []byte("public-key"), Data: []byte(`{"credential":"public"}`), CreatedAt: now}
	change := usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: 1, ExpectedCredentialVersion: 1,
		Purpose: usercmd.MFAEnable, BoundSessionID: f.ID, ExpectedSessionVersion: 1, Credential: key, ChangedAt: now, Audit: audit("create")}
	// An audit collision must roll back key, policy, authority and revocation.
	if err := s.ApplyMFAChange(t.Context(), change); err == nil {
		t.Fatal("accepted duplicate audit")
	}
	profile, err = s.GetMFAProfile(t.Context(), u.ID)
	if err != nil || profile.Enabled {
		t.Fatalf("failed transaction enabled MFA: %+v, %v", profile, err)
	}
	loaded, found, err := s.GetSessionByAccessSelector(t.Context(), f.Access.Selector)
	if err != nil || !found || !loaded.Family.RevokedAt.IsZero() || loaded.User.Version != 1 {
		t.Fatalf("failed mutation changed authority: %+v, %t, %v", loaded, found, err)
	}
	change.Audit = audit("enable")
	if err := s.ApplyMFAChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	loaded, found, err = s.GetSessionByAccessSelector(t.Context(), f.Access.Selector)
	if err != nil || !found || loaded.Family.RevokedAt.IsZero() || loaded.User.Version != 2 || loaded.User.Credential.Version != 2 {
		t.Fatalf("enable did not revoke/advance: %+v, %t, %v", loaded, found, err)
	}
	f.ID, f.Access.Selector, f.RefreshTokens[0].Selector, f.CredentialVersion = "session-2", "access-2", "refresh-2", 2
	if err := s.CreateSession(t.Context(), f, audit("bypass")); !errors.Is(err, usercmd.ErrSessionUnavailable) {
		t.Fatalf("password-only insertion for enrolled admin: %v", err)
	}
	f.MFAFactorID, f.WebAuthnVerifiedAt = key.ID, now
	v := usercmd.MFAVerification{UserID: u.ID, ExpectedUserVersion: 2, ExpectedCredentialVersion: 2,
		ExpectedMFAVersion: 1, Credential: key, Session: &f, VerifiedAt: now, Audit: audit("factor-login")}
	if err := s.VerifyMFACredential(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	// Synced zero counters are valid, but a nonzero counter cannot return to zero.
	counter := v
	counter.Session, counter.Credential.SignCount = nil, 5
	counter.Audit = audit("counter-five")
	if err := s.VerifyMFACredential(t.Context(), counter); err != nil {
		t.Fatal(err)
	}
	counter.ExpectedSignCount, counter.Credential.SignCount, counter.Audit = 5, 0, audit("counter-regression")
	if err := s.VerifyMFACredential(t.Context(), counter); !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("counter regression: %v", err)
	}
	profile, err = s.GetMFAProfile(t.Context(), u.ID)
	if err != nil || profile.Credential.SignCount != 5 {
		t.Fatalf("counter rollback: %+v, %v", profile, err)
	}
	v.ExpectedSignCount, v.Credential.SignCount = 5, 6
	loaded, found, err = s.GetSessionByAccessSelector(t.Context(), f.Access.Selector)
	if err != nil || !found || loaded.Family.MFAFactorID != key.ID || !loaded.Family.WebAuthnVerifiedAt.Equal(now) {
		t.Fatalf("proof persistence: %+v, %t, %v", loaded, found, err)
	}
	// A pre-recovery pending login must not survive the authority transition.
	ceremony.Purpose, ceremony.SessionID, ceremony.UserVersion, ceremony.CredentialVersion, ceremony.MFAVersion = usercmd.MFALogin, "", 2, 2, 1
	ceremony.TokenDigest = bytes.Repeat([]byte{5}, 32)
	if err := s.CreateMFACeremony(t.Context(), ceremony); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(t.Context(), f.ID, 1, now, "test revoke", audit("revoke")); err != nil {
		t.Fatal(err)
	}
	revokedChange := usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: 2, ExpectedCredentialVersion: 2, ExpectedMFAVersion: 1,
		Purpose: usercmd.MFADisable, BoundSessionID: f.ID, ExpectedSessionVersion: 1, ChangedAt: now, Audit: audit("revoked-disable")}
	if err := s.ApplyMFAChange(t.Context(), revokedChange); !errors.Is(err, usercmd.ErrSessionUnavailable) {
		t.Fatalf("revoked family still authorizes MFA change: %v", err)
	}
	change = usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: 2, ExpectedCredentialVersion: 2, ExpectedMFAVersion: 1,
		Purpose: usercmd.MFARecover, ChangedAt: now, Audit: audit("recover")}
	if err := s.ApplyMFAChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeMFACeremony(t.Context(), ceremony.TokenDigest, ceremony.BrowserDigest, ceremony.CSRFDigest, ceremony.Purpose, now); !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("stale ceremony: %v", err)
	}
	profile, err = s.GetMFAProfile(t.Context(), u.ID)
	if err != nil || profile.Enabled || profile.Version != 2 {
		t.Fatalf("recovered profile: %+v, %v", profile, err)
	}
	secret, found, err := s.GetCredentialSecret(t.Context(), u.ID)
	if err != nil || !found || secret.PasswordHash != contractSecret(u.ID).PasswordHash {
		t.Fatal("recovery changed password")
	}
	if count, err := s.DeleteExpiredMFACeremonies(t.Context(), now.Add(2*time.Minute), 100); err != nil || count != 2 {
		t.Fatalf("cleanup count=%d error=%v", count, err)
	}
}
