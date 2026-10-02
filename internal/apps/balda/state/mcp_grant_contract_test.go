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

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func contractMCPGrant(t *testing.T, p Provider) MCPGrantMutation {
	t.Helper()
	connection := contractMCPMutation(t, p)
	if err := p.MCP().SaveMCPConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	a := connection.Authority
	return MCPGrantMutation{Grant: mcpcmd.Grant{ID: "worker-grant", Binding: mcpcmd.AuthBinding{ConnectionID: connection.Connection.ID, Resource: connection.Revision.Definition.URL, Issuer: "https://issuer.example.org", ClientID: "worker-client"}, Generation: 1, Status: mcpcmd.GrantAuthRequired, Scopes: []string{"tools"}, TokenEndpointAuthMethod: "none", CreatedAt: a.At, UpdatedAt: a.At}, Operation: mcpcmd.GrantRegister, Authority: &a, Audit: usercmd.AuditEvent{ID: "grant-register", Action: usercmd.AuditActionMCPAuthorizationChanged, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID, ActorSessionID: a.SessionID, TargetType: usercmd.AuditTargetMCP, TargetID: connection.Connection.ID, Source: "provider-contract", OccurredAt: a.At}}
}

func authorizeContractGrant(m MCPGrantMutation) MCPGrantMutation {
	m.ExpectedGeneration = m.Grant.Generation
	m.Grant.Generation++
	m.Grant.Status = mcpcmd.GrantAuthorized
	m.Grant.ProtectedValues = append([]byte{1}, bytes.Repeat([]byte{0x91}, 64)...)
	m.Operation = mcpcmd.GrantAuthorize
	m.Audit.ID = "grant-authorize"
	return m
}

func checkMCPGrantsSurviveRestart(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "state.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	m := contractMCPGrant(t, p)
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatalf("register worker client: %v", err)
	}
	m = authorizeContractGrant(m)
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	closeContractProvider(t, p)
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeContractProvider(t, p)
	got, found, err := p.MCP().GetMCPGrant(t.Context(), m.Grant.Binding)
	if err != nil || !found || !reflect.DeepEqual(got, m.Grant) {
		t.Fatalf("worker grant changed on reopen: found=%v, error=%v", found, err)
	}
	public, err := json.Marshal(got)
	if err != nil || bytes.Contains(public, []byte("protected_values")) {
		t.Fatal("protected grant material entered public metadata")
	}
	grants, err := p.MCP().ListMCPGrants(t.Context())
	if err != nil || len(grants) != 1 {
		t.Fatalf("credential readiness inventory: %v", err)
	}
	other := m.Grant.Binding
	other.Resource = "https://other.example.org/mcp"
	if _, found, err := p.MCP().GetMCPGrant(t.Context(), other); err != nil || found {
		t.Fatal("grant crossed resource boundary")
	}
}

func checkMCPGrantTransitionsAreAtomic(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPGrant(t, p)
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	authorized := authorizeContractGrant(m)
	for _, tc := range []struct {
		name   string
		change func(*MCPGrantMutation)
		want   error
	}{
		{"stale generation", func(m *MCPGrantMutation) { m.ExpectedGeneration = 0; m.Grant.Generation = 1 }, mcpcmd.ErrConflict},
		{"resource change", func(m *MCPGrantMutation) { m.Grant.Binding.Resource = "https://other.example.org/mcp" }, mcpcmd.ErrConflict},
		{"issuer change", func(m *MCPGrantMutation) { m.Grant.Binding.Issuer = "https://other-issuer.example.org" }, mcpcmd.ErrConflict},
		{"client change", func(m *MCPGrantMutation) { m.Grant.Binding.ClientID = "different-client" }, mcpcmd.ErrConflict},
		{"grant ID change", func(m *MCPGrantMutation) { m.Grant.ID = "different-grant" }, mcpcmd.ErrConflict},
		{"authorize registration method", func(m *MCPGrantMutation) { m.Grant.TokenEndpointAuthMethod = "client_secret_basic" }, mcpcmd.ErrConflict},
		{"authorize registration expiry", func(m *MCPGrantMutation) { m.Grant.ClientSecretExpiresAt = m.Grant.CreatedAt.AddDate(0, 0, 1) }, mcpcmd.ErrConflict},
		{"disconnect registration method", func(m *MCPGrantMutation) {
			m.Operation = mcpcmd.GrantDisconnect
			m.Grant.Status = mcpcmd.GrantDisconnected
			m.Grant.ProtectedValues = nil
			m.Grant.TokenEndpointAuthMethod = "client_secret_basic"
		}, mcpcmd.ErrConflict},
		{"disconnect registration expiry", func(m *MCPGrantMutation) {
			m.Operation = mcpcmd.GrantDisconnect
			m.Grant.Status = mcpcmd.GrantDisconnected
			m.Grant.ProtectedValues = nil
			m.Grant.ClientSecretExpiresAt = m.Grant.CreatedAt.AddDate(0, 0, 1)
		}, mcpcmd.ErrConflict},
		{"duplicate audit", func(m *MCPGrantMutation) { m.Audit.ID = "grant-register" }, mcpcmd.ErrConflict},
		{"stale administrator", func(m *MCPGrantMutation) { a := *m.Authority; a.UserVersion++; m.Authority = &a }, mcpcmd.ErrConflict},
		{"missing authority", func(m *MCPGrantMutation) { m.Authority = nil }, mcpcmd.ErrInvalid},
		{"missing protected tokens", func(m *MCPGrantMutation) { m.Grant.ProtectedValues = nil }, mcpcmd.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := authorized
			tc.change(&bad)
			if err := p.MCP().SaveMCPGrant(t.Context(), bad); !errors.Is(err, tc.want) {
				t.Fatalf("rejected transition=%v, want %v", err, tc.want)
			}
			got, found, err := p.MCP().GetMCPGrant(t.Context(), m.Grant.Binding)
			if err != nil || !found || !reflect.DeepEqual(got, m.Grant) {
				t.Fatal("rejected transition changed grant")
			}
		})
	}
	if err := p.MCP().SaveMCPGrant(t.Context(), authorized); err != nil {
		t.Fatal(err)
	}
	audits, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil || len(audits.Events) != 5 {
		t.Fatalf("rejected grant transition changed audit: count=%d, err=%v", len(audits.Events), err)
	}
}

