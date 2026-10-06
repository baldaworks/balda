package balda

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

func TestMCPStartupChecksRetainedCredentialsBeforeProviderAndIngress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	p, err := state.NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	s, err := mcpmanage.New(key)
	if err != nil {
		t.Fatal(err)
	}
	// A current public revision must not hide protected data used by old pins.
	persistStartupMCPRevisions(t, p, s)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = state.NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, tc := range []struct {
		name, key string
		blocked   bool
	}{
		{"matching deployment key", key, false},
		{"missing deployment key", "", true},
		{"wrong deployment key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := mcpmanage.New(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			stages := applicationLifecycleStages(applicationLifecycleParams{MCPManagement: s, StateProvider: p}, &telegramLifecycle{})
			var readiness lifecycleStage
			for _, stage := range stages {
				if stage.name == "managed MCP credential readiness" {
					readiness = stage
				}
			}
			if readiness.start == nil {
				t.Fatal("application has no MCP credential startup check")
			}
			providerStarted, ingressStarted := false, false
			coordinator := newApplicationLifecycle(zerolog.Nop(), []lifecycleStage{readiness,
				{name: "provider", start: func(context.Context) error { providerStarted = true; return nil }},
				{name: "ingress", start: func(context.Context) error { ingressStarted = true; return nil }},
			})
			err = coordinator.Start(t.Context())
			if tc.blocked {
				if !errors.Is(err, mcpcmd.ErrCredentials) || providerStarted || ingressStarted {
					t.Fatalf("unprotected startup reached execution: %v", err)
				}
			} else if err != nil || !providerStarted || !ingressStarted {
				t.Fatalf("valid protected startup failed: %v", err)
			}
			if err := coordinator.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func persistStartupMCPRevisions(t *testing.T, p state.Provider, s *mcpmanage.Service) {
	t.Helper()
	now := time.Now().UTC()
	audit := func(id string, action usercmd.AuditAction, target usercmd.AuditTargetType, targetID string) usercmd.AuditEvent {
		return usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
			TargetType: target, TargetID: targetID, Source: "mcp-startup-fixture", OccurredAt: now}
	}
	u := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin",
		Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Version: 1,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: u.ID, PasswordHash: "fixture-hash"}, audit("create-admin", usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, u.ID)); err != nil {
		t.Fatal(err)
	}
	f := usercmd.SessionFamily{ID: "family", UserID: u.ID, Version: 1, CredentialVersion: 1, Assurance: usercmd.SessionAssuranceNormal,
		Access:             usercmd.AccessCredential{Selector: "access", VerifierDigest: []byte("access-digest"), ExpiresAt: now.Add(15 * time.Minute)},
		CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour),
		RefreshTokens: []usercmd.RefreshToken{{Selector: "refresh", VerifierDigest: []byte("refresh-digest"), Generation: 1,
			State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}},
	}
	if err := p.Users().CreateSession(t.Context(), f, audit("create-session", usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, f.ID)); err != nil {
		t.Fatal(err)
	}
	r, err := s.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "worker", ID: "protected", CreatedAt: now,
		Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp", Targets: mcpcmd.Targets{All: true}}},
		mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{"Authorization": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "retained-worker-credential-fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	event := audit("create-mcp", usercmd.AuditActionMCPDefinitionChanged, usercmd.AuditTargetMCP, r.ConnectionID)
	event.ActorUserID, event.ActorSessionID = u.ID, f.ID
	m := state.MCPMutation{Connection: mcpcmd.Connection{ID: r.ConnectionID, PublicID: "worker", Source: mcpcmd.SourceManaged,
		CurrentRevisionID: r.ID, Enabled: true, CreatedAt: now, UpdatedAt: now}, Revision: &r, Audit: event,
		Authority: mcpcmd.Authority{UserID: u.ID, UserVersion: 1, CredentialVersion: 1, SessionID: f.ID, SessionVersion: 1, At: now}}
	if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	next := r
	next.ID = "public-current"
	next, err = s.PrepareRevision(&r, next, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{"Authorization": {Operation: mcpcmd.ValueRemove}}})
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectedVersion, m.Revision, m.Connection.CurrentRevisionID, m.Audit.ID = 1, &next, next.ID, "update-mcp"
	if err := p.MCP().SaveMCPConnection(t.Context(), m); err != nil {
		t.Fatal(err)
	}
}
