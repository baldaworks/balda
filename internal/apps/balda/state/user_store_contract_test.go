//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkUserStoreCanonicalLifecycle(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)

	first := contractUser("admin-1", "admin.one", true, now)
	second := contractUser("admin-2", "admin.two", false, now)
	if err := store.CreateUser(t.Context(), first, contractSecret(first.ID), contractAudit("audit-create-1", usercmd.AuditActionCredentialChanged, first.ID, now)); err != nil {
		t.Fatalf("CreateUser(first) error = %v", err)
	}
	if err := store.CreateUser(t.Context(), second, contractSecret(second.ID), contractAudit("audit-create-2", usercmd.AuditActionCredentialChanged, second.ID, now)); err != nil {
		t.Fatalf("CreateUser(second) error = %v", err)
	}
	duplicate := contractUser("duplicate", first.NormalizedUsername, false, now)
	if err := store.CreateUser(t.Context(), duplicate, contractSecret(duplicate.ID), contractAudit("audit-duplicate", usercmd.AuditActionCredentialChanged, duplicate.ID, now)); !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("CreateUser(duplicate) error = %v, want ErrConflict", err)
	}

	got, found, err := store.GetUserByNormalizedUsername(t.Context(), first.NormalizedUsername)
	if err != nil || !found || got.ID != first.ID || got.Binding != nil {
		t.Fatalf("GetUserByNormalizedUsername() = %+v, %t, %v", got, found, err)
	}
	secret, found, err := store.GetCredentialSecret(t.Context(), first.ID)
	if err != nil || !found || secret.PasswordHash != contractSecret(first.ID).PasswordHash {
		t.Fatalf("GetCredentialSecret() = %+v, %t, %v", secret, found, err)
	}
	page, err := store.ListUsers(t.Context(), usercmd.PageRequest{Limit: 1})
	if err != nil || len(page.Users) != 1 || page.NextAfterID == "" {
		t.Fatalf("ListUsers(first page) = %+v, %v", page, err)
	}
	next, err := store.ListUsers(t.Context(), usercmd.PageRequest{AfterID: page.NextAfterID, Limit: 1})
	if err != nil || len(next.Users) != 1 || next.Users[0].ID == page.Users[0].ID {
		t.Fatalf("ListUsers(second page) = %+v, %v", next, err)
	}

	claim := usercmd.BindingClaim{ID: "claim-1", UserID: first.ID, ChannelType: "telegram", ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateBindingClaim(t.Context(), claim, now, contractAudit("audit-claim", usercmd.AuditActionBindingAttached, first.ID, now)); err != nil {
		t.Fatalf("CreateBindingClaim() error = %v", err)
	}
	binding := usercmd.Binding{
		ID: "binding-1", UserID: first.ID, ChannelType: "telegram", Principal: "101",
		DisplayName: "Admin One", Provenance: "verified", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.AttachBinding(t.Context(), claim.ID, binding, now.Add(time.Minute), contractAudit("audit-binding", usercmd.AuditActionBindingAttached, binding.ID, now)); err != nil {
		t.Fatalf("AttachBinding() error = %v", err)
	}
	bound, found, err := store.GetUserByBinding(t.Context(), "telegram", "101")
	if err != nil || !found || bound.ID != first.ID || bound.Binding == nil || bound.Binding.ID != binding.ID {
		t.Fatalf("GetUserByBinding() = %+v, %t, %v", bound, found, err)
	}
	if err := store.AttachBinding(t.Context(), claim.ID, binding, now.Add(2*time.Minute), contractAudit("audit-binding-reuse", usercmd.AuditActionBindingAttached, binding.ID, now)); !errors.Is(err, usercmd.ErrBindingClaimUnavailable) {
		t.Fatalf("AttachBinding(reused claim) error = %v, want ErrBindingClaimUnavailable", err)
	}

	demoted := second
	demoted.Role = usercmd.RoleOperator
	demoted.Version = 2
	if err := store.UpdateUser(t.Context(), demoted, 99, contractAudit("audit-stale", usercmd.AuditActionUserRoleChanged, second.ID, now)); !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("UpdateUser(stale) error = %v, want ErrConflict", err)
	}
	if err := store.UpdateUser(t.Context(), demoted, 1, contractAudit("audit-demote-2", usercmd.AuditActionUserRoleChanged, second.ID, now)); err != nil {
		t.Fatalf("UpdateUser(second demotion) error = %v", err)
	}
	lastAdmin := first
	lastAdmin.Status = usercmd.StatusDisabled
	lastAdmin.Version = 2
	if err := store.UpdateUser(t.Context(), lastAdmin, 1, contractAudit("audit-disable-1", usercmd.AuditActionUserStatusChanged, first.ID, now)); !errors.Is(err, usercmd.ErrLastAdministrator) {
		t.Fatalf("UpdateUser(last administrator) error = %v, want ErrLastAdministrator", err)
	}

	type updateOutcome struct{ err error }
	updates := make(chan updateOutcome, 2)
	var updateWG sync.WaitGroup
	for i := range 2 {
		updateWG.Add(1)
		go func() {
			defer updateWG.Done()
			candidate := first
			candidate.DisplayName = fmt.Sprintf("Concurrent %d", i)
			candidate.Version = 2
			candidate.UpdatedAt = now.Add(time.Duration(i+1) * time.Minute)
			updates <- updateOutcome{err: store.UpdateUser(
				t.Context(), candidate, 1,
				contractAudit(fmt.Sprintf("audit-concurrent-%d", i), usercmd.AuditActionUserStatusChanged, first.ID, candidate.UpdatedAt),
			)}
		}()
	}
	updateWG.Wait()
	close(updates)
	var updateSuccesses, updateConflicts int
	for outcome := range updates {
		switch {
		case outcome.err == nil:
			updateSuccesses++
		case errors.Is(outcome.err, usercmd.ErrConflict):
			updateConflicts++
		default:
			t.Fatalf("UpdateUser(concurrent) error = %v", outcome.err)
		}
	}
	if updateSuccesses != 1 || updateConflicts != 1 {
		t.Fatalf("concurrent updates successes/conflicts = %d/%d, want 1/1", updateSuccesses, updateConflicts)
	}

	rolledBack := contractUser("rolled-back", "rolled.back", false, now)
	err = store.CreateUser(
		t.Context(), rolledBack, contractSecret(rolledBack.ID),
		contractAudit("audit-create-1", usercmd.AuditActionCredentialChanged, rolledBack.ID, now),
	)
	if !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("CreateUser(duplicate audit) error = %v, want ErrConflict", err)
	}
	if _, found, err := store.GetUser(t.Context(), rolledBack.ID); err != nil || found {
		t.Fatalf("audit-failed user found = %t, error = %v", found, err)
	}

	auditPage, err := store.ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(auditPage.Events) != 6 {
		t.Fatalf("committed audit events = %d, want 6", len(auditPage.Events))
	}
}