func checkMCPDisconnectFencesCompletion(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPGrant(t, p)
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	completion := authorizeContractGrant(m)
	disconnect := completion
	disconnect.Operation = mcpcmd.GrantDisconnect
	disconnect.Grant.Status = mcpcmd.GrantDisconnected
	disconnect.Grant.ProtectedValues = nil
	disconnect.Audit.ID = "grant-disconnect"
	if err := p.MCP().SaveMCPGrant(t.Context(), disconnect); err != nil {
		t.Fatal(err)
	}
	if err := p.MCP().SaveMCPGrant(t.Context(), completion); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("authorization resurrected disconnected grant: %v", err)
	}
	// A new explicit authorization cycle advances generation before completion.
	m = disconnect
	m.Operation = mcpcmd.GrantRegister
	m.ExpectedGeneration = m.Grant.Generation
	m.Grant.Generation++
	m.Grant.Status = mcpcmd.GrantAuthRequired
	m.Audit.ID = "grant-reconnect"
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m = authorizeContractGrant(m)
	m.Audit.ID = "grant-reauthorize"
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	refresh := m
	refresh.Operation = mcpcmd.GrantRenew
	refresh.Authority = nil
	refresh.ExpectedGeneration = m.Grant.Generation
	refresh.Grant.Generation++
	refresh.Audit.ID = "grant-refresh"
	refresh.Audit.Action = usercmd.AuditActionMCPCredentialsRenewed
	refresh.Audit.ActorUserID = ""
	refresh.Audit.ActorSessionID = ""
	disconnect = m
	disconnect.ExpectedGeneration = m.Grant.Generation
	disconnect.Grant.Generation++
	disconnect.Operation = mcpcmd.GrantDisconnect
	disconnect.Grant.Status = mcpcmd.GrantDisconnected
	disconnect.Grant.ProtectedValues = nil
	disconnect.Audit.ID = "grant-final-disconnect"
	if err := p.MCP().SaveMCPGrant(t.Context(), disconnect); err != nil {
		t.Fatal(err)
	}
	if err := p.MCP().SaveMCPGrant(t.Context(), refresh); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("stale refresh restored tokens: %v", err)
	}
	got, found, err := p.MCP().GetMCPGrant(t.Context(), m.Grant.Binding)
	if err != nil || !found || got.Status != mcpcmd.GrantDisconnected || len(got.ProtectedValues) != 0 || got.Generation != disconnect.Grant.Generation {
		t.Fatal("disconnect fence lost")
	}
}

func checkMCPConcurrentGrantRenewalCommitsOnce(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	m := contractMCPGrant(t, p)
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m = authorizeContractGrant(m)
	if err := p.MCP().SaveMCPGrant(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m.Operation = mcpcmd.GrantRenew
	m.Authority = nil
	m.ExpectedGeneration = m.Grant.Generation
	m.Grant.Generation++
	m.Audit.Action = usercmd.AuditActionMCPCredentialsRenewed
	m.Audit.ActorUserID = ""
	m.Audit.ActorSessionID = ""
	var won atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{"renew-one", "renew-two"} {
		wg.Go(func() {
			candidate := m
			candidate.Audit.ID = id
			err := p.MCP().SaveMCPGrant(t.Context(), candidate)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, mcpcmd.ErrConflict) {
				t.Errorf("renewal: %v", err)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("concurrent renewals committed %d times", won.Load())
	}
}
