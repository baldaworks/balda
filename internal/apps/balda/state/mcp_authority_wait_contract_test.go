//go:build integration && (sqlite || postgres)

package state

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const mcpProofExpiry = "proof"

func checkMCPAuthorityExpiresWhileWaitingForConnection(t *testing.T, factory func(*testing.T) contractOpener) {
	for _, tc := range []struct{ operation, expiry string }{
		{"definition", "access"}, {"grant", mcpProofExpiry}, {"preflight", "access"},
	} {
		t.Run(tc.operation+"/"+tc.expiry, func(t *testing.T) {
			checkMCPAuthorityExpiresWhileWaiting(t, factory(t), "database", tc.operation, tc.expiry)
		})
	}
}

// Each row tests a different reachable wait before privileged durable work.
func checkMCPAuthorityExpiresWhileWaitingForRow(t *testing.T, open contractOpener) {
	for _, tc := range []struct{ row, operation, expiry string }{
		{"canonical-user", "definition", "access"},
		{"family", "grant", mcpProofExpiry},
		{"connection", "definition", "access"},
		{"connection", "grant", "access"},
		{"grant", "grant", mcpProofExpiry},
		{"audit", "definition", "access"},
	} {
		t.Run(tc.row+"/"+tc.operation+"/"+tc.expiry, func(t *testing.T) {
			checkMCPAuthorityExpiresWhileWaiting(t, open, tc.row, tc.operation, tc.expiry)
		})
	}
}

