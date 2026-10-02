package mcpfx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

func TestDefinitionServiceUsesCanonicalAuthorityAtCommit(t *testing.T) {
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	now := time.Now().UTC()
	u := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	audit := func(id string, action usercmd.AuditAction, target usercmd.AuditTargetType, targetID string) usercmd.AuditEvent {
		return usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: target, TargetID: targetID, Source: "mcp-definition-fixture", OccurredAt: now}
	}
	if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: u.ID, PasswordHash: "fixture-hash"}, audit("create-admin", usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, u.ID)); err != nil {
		t.Fatal(err)
	}
	f := usercmd.SessionFamily{ID: "browser", UserID: u.ID, Version: 1, CredentialVersion: 1, Assurance: usercmd.SessionAssuranceNormal, Access: usercmd.AccessCredential{Selector: "access", VerifierDigest: []byte("access-digest"), ExpiresAt: now.Add(time.Minute)}, CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour), RefreshTokens: []usercmd.RefreshToken{{Selector: "refresh", VerifierDigest: []byte("refresh-digest"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	if err := p.Users().CreateSession(t.Context(), f, audit("create-browser", usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, f.ID)); err != nil {
		t.Fatal(err)
	}
	credentials, err := mcpmanage.New("")
	if err != nil {
		t.Fatal(err)
	}
	configured := NewConfiguredDefinitions(nil, map[string]agentconfig.Config{"hosted": {}}, nil)
	probe, err := NewManagedProbe(credentials, NewClientLauncher())
	if err != nil {
		t.Fatal(err)
	}
	service, err := mcpmanage.NewDefinitions(credentials, NewDefinitionStore(p.MCP()), configured, definitionCommitOnly{}, probe)
	if err != nil {
		t.Fatal(err)
	}
	request := mcpcmd.CreateDefinition{PublicID: "worker", Enabled: true, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: "worker-mcp", Targets: mcpcmd.Targets{All: true}}, Authority: mcpcmd.Authority{UserID: u.ID, UserVersion: 1, CredentialVersion: 1, SessionID: f.ID, SessionVersion: 1, At: now, FreshProofAge: time.Minute}}
	created, err := service.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != mcpcmd.StatusPending {
		t.Fatal("commit without publication claimed readiness")
	}
	if err := p.MCP().CheckMCPAuthority(t.Context(), request.Authority); err != nil {
		t.Fatalf("valid canonical preflight rejected: %v", err)
	}
	for _, change := range []func(*mcpcmd.Authority){func(a *mcpcmd.Authority) { a.At = time.Time{} }, func(a *mcpcmd.Authority) { a.FreshProofAge = 0 }, func(a *mcpcmd.Authority) { a.SessionVersion = 0 }, func(a *mcpcmd.Authority) { a.UserID = "" }} {
		a := request.Authority
		change(&a)
		if err := p.MCP().CheckMCPAuthority(t.Context(), a); !errors.Is(err, mcpcmd.ErrInvalid) {
			t.Fatalf("invalid preflight envelope accepted: %v", err)
		}
	}
	update := mcpcmd.UpdateDefinition{ConnectionID: created.Connection.ID, ExpectedVersion: 1, Definition: created.Definition, Enabled: true, Authority: request.Authority}
	// The request was authorized before its browser family was revoked.
	if err := p.Users().RevokeSession(t.Context(), f.ID, 1, now, "revoked", audit("revoke-browser", usercmd.AuditActionSessionRevoked, usercmd.AuditTargetSession, f.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(t.Context(), update); !errors.Is(err, mcpcmd.ErrConflict) && !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("stale administrator write accepted: %v", err)
	}
	// The same canonical family fence must run before a candidate launches.
	if _, err := service.Probe(t.Context(), request); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("revoked browser could probe: %v", err)
	}
	connection, found, err := p.MCP().GetMCPConnection(t.Context(), created.Connection.ID)
	if err != nil || !found || connection.Version != 1 || connection.CurrentRevisionID != created.Connection.CurrentRevisionID {
		t.Fatal("authority failure changed durable selection")
	}
	revisions, err := p.MCP().ListMCPRevisions(t.Context())
	if err != nil || len(revisions) != 1 {
		t.Fatal("authority failure inserted a revision")
	}
}

type definitionCommitOnly struct{}

func (definitionCommitOnly) PublishMCP(_ context.Context, commit func() error) error { return commit() }
func (definitionCommitOnly) MCPHealth(context.Context, mcpcmd.Connection) (mcpcmd.Status, int, error) {
	return mcpcmd.StatusPending, 0, nil
}