func checkUserStoreRefreshRotationAndReplay(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)
	user := contractUser("admin-1", "admin.one", true, now)
	if err := store.CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit("audit-create", usercmd.AuditActionCredentialChanged, user.ID, now)); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	family := contractSessionFamily(user.ID, now)
	if err := store.CreateSession(t.Context(), family, contractAudit("audit-login", usercmd.AuditActionCredentialChanged, family.ID, now)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	access, found, err := store.GetSessionByAccessSelector(t.Context(), family.Access.Selector)
	if err != nil || !found || access.Family.ID != family.ID || access.User.ID != user.ID {
		t.Fatalf("GetSessionByAccessSelector() = %+v, %t, %v", access, found, err)
	}

	rotation := usercmd.RefreshRotation{
		Selector: family.RefreshTokens[0].Selector, PresentedVerifierDigest: family.RefreshTokens[0].VerifierDigest,
		Access: usercmd.AccessCredential{Selector: "access-2", VerifierDigest: []byte("access-digest-2"), ExpiresAt: now.Add(30 * time.Minute)},
		Refresh: usercmd.RefreshToken{
			Selector: "refresh-2", VerifierDigest: []byte("refresh-digest-2"), Generation: 2,
			State: usercmd.RefreshTokenStateActive, IssuedAt: now.Add(15 * time.Minute), ExpiresAt: family.RefreshExpiresAt,
		},
		RotatedAt:    now.Add(15 * time.Minute),
		SuccessAudit: contractAudit("audit-refresh", usercmd.AuditActionSessionRevoked, family.ID, now.Add(15*time.Minute)),
		ReplayAudit:  contractAudit("audit-replay", usercmd.AuditActionSessionRevoked, family.ID, now.Add(16*time.Minute)),
	}
	result, err := store.RotateRefresh(t.Context(), rotation)
	if err != nil || result != usercmd.RefreshRotationSucceeded {
		t.Fatalf("RotateRefresh() = %q, %v", result, err)
	}
	refreshed, found, err := store.GetSessionByRefreshSelector(t.Context(), rotation.Refresh.Selector)
	if err != nil || !found || refreshed.Token.Generation != 2 || refreshed.Token.State != usercmd.RefreshTokenStateActive {
		t.Fatalf("GetSessionByRefreshSelector(new) = %+v, %t, %v", refreshed, found, err)
	}
	if refreshed.Family.RefreshExpiresAt != family.RefreshExpiresAt || len(refreshed.Family.RefreshTokens) != 2 {
		t.Fatalf("rotated family = %+v", refreshed.Family)
	}
	if refreshed.Family.RefreshTokens[0].State != usercmd.RefreshTokenStateUsed {
		t.Fatalf("old refresh state = %q, want used", refreshed.Family.RefreshTokens[0].State)
	}

	result, err = store.RotateRefresh(t.Context(), rotation)
	if err != nil || result != usercmd.RefreshRotationReplayRevoked {
		t.Fatalf("RotateRefresh(replay) = %q, %v", result, err)
	}
	revoked, found, err := store.GetSessionByRefreshSelector(t.Context(), rotation.Refresh.Selector)
	if err != nil || !found || revoked.Family.RevokedAt.IsZero() || revoked.Token.State != usercmd.RefreshTokenStateRevoked {
		t.Fatalf("replayed family = %+v, %t, %v", revoked, found, err)
	}

	result, err = store.RotateRefresh(t.Context(), usercmd.RefreshRotation{
		Selector: rotation.Refresh.Selector, PresentedVerifierDigest: []byte("wrong"), RotatedAt: now.Add(17 * time.Minute),
	})
	if err != nil || result != usercmd.RefreshRotationUnavailable {
		t.Fatalf("RotateRefresh(wrong verifier) = %q, %v", result, err)
	}

	deleted, err := store.DeleteExpiredSessions(t.Context(), family.RefreshExpiresAt.Add(time.Hour), 1)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpiredSessions() = %d, %v", deleted, err)
	}
	_, found, err = store.GetSessionByAccessSelector(t.Context(), rotation.Access.Selector)
	if err != nil || found {
		t.Fatalf("deleted session found = %t, error = %v", found, err)
	}
}