func checkMCPAuthorityExpiresWhileWaiting(t *testing.T, open contractOpener, wait, operation, expiry string) {
	t.Helper()
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	grant := contractMCPGrant(t, p)
	if err := p.MCP().SaveMCPGrant(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	grant = authorizeContractGrant(grant)
	a := *grant.Authority
	if expiry == mcpProofExpiry {
		a = contractMCPFreshAuthority(t, p, a)
	}
	store := p.MCP().(*sqlMCPStore)
	db := contractDatabase(p).db
	deadline := time.Now().UTC().Add(time.Second)
	switch expiry {
	case "access":
		if _, err := db.ExecContext(t.Context(), store.users.bind(`UPDATE balda_backoffice_sessions SET access_expires_at = ? WHERE session_id = ?`), formatUserTime(deadline), a.SessionID); err != nil {
			t.Fatal(err)
		}
	case mcpProofExpiry:
		verified := deadline.Add(-time.Second)
		a.FreshProofAge = time.Second
		if _, err := db.ExecContext(t.Context(), store.users.bind(`UPDATE balda_backoffice_sessions SET webauthn_verified_at = ? WHERE session_id = ?`), formatUserTime(verified), a.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	a.At = time.Now().UTC()
	if err := p.MCP().CheckMCPAuthority(t.Context(), a); err != nil {
		t.Fatalf("authority was not live before queue: %v", err)
	}
	family, found, err := p.Users().GetSession(t.Context(), a.SessionID)
	if err != nil || !found {
		t.Fatal("missing queued authority family")
	}
	storedDeadline := family.Access.ExpiresAt
	if expiry == mcpProofExpiry {
		profile, err := p.Users().GetMFAProfile(t.Context(), a.UserID)
		if err != nil || !profile.Enabled || family.MFAFactorID != profile.Credential.ID {
			t.Fatal("queued authority did not have an enabled verified factor")
		}
		storedDeadline = family.WebAuthnVerifiedAt.Add(a.FreshProofAge)
	}
	if !storedDeadline.Equal(deadline) {
		t.Fatalf("stored expiry %s differs from fixture expiry %s", storedDeadline, deadline)
	}
	c, found, err := p.MCP().GetMCPConnection(t.Context(), grant.Grant.Binding.ConnectionID)
	if err != nil || !found {
		t.Fatal("missing initial connection")
	}
	r, found, err := p.MCP().GetMCPRevision(t.Context(), c.ID, c.CurrentRevisionID)
	if err != nil || !found {
		t.Fatal("missing initial revision")
	}
	c.CurrentRevisionID, r.ID = "queued-revision", "queued-revision"
	// A future metadata floor must not stand in for current security time.
	c.UpdatedAt, r.CreatedAt = deadline.Add(time.Hour), deadline.Add(time.Hour)
	grant.Authority, grant.Audit.OccurredAt = &a, a.At
	grant.Audit.ActorSessionID = a.SessionID
	grant.Grant.UpdatedAt = deadline.Add(time.Hour)
	audit := grant.Audit
	audit.ID, audit.Action = "queued-definition", usercmd.AuditActionMCPDefinitionChanged
	definition := MCPMutation{Connection: c, Revision: &r, ExpectedVersion: c.Version, Authority: a, Audit: audit}
	before := snapshotMCPAuthorityWaitState(t, p)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if wait == "database" {
		maxConnections := db.Stats().MaxOpenConnections
		db.SetMaxOpenConns(1)
		defer db.SetMaxOpenConns(maxConnections)
	}
	// Begin directly: the blocker must not take the PostgreSQL application
	// advisory lock, so the writer can reach the specific row being tested.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var blockerPID int
	if wait != "database" {
		if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
			t.Fatal(err)
		}
		if wait == "audit" {
			if err := store.users.insertAudit(ctx, tx, audit); err != nil {
				t.Fatal(err)
			}
		} else {
			lockMCPAuthorityWaitRow(t, ctx, store, tx, wait, a, c.ID, grant.Grant.ID)
		}
	}
	initialWaitCount := db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		switch operation {
		case "definition":
			done <- p.MCP().SaveMCPConnection(ctx, definition)
		case "grant":
			done <- p.MCP().SaveMCPGrant(ctx, grant)
		case "preflight":
			done <- p.MCP().CheckMCPAuthority(ctx, a)
		}
	}()
	observeMCPAuthorityWait(t, ctx, db, initialWaitCount, blockerPID, done)
	if !time.Now().Before(deadline) {
		t.Fatal("authority expired before the queue was observed")
	}
	timer := time.NewTimer(time.Until(deadline.Add(20 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releasedAt := time.Now().UTC()
	if releasedAt.Before(storedDeadline) {
		t.Fatalf("timer elapsed before actual stored expiry: release=%s expiry=%s", releasedAt, storedDeadline)
	}
	t.Logf("authority time=%s stored expiry=%s verified=%s proof age=%s release=%s", a.At, storedDeadline, family.WebAuthnVerifiedAt, a.FreshProofAge, releasedAt)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, mcpcmd.ErrForbidden) {
			t.Logf("authority time=%s deadline=%s completed=%s", a.At, deadline, time.Now().UTC())
			if expiry == mcpProofExpiry {
				profile, profileErr := p.Users().GetMFAProfile(t.Context(), a.UserID)
				family, found, familyErr := p.Users().GetSession(t.Context(), a.SessionID)
				t.Logf("MFA enabled=%v profile error=%v family found=%v family error=%v verified=%s proof age=%s", profile.Enabled, profileErr, found, familyErr, family.WebAuthnVerifiedAt, a.FreshProofAge)
			}
			t.Errorf("%s committed or returned wrong rejection after %s expired in %s queue: %v", operation, expiry, wait, err)
		}
	case <-ctx.Done():
		t.Fatal("queued authority check did not finish")
	}
	if after := snapshotMCPAuthorityWaitState(t, p); !reflect.DeepEqual(after, before) {
		t.Error("expired queued authority changed connection, revision, grant or audit state")
	}
}

func contractMCPFreshAuthority(t *testing.T, p Provider, a mcpcmd.Authority) mcpcmd.Authority {
	t.Helper()
	now := time.Now().UTC()
	key := usercmd.MFACredential{ID: "queue-factor", UserID: a.UserID, RPID: "localhost", CredentialID: []byte("public-credential"), PublicKey: []byte("public-key"), Data: []byte(`{"credential":"public"}`), CreatedAt: now}
	change := usercmd.MFAChange{UserID: a.UserID, ExpectedUserVersion: 1, ExpectedCredentialVersion: 1, Purpose: usercmd.MFAEnable, BoundSessionID: a.SessionID, ExpectedSessionVersion: 1, Credential: key, ChangedAt: now, Audit: contractAudit("queue-factor-enable", usercmd.AuditActionMFAEnabled, a.UserID, now)}
	if err := p.Users().ApplyMFAChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	f := contractSessionFamily(a.UserID, now)
	f.ID, f.Access.Selector, f.RefreshTokens[0].Selector = "queue-family", "queue-access", "queue-refresh"
	f.CredentialVersion, f.MFAFactorID, f.WebAuthnVerifiedAt = 2, key.ID, now
	v := usercmd.MFAVerification{UserID: a.UserID, ExpectedUserVersion: 2, ExpectedCredentialVersion: 2, ExpectedMFAVersion: 1, Credential: key, Session: &f, VerifiedAt: now, Audit: contractAudit("queue-factor-login", usercmd.AuditActionMFAVerified, f.ID, now)}
	if err := p.Users().VerifyMFACredential(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	a.UserVersion, a.CredentialVersion, a.MFAVersion, a.SessionID = 2, 2, 1, f.ID
	return a
}

func lockMCPAuthorityWaitRow(t *testing.T, ctx context.Context, s *sqlMCPStore, tx *sql.Tx, row string, a mcpcmd.Authority, connectionID, grantID string) {
	t.Helper()
	var query, id string
	switch row {
	case "canonical-user":
		query, id = "SELECT user_id FROM balda_users WHERE user_id = ?", a.UserID
	case "family":
		query, id = "SELECT session_id FROM balda_backoffice_sessions WHERE session_id = ?", a.SessionID
	case "connection":
		query, id = "SELECT connection_id FROM balda_mcp_connections WHERE connection_id = ?", connectionID
	case "grant":
		query, id = "SELECT grant_id FROM balda_mcp_grants WHERE grant_id = ?", grantID
	}
	var locked string
	if err := tx.QueryRowContext(ctx, s.users.bind(query)+s.users.forUpdate, id).Scan(&locked); err != nil {
		t.Fatal(err)
	}
}

func observeMCPAuthorityWait(t *testing.T, ctx context.Context, db *sql.DB, initialWaitCount int64, blockerPID int, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if blockerPID == 0 && db.Stats().WaitCount > initialWaitCount {
			return
		}
		if blockerPID != 0 {
			var blocked bool
			if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
		}
		select {
		case err := <-done:
			t.Fatalf("authority operation completed before expected database wait: %v", err)
		case <-ctx.Done():
			t.Fatal("authority operation did not reach expected database wait")
		case <-ticker.C:
		}
	}
}

type mcpAuthorityWaitState struct {
	connections []mcpcmd.Connection
	revisions   []mcpcmd.Revision
	grants      []mcpcmd.Grant
	audits      usercmd.AuditPage
}

func snapshotMCPAuthorityWaitState(t *testing.T, p Provider) mcpAuthorityWaitState {
	t.Helper()
	var snapshot mcpAuthorityWaitState
	var err error
	if snapshot.connections, err = p.MCP().ListMCPConnections(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot.revisions, err = p.MCP().ListMCPRevisions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot.grants, err = p.MCP().ListMCPGrants(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot.audits, err = p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
