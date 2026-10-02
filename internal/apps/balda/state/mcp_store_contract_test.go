//go:build integration && (sqlite || postgres)

package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkMCPRevisionsSurviveRestart(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "state.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	m := contractMCPMutation(t, p)
	store := p.MCP()
	if store == nil {
		t.Fatal("provider cannot persist MCP definitions")
	}
	if err := store.SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	first := *m.Revision
	m.ExpectedVersion = 1
	m.Connection.CurrentRevisionID = "revision-2"
	m.Revision = &mcpcmd.Revision{ConnectionID: m.Connection.ID, ID: "revision-2", Definition: first.Definition,
		ProtectedValues: []byte("opaque-protected-revision-2"), CreatedAt: first.CreatedAt}
	m.Revision.Definition.URL = "https://second.example/mcp"
	m.Audit.ID = "mcp-edit"
	if err := store.SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	closeContractProvider(t, p)
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeContractProvider(t, p)
	c, found, err := p.MCP().GetMCPConnection(t.Context(), m.Connection.ID)
	if err != nil || !found || c.Version != 2 || c.PublishedVersion != 0 || c.CurrentRevisionID != "revision-2" {
		t.Fatalf("saved connection after restart = %+v, %v, %v", c, found, err)
	}
	retained, err := p.MCP().ListMCPRevisions(t.Context())
	if err != nil || len(retained) != 2 {
		t.Fatalf("historical credential readiness inventory = %d revisions, %v", len(retained), err)
	}
	for _, want := range []mcpcmd.Revision{first, *m.Revision} {
		got, found, err := p.MCP().GetMCPRevision(t.Context(), want.ConnectionID, want.ID)
		if err != nil || !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("exact revision changed: found=%v error=%v", found, err)
		}
		public, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(public, want.ProtectedValues) {
			t.Fatal("protected payload reached public JSON")
		}
	}
}

func contractMCPMutation(t *testing.T, p Provider) MCPMutation {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	u := contractUser("mcp-admin", "mcp-admin", false, now)
	if err := p.Users().CreateUser(t.Context(), u, contractSecret(u.ID), contractAudit("mcp-admin-create", usercmd.AuditActionUserCreated, u.ID, now)); err != nil {
		t.Fatal(err)
	}
	f := contractSessionFamily(u.ID, now)
	if err := p.Users().CreateSession(t.Context(), f, contractAudit("mcp-admin-login", usercmd.AuditActionLoginSucceeded, f.ID, now)); err != nil {
		t.Fatal(err)
	}
	return MCPMutation{
		Connection: mcpcmd.Connection{ID: "connection-1", PublicID: "worker-tools", Source: mcpcmd.SourceManaged,
			CurrentRevisionID: "revision-1", Enabled: true, CreatedAt: now, UpdatedAt: now},
		Revision: &mcpcmd.Revision{ConnectionID: "connection-1", ID: "revision-1", CreatedAt: now,
			Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://first.example/mcp", Targets: mcpcmd.Targets{All: true},
				Headers: map[string]mcpcmd.ValueBinding{"Authorization": {Kind: mcpcmd.ValueProtected}}},
			ProtectedValues: []byte("opaque-protected-revision-1")},
		Authority: mcpcmd.Authority{UserID: u.ID, UserVersion: u.Version, CredentialVersion: u.Credential.Version,
			SessionID: f.ID, SessionVersion: f.Version, At: now, FreshProofAge: 5 * time.Minute},
		Audit: usercmd.AuditEvent{ID: "mcp-create", ActorUserID: u.ID, ActorSessionID: f.ID, Action: "mcp.definition.changed",
			TargetType: "mcp", TargetID: "connection-1", Outcome: usercmd.AuditOutcomeSucceeded, Source: "provider-contract", OccurredAt: now},
	}
}