func checkUserStoreCredentialAndSessionRevocation(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	user := contractUser("admin-1", "admin.one", true, now)
	if err := store.CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit("audit-create", usercmd.AuditActionCredentialChanged, user.ID, now)); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	staleFamily := contractSessionFamily(user.ID, now)
	staleFamily.ID = "stale-session"
	staleFamily.CredentialVersion = 2
	if err := store.CreateSession(t.Context(), staleFamily, contractAudit("audit-stale-session", usercmd.AuditActionCredentialChanged, staleFamily.ID, now)); !errors.Is(err, usercmd.ErrSessionUnavailable) {
		t.Fatalf("CreateSession(stale credential) error = %v, want ErrSessionUnavailable", err)
	}

	family := contractSessionFamily(user.ID, now)
	if err := store.CreateSession(t.Context(), family, contractAudit("audit-login", usercmd.AuditActionCredentialChanged, family.ID, now)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	nextCredential := usercmd.Credential{State: usercmd.CredentialStateActive, Version: 2}
	nextSecret := usercmd.CredentialSecret{UserID: user.ID, PasswordHash: "replacement-hash"}
	changedAt := now.Add(time.Hour)
	conflictingSession := contractSessionFamily(user.ID, changedAt)
	conflictingSession.ID = "replacement-session"
	conflictingSession.CredentialVersion = 2
	conflictingSession.Access.Selector = family.Access.Selector
	conflictingSession.RefreshTokens[0].Selector = "replacement-refresh"
	err := store.ChangeCredentialAndCreateSession(t.Context(), usercmd.CredentialSessionChange{
		UserID: user.ID, ExpectedUserVersion: 1, ExpectedCredentialVersion: 1,
		Credential: nextCredential, Secret: nextSecret, RevokedAt: changedAt,
		Session:         conflictingSession,
		CredentialAudit: contractAudit("audit-password-rollback", usercmd.AuditActionCredentialChanged, user.ID, changedAt),
		SessionAudit:    contractAudit("audit-session-rollback", usercmd.AuditActionLoginSucceeded, conflictingSession.ID, changedAt),
	})
	if !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("ChangeCredentialAndCreateSession(conflict) error = %v, want ErrConflict", err)
	}
	unchanged, found, err := store.GetUser(t.Context(), user.ID)
	if err != nil || !found || unchanged.Version != 1 || unchanged.Credential.Version != 1 {
		t.Fatalf("GetUser(after rollback) = %+v, %t, %v", unchanged, found, err)
	}
	stillActive, found, err := store.GetSessionByAccessSelector(t.Context(), family.Access.Selector)
	if err != nil || !found || !stillActive.Family.RevokedAt.IsZero() {
		t.Fatalf("session after rollback = %+v, %t, %v", stillActive, found, err)
	}
	if err := store.ChangeCredential(
		t.Context(), user.ID, 1, 1, nextCredential, nextSecret, changedAt,
		contractAudit("audit-password", usercmd.AuditActionCredentialChanged, user.ID, changedAt),
	); err != nil {
		t.Fatalf("ChangeCredential() error = %v", err)
	}
	changed, found, err := store.GetUser(t.Context(), user.ID)
	if err != nil || !found || changed.Version != 2 || changed.Credential.Version != 2 {
		t.Fatalf("GetUser(changed credential) = %+v, %t, %v", changed, found, err)
	}
	secret, found, err := store.GetCredentialSecret(t.Context(), user.ID)
	if err != nil || !found || secret.PasswordHash != nextSecret.PasswordHash {
		t.Fatalf("GetCredentialSecret(changed) = %+v, %t, %v", secret, found, err)
	}
	revoked, found, err := store.GetSessionByAccessSelector(t.Context(), family.Access.Selector)
	if err != nil || !found || !revoked.Family.RevokedAt.Equal(changedAt) ||
		revoked.Family.RefreshTokens[0].State != usercmd.RefreshTokenStateRevoked {
		t.Fatalf("credential-revoked family = %+v, %t, %v", revoked, found, err)
	}

	second := contractSessionFamily(user.ID, changedAt.Add(time.Minute))
	second.ID = "session-2"
	second.CredentialVersion = 2
	second.Access.Selector = "access-session-2"
	second.RefreshTokens[0].Selector = "refresh-session-2"
	if err := store.CreateSession(t.Context(), second, contractAudit("audit-login-2", usercmd.AuditActionCredentialChanged, second.ID, second.CreatedAt)); err != nil {
		t.Fatalf("CreateSession(second) error = %v", err)
	}
	if err := store.RevokeSession(
		t.Context(), second.ID, 99, changedAt.Add(2*time.Minute), "administrative revocation",
		contractAudit("audit-revoke-stale", usercmd.AuditActionSessionRevoked, second.ID, changedAt),
	); !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("RevokeSession(stale) error = %v, want ErrConflict", err)
	}
	if err := store.RevokeSession(
		t.Context(), second.ID, 1, changedAt.Add(2*time.Minute), "administrative revocation",
		contractAudit("audit-revoke", usercmd.AuditActionSessionRevoked, second.ID, changedAt.Add(2*time.Minute)),
	); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	sessions, err := store.ListSessions(t.Context(), user.ID, usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil || len(sessions.Sessions) != 2 {
		t.Fatalf("ListSessions() = %+v, %v", sessions, err)
	}
	if sessions.Sessions[1].Version != 2 {
		t.Fatalf("revoked session version = %d, want 2", sessions.Sessions[1].Version)
	}
}