func checkMCPMutationIsAtomic(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPMutation(t, p)
	s := p.MCP()
	if err := s.SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	want, found, err := s.GetMCPConnection(t.Context(), m.Connection.ID)
	if err != nil || !found {
		t.Fatalf("created definition not found: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*MCPMutation)
	}{
		{"stale version", func(m *MCPMutation) { m.ExpectedVersion = 0 }},
		{"source change", func(m *MCPMutation) { m.Connection.Source = mcpcmd.SourceConfig }},
		{"public identity change", func(m *MCPMutation) { m.Connection.PublicID = "other-tools" }},
		{"duplicate audit", func(m *MCPMutation) { m.Audit.ID = "mcp-create" }},
		{"revision overwrite", func(m *MCPMutation) { m.Connection.CurrentRevisionID = "revision-1"; m.Revision.ID = "revision-1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update := m
			r := *m.Revision
			update.Revision = &r
			update.ExpectedVersion = 1
			update.Connection.CurrentRevisionID, r.ID = "rejected-revision", "rejected-revision"
			update.Audit.ID = "rejected-audit"
			tc.change(&update)
			if err := s.SaveMCPConnection(t.Context(), update); !errors.Is(err, mcpcmd.ErrConflict) {
				t.Fatalf("mutation error = %v, want conflict", err)
			}
			got, found, err := s.GetMCPConnection(t.Context(), m.Connection.ID)
			if err != nil || !found || got != want {
				t.Fatalf("rejected mutation changed connection: %v", err)
			}
			_, found, err = s.GetMCPRevision(t.Context(), m.Connection.ID, "rejected-revision")
			if err != nil || found {
				t.Fatalf("rejected mutation retained a revision: %v", err)
			}
		})
	}
	duplicate := m
	duplicate.Connection.ID, duplicate.Connection.Source = "config-side-record", mcpcmd.SourceConfig
	r := *m.Revision
	r.ConnectionID = duplicate.Connection.ID
	duplicate.Revision = &r
	duplicate.Audit.ID, duplicate.Audit.TargetID = "duplicate-public-id", duplicate.Connection.ID
	if err := s.SaveMCPConnection(t.Context(), duplicate); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("public-ID collision = %v", err)
	}
	all, err := s.ListMCPConnections(t.Context())
	if err != nil || len(all) != 1 {
		t.Fatalf("public-ID collision changed inventory: %v", err)
	}
	audits, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil || len(audits.Events) != 3 {
		t.Fatalf("failed mutations changed audit history: %v", err)
	}
}

func checkMCPAuthorityFences(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPMutation(t, p)
	if err := p.MCP().CheckMCPAuthority(t.Context(), m.Authority); err != nil {
		t.Fatalf("valid MCP preflight rejected: %v", err)
	}
	for _, change := range []func(*mcpcmd.Authority){
		func(a *mcpcmd.Authority) { a.At = time.Time{} }, func(a *mcpcmd.Authority) { a.FreshProofAge = 0 }, func(a *mcpcmd.Authority) { a.UserID = "" }, func(a *mcpcmd.Authority) { a.SessionID = "" }, func(a *mcpcmd.Authority) { a.UserVersion = 0 }, func(a *mcpcmd.Authority) { a.CredentialVersion = 0 }, func(a *mcpcmd.Authority) { a.SessionVersion = 0 },
	} {
		a := m.Authority
		change(&a)
		if err := p.MCP().CheckMCPAuthority(t.Context(), a); !errors.Is(err, mcpcmd.ErrInvalid) {
			t.Fatalf("invalid preflight accepted: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*MCPMutation)
		want   error
	}{
		{"stale user", func(m *MCPMutation) { m.Authority.UserVersion++ }, mcpcmd.ErrConflict},
		{"stale credential", func(m *MCPMutation) { m.Authority.CredentialVersion++ }, mcpcmd.ErrConflict},
		{"stale factor", func(m *MCPMutation) { m.Authority.MFAVersion++ }, mcpcmd.ErrConflict},
		{"stale family", func(m *MCPMutation) { m.Authority.SessionVersion++ }, mcpcmd.ErrConflict},
		{"unknown family", func(m *MCPMutation) { m.Authority.SessionID = "missing"; m.Audit.ActorSessionID = "missing" }, mcpcmd.ErrForbidden},
		{"expired access", func(m *MCPMutation) {
			m.Authority.At = m.Authority.At.Add(15 * time.Minute)
			m.Audit.OccurredAt = m.Authority.At
		}, mcpcmd.ErrForbidden},
		{"audit actor mismatch", func(m *MCPMutation) { m.Audit.ActorUserID = "other-admin" }, mcpcmd.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := m
			tc.change(&bad)
			if err := p.MCP().SaveMCPConnection(t.Context(), bad); !errors.Is(err, tc.want) {
				t.Fatalf("authority error = %v, want %v", err, tc.want)
			}
			all, err := p.MCP().ListMCPConnections(t.Context())
			if err != nil || len(all) != 0 {
				t.Fatalf("rejected authority created a connection: %v", err)
			}
		})
	}
	u, found, err := p.Users().GetUser(t.Context(), m.Authority.UserID)
	if err != nil || !found {
		t.Fatal(err)
	}
	keeper := contractUser("remaining-admin", "remaining-admin", false, m.Authority.At)
	if err := p.Users().CreateUser(t.Context(), keeper, contractSecret(keeper.ID), contractAudit("remaining-admin-create", usercmd.AuditActionUserCreated, keeper.ID, m.Authority.At)); err != nil {
		t.Fatal(err)
	}
	u.Role, u.Version = usercmd.RoleOperator, u.Version+1
	if err := p.Users().UpdateUser(t.Context(), u, u.Version-1, contractAudit("demote-admin", usercmd.AuditActionUserRoleChanged, u.ID, m.Authority.At)); err != nil {
		t.Fatal(err)
	}
	m.Authority.UserVersion = u.Version
	if err := p.MCP().CheckMCPAuthority(t.Context(), m.Authority); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("demoted administrator could start MCP probe: %v", err)
	}
	if err := p.MCP().SaveMCPConnection(t.Context(), m); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("demoted administrator committed: %v", err)
	}
}

func checkMCPSelectionAndPublicationSurviveRestart(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "state.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	m := contractMCPMutation(t, p)
	if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := p.MCP().MarkMCPPublished(t.Context(), m.Connection.ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []bool{false, true} {
		m.ExpectedVersion++
		m.Revision = nil
		m.Connection.Enabled, m.Connection.Deleted = false, deleted
		m.Audit.ID += "-selection"
		if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.MCP().MarkMCPPublished(t.Context(), m.Connection.ID, 2); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("stale publication succeeded: %v", err)
	}
	closeContractProvider(t, p)
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeContractProvider(t, p)
	c, found, err := p.MCP().GetMCPConnection(t.Context(), m.Connection.ID)
	if err != nil || !found || c.Enabled || !c.Deleted || c.Version != 3 || c.PublishedVersion != 1 {
		t.Fatalf("selection/recovery state lost: %+v %v", c, err)
	}
	_, found, err = p.MCP().GetMCPRevision(t.Context(), m.Connection.ID, "revision-1")
	if err != nil || !found {
		t.Fatalf("delete removed pinned revision: %v", err)
	}
	if err := p.MCP().MarkMCPPublished(t.Context(), m.Connection.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := p.MCP().MarkMCPPublished(t.Context(), m.Connection.ID, 3); err != nil {
		t.Fatalf("publication retry: %v", err)
	}
}

func checkMCPConcurrentEditsCommitOnce(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPMutation(t, p)
	if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m.ExpectedVersion, m.Revision, m.Connection.Enabled, m.Audit.ID = 1, nil, false, "concurrent-edit"
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			err := p.MCP().SaveMCPConnection(t.Context(), m)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, mcpcmd.ErrConflict) {
				t.Errorf("concurrent edit: %v", err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent commits = %d", successes.Load())
	}
	c, found, err := p.MCP().GetMCPConnection(t.Context(), m.Connection.ID)
	if err != nil || !found || c.Version != 2 || c.Enabled {
		t.Fatalf("concurrent edits changed state incorrectly: %v", err)
	}
}

func checkMCPProtectedBindingsAndSafeAudit(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPMutation(t, p)
	const secret = "fixture-secret-must-not-reach-public-storage"
	m.Revision.Definition.Headers["Authorization"] = mcpcmd.ValueBinding{Kind: mcpcmd.ValueProtected, Value: secret}
	if err := p.MCP().SaveMCPConnection(t.Context(), m); !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatalf("plaintext protected value accepted: %v", err)
	}
	_, found, err := p.MCP().GetMCPRevision(t.Context(), m.Connection.ID, m.Revision.ID)
	if err != nil || found {
		t.Fatalf("rejected plaintext retained a revision: %v", err)
	}
	m.Revision.Definition.Headers["Authorization"] = mcpcmd.ValueBinding{Kind: mcpcmd.ValueProtected}
	m.Audit.Reason = secret
	if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	audits, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(audits)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("MCP mutation copied submitted material to audit")
	}
}