func checkUserStoreConcurrentRefreshReplay(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 23, 4, 0, 0, 0, time.UTC)
	user := contractUser("admin-1", "admin.one", true, now)
	if err := store.CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit("audit-create", usercmd.AuditActionCredentialChanged, user.ID, now)); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	family := contractSessionFamily(user.ID, now)
	if err := store.CreateSession(t.Context(), family, contractAudit("audit-login", usercmd.AuditActionCredentialChanged, family.ID, now)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	base := usercmd.RefreshRotation{
		Selector: family.RefreshTokens[0].Selector, PresentedVerifierDigest: family.RefreshTokens[0].VerifierDigest,
		Access: usercmd.AccessCredential{Selector: "access-2", VerifierDigest: []byte("access-digest-2"), ExpiresAt: now.Add(30 * time.Minute)},
		Refresh: usercmd.RefreshToken{
			Selector: "refresh-2", VerifierDigest: []byte("refresh-digest-2"), Generation: 2,
			State: usercmd.RefreshTokenStateActive, IssuedAt: now.Add(15 * time.Minute), ExpiresAt: family.RefreshExpiresAt,
		},
		RotatedAt:    now.Add(15 * time.Minute),
		SuccessAudit: contractAudit("audit-refresh", usercmd.AuditActionSessionRevoked, family.ID, now.Add(15*time.Minute)),
	}

	type outcome struct {
		result usercmd.RefreshRotationResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rotation := base
			rotation.ReplayAudit = contractAudit(
				fmt.Sprintf("audit-replay-%d", i), usercmd.AuditActionSessionRevoked, family.ID, now.Add(16*time.Minute),
			)
			result, err := store.RotateRefresh(t.Context(), rotation)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	wg.Wait()
	close(outcomes)
	var results []string
	for got := range outcomes {
		if got.err != nil {
			t.Fatalf("RotateRefresh(concurrent) error = %v", got.err)
		}
		results = append(results, string(got.result))
	}
	sort.Strings(results)
	want := []string{string(usercmd.RefreshRotationReplayRevoked), string(usercmd.RefreshRotationSucceeded)}
	if !slices.Equal(results, want) {
		t.Fatalf("concurrent refresh results = %v, want %v", results, want)
	}
}

func checkUserStoreMigrationBatch(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 23, 5, 0, 0, 0, time.UTC)
	admin := contractUser("migrated-admin", "telegram-101", true, now)
	admin.Credential = usercmd.Credential{State: usercmd.CredentialStateTemporary, MustChange: true, Version: 1}
	binding := usercmd.Binding{
		ID: "migrated-binding", UserID: admin.ID, ChannelType: "telegram", Principal: "101",
		DisplayName: "Legacy owner", Provenance: "legacy-owner", CreatedAt: now, UpdatedAt: now,
	}
	migration := usercmd.UserMigration{
		ID: "migration-1", SourceFingerprint: "fingerprint-1", SourceCountsJSON: `{"bindings":1,"users":1}`,
		PrimaryUserID: admin.ID, CompletedAt: now, GeneratedBindingCount: 1,
		Users: []usercmd.MigrationUser{{
			User: admin, Secret: usercmd.CredentialSecret{UserID: admin.ID, PasswordHash: "adaptive-hash"}, Binding: &binding,
			Audits: []usercmd.AuditEvent{contractAudit("audit-migration", usercmd.AuditActionUserMigrated, admin.ID, now)},
		}},
	}
	rollbackMigration := migration
	rollbackMigration.Users = append([]usercmd.MigrationUser(nil), migration.Users...)
	rollbackMigration.ID = "migration-rollback"
	rollbackMigration.SourceFingerprint = "fingerprint-rollback"
	rollbackMigration.Users[0].Audits = []usercmd.AuditEvent{
		contractAudit("audit-duplicate", usercmd.AuditActionUserMigrated, admin.ID, now),
		contractAudit("audit-duplicate", usercmd.AuditActionUserMigrated, binding.ID, now),
	}
	if _, err := store.ApplyUserMigration(t.Context(), rollbackMigration); !errors.Is(err, usercmd.ErrConflict) {
		t.Fatalf("ApplyUserMigration(duplicate audit) error = %v, want ErrConflict", err)
	}
	if _, found, err := store.GetUser(t.Context(), admin.ID); err != nil || found {
		t.Fatalf("rolled-back migrated user found=%t error=%v", found, err)
	}
	marked, err := store.UserMigrationApplied(t.Context(), rollbackMigration.SourceFingerprint)
	if err != nil || marked {
		t.Fatalf("rolled-back migration marker=%t error=%v", marked, err)
	}
	applied, err := store.ApplyUserMigration(t.Context(), migration)
	if err != nil || !applied {
		t.Fatalf("ApplyUserMigration() = %t, %v", applied, err)
	}
	marked, err = store.UserMigrationApplied(t.Context(), migration.SourceFingerprint)
	if err != nil || !marked {
		t.Fatalf("UserMigrationApplied() = %t, %v", marked, err)
	}
	anyMarked, err := store.AnyUserMigrationApplied(t.Context())
	if err != nil || !anyMarked {
		t.Fatalf("AnyUserMigrationApplied() = %t, %v", anyMarked, err)
	}
	bound, found, err := store.GetUserByBinding(t.Context(), "telegram", "101")
	if err != nil || !found || bound.ID != admin.ID || bound.Credential.State != usercmd.CredentialStateTemporary {
		t.Fatalf("GetUserByBinding() = %+v, %t, %v", bound, found, err)
	}
	sessions, err := store.ListSessions(t.Context(), admin.ID, usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil || len(sessions.Sessions) != 0 {
		t.Fatalf("ListSessions(migrated user) = %+v, %v", sessions, err)
	}
	applied, err = store.ApplyUserMigration(t.Context(), migration)
	if err != nil || applied {
		t.Fatalf("ApplyUserMigration(repeated) = %t, %v", applied, err)
	}
}

func contractUser(id, username string, primary bool, now time.Time) usercmd.User {
	return usercmd.User{
		ID: id, DisplayName: id, Username: username, NormalizedUsername: username,
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Primary:    primary, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func contractSecret(userID string) usercmd.CredentialSecret {
	return usercmd.CredentialSecret{UserID: userID, PasswordHash: "hash-" + userID}
}

func contractAudit(id string, action usercmd.AuditAction, targetID string, now time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: targetID, Source: "provider-contract", OccurredAt: now,
	}
}

func contractSessionFamily(userID string, now time.Time) usercmd.SessionFamily {
	expiresAt := now.Add(24 * time.Hour)
	return usercmd.SessionFamily{
		ID: "session-1", UserID: userID, Assurance: usercmd.SessionAssuranceNormal, CredentialVersion: 1,
		Access:             usercmd.AccessCredential{Selector: "access-1", VerifierDigest: []byte("access-digest-1"), ExpiresAt: now.Add(15 * time.Minute)},
		CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now,
		RefreshExpiresAt: expiresAt, Version: 1,
		RefreshTokens: []usercmd.RefreshToken{{
			Selector: "refresh-1", VerifierDigest: []byte("refresh-digest-1"), Generation: 1,
			State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: expiresAt,
		}},
	}
}