func checkMCPFreshFactorAndRevocation(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPMutation(t, p)
	now, uid := m.Authority.At, m.Authority.UserID
	key := usercmd.MFACredential{ID: "mcp-factor", UserID: uid, RPID: "localhost", CredentialID: []byte("public-credential"),
		PublicKey: []byte("public-key"), Data: []byte(`{"credential":"public"}`), CreatedAt: now}
	change := usercmd.MFAChange{UserID: uid, ExpectedUserVersion: 1, ExpectedCredentialVersion: 1,
		Purpose: usercmd.MFAEnable, BoundSessionID: m.Authority.SessionID, ExpectedSessionVersion: 1,
		Credential: key, ChangedAt: now, Audit: contractAudit("mcp-factor-enable", usercmd.AuditActionMFAEnabled, uid, now)}
	if err := p.Users().ApplyMFAChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	m.Authority.UserVersion, m.Authority.CredentialVersion, m.Authority.MFAVersion = 2, 2, 1
	if err := p.MCP().SaveMCPConnection(t.Context(), m); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("revoked password family accepted: %v", err)
	}
	f := contractSessionFamily(uid, now)
	f.ID, f.Access.Selector, f.RefreshTokens[0].Selector = "factor-session", "factor-access", "factor-refresh"
	f.CredentialVersion, f.MFAFactorID, f.WebAuthnVerifiedAt = 2, key.ID, now
	v := usercmd.MFAVerification{UserID: uid, ExpectedUserVersion: 2, ExpectedCredentialVersion: 2,
		ExpectedMFAVersion: 1, Credential: key, Session: &f, VerifiedAt: now,
		Audit: contractAudit("mcp-factor-login", usercmd.AuditActionMFAVerified, f.ID, now)}
	if err := p.Users().VerifyMFACredential(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	m.Authority.SessionID, m.Audit.ActorSessionID = f.ID, f.ID
	expired := m
	expired.Authority.At = now.Add(expired.Authority.FreshProofAge)
	expired.Audit.OccurredAt = expired.Authority.At
	if err := p.MCP().CheckMCPAuthority(t.Context(), expired.Authority); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("stale factor preflight accepted: %v", err)
	}
	if err := p.MCP().SaveMCPConnection(t.Context(), expired); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("stale factor proof accepted: %v", err)
	}
	if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatalf("fresh factor proof rejected: %v", err)
	}
	if err := p.Users().RevokeSession(t.Context(), f.ID, f.Version, now, "test revocation", contractAudit("mcp-factor-revoke", usercmd.AuditActionSessionRevoked, f.ID, now)); err != nil {
		t.Fatal(err)
	}
	m.ExpectedVersion, m.Revision, m.Connection.Enabled, m.Audit.ID = 1, nil, false, "revoked-mutation"
	if err := p.MCP().SaveMCPConnection(t.Context(), m); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("revoked factor family accepted: %v", err)
	}
	c, found, err := p.MCP().GetMCPConnection(t.Context(), m.Connection.ID)
	if err != nil || !found || !c.Enabled || c.Version != 1 {
		t.Fatalf("revoked family changed state: %v", err)
	}
}
