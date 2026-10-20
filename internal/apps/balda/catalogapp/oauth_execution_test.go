package catalogapp

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type workerGrantFixture struct {
	provider         state.Provider
	credentials      *mcpmanage.Service
	grants           *mcpmanage.Grants
	authority        mcpcmd.Authority
	binding          mcpcmd.AuthBinding
	access           atomic.Value
	renewals         atomic.Int32
	rejectRefresh    atomic.Bool
	transientRefresh atomic.Bool
}

func newWorkerGrantFixture(t *testing.T, provider state.Provider, credentials *mcpmanage.Service, authority mcpcmd.Authority, connectionID, resource string) *workerGrantFixture {
	t.Helper()
	f := &workerGrantFixture{provider: provider, credentials: credentials, authority: authority}
	f.access.Store("worker-access-1")
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			if f.transientRefresh.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
				return
			}
			if f.rejectRefresh.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("resource") != resource || r.Form.Get("client_id") != "worker-client" {
				t.Error("refresh was not bound to the worker resource/client")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			want := "initial-worker-refresh"
			if f.renewals.Load() > 0 {
				want = "rotated-worker-refresh"
			}
			if r.Form.Get("refresh_token") != want {
				t.Error("refresh did not use durably rotated credentials")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			generation := f.renewals.Add(1) + 1
			access := fmt.Sprintf("worker-access-%d", generation)
			f.access.Store(access)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "Bearer", "refresh_token": "rotated-worker-refresh", "expires_in": 3600, "scope": "tools:read", "resource": resource})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{"tools:read"}})
	}))
	t.Cleanup(issuer.Close)
	f.binding = mcpcmd.AuthBinding{ConnectionID: connectionID, Resource: resource, Issuer: issuer.URL, ClientID: "worker-client"}
	var err error
	f.grants, err = mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(provider.MCP()), mcpfx.NewOAuthProvider(issuer.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *workerGrantFixture) expire(t *testing.T) {
	t.Helper()
	g, found, err := f.provider.MCP().GetMCPGrant(t.Context(), f.binding)
	if err != nil || !found {
		t.Fatalf("read worker grant: %v", err)
	}
	secrets, err := f.credentials.OpenGrant(g)
	if err != nil {
		t.Fatal(err)
	}
	expected := g.Generation
	g.Generation++
	now := time.Now().UTC()
	// Match production renewal metadata when the wall clock moves backwards.
	if now.After(g.UpdatedAt) {
		g.UpdatedAt = now
	}
	if g.UpdatedAt.Before(g.CreatedAt) {
		g.UpdatedAt = g.CreatedAt
	}
	g.AccessExpiresAt = now.Add(-time.Minute)
	f.save(t, g, secrets, mcpcmd.GrantRenew, expected, nil)
}

func (f *workerGrantFixture) authorize(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	f.authority.At = now
	g := mcpcmd.Grant{ID: rand.Text(), Binding: f.binding, Generation: 1, Status: mcpcmd.GrantAuthRequired, Scopes: []string{"tools:read"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone, CreatedAt: now, UpdatedAt: now}
	f.save(t, g, mcpmanage.GrantSecrets{}, mcpcmd.GrantRegister, 0, &f.authority)
	g.Generation, g.Status, g.AccessExpiresAt = 2, mcpcmd.GrantAuthorized, now.Add(time.Hour)
	f.save(t, g, mcpmanage.GrantSecrets{AccessToken: f.access.Load().(string), RefreshToken: "initial-worker-refresh", TokenType: "Bearer"}, mcpcmd.GrantAuthorize, 1, &f.authority)
}

func (f *workerGrantFixture) save(t *testing.T, grant mcpcmd.Grant, secrets mcpmanage.GrantSecrets, operation mcpcmd.GrantOperation, expected uint64, authority *mcpcmd.Authority) {
	t.Helper()
	payload, err := f.credentials.ProtectGrant(grant, secrets)
	if err != nil {
		t.Fatalf("protect fixture grant: operation=%s generation=%d status=%s: %v", operation, grant.Generation, grant.Status, err)
	}
	grant.ProtectedValues = payload
	audit := usercmd.AuditEvent{ID: rand.Text(), Action: usercmd.AuditActionMCPAuthorizationChanged, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetMCP, TargetID: grant.Binding.ConnectionID, Source: "worker-fixture", OccurredAt: grant.UpdatedAt}
	if authority != nil {
		audit.ActorUserID, audit.ActorSessionID = authority.UserID, authority.SessionID
	} else {
		audit.Action = usercmd.AuditActionMCPCredentialsRenewed
	}
	connection, found, err := f.provider.MCP().GetMCPConnection(t.Context(), grant.Binding.ConnectionID)
	if err != nil || !found {
		t.Fatal("fixture grant connection missing")
	}
	if err := f.provider.MCP().SaveMCPGrant(t.Context(), state.MCPGrantMutation{Grant: grant, Operation: operation, ExpectedGeneration: expected, ExpectedRevisionID: connection.CurrentRevisionID, Authority: authority, Audit: audit}); err != nil {
		current, found, readErr := f.provider.MCP().GetMCPGrant(t.Context(), grant.Binding)
		t.Fatalf("save fixture grant: operation=%s expected=%d attempted=%d current=%d found=%v readError=%v created=%s updated=%s currentCreated=%s currentUpdated=%s: %v", operation, expected, grant.Generation, current.Generation, found, readErr, grant.CreatedAt.Format(time.RFC3339Nano), grant.UpdatedAt.Format(time.RFC3339Nano), current.CreatedAt.Format(time.RFC3339Nano), current.UpdatedAt.Format(time.RFC3339Nano), err)
	}
}

func TestWorkerGrantExpiryPreservesMetadataTime(t *testing.T) {
	provider, _, _, mutation, credentials := hybridCatalogFixture(t)
	worker := newWorkerGrantFixture(t, provider, credentials, mutation.Authority, mutation.Connection.ID, "https://worker.example/mcp")
	revision := *mutation.Revision
	revision.Definition = mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: worker.binding.Resource, OAuth: true, AuthBinding: &worker.binding, Scopes: []string{"tools:read"}, Targets: mcpcmd.Targets{All: true}}
	var err error
	revision, err = credentials.PrepareRevision(nil, revision, mcpcmd.ValueEdits{})
	if err != nil {
		t.Fatal(err)
	}
	mutation.Revision = &revision
	if err := provider.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	worker.authorize(t)
	grant, found, err := provider.MCP().GetMCPGrant(t.Context(), worker.binding)
	if err != nil || !found {
		t.Fatalf("fixture grant found = %v, error = %v", found, err)
	}
	secrets, err := credentials.OpenGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	expected := grant.Generation
	grant.Generation++
	// A retained timestamp may be ahead of the wall clock after clock correction.
	grant.UpdatedAt = time.Now().UTC().Add(time.Hour)
	worker.save(t, grant, secrets, mcpcmd.GrantRenew, expected, nil)
	worker.expire(t)
	expired, found, err := provider.MCP().GetMCPGrant(t.Context(), worker.binding)
	if err != nil || !found {
		t.Fatalf("expired grant found = %v, error = %v", found, err)
	}
	if expired.Generation != grant.Generation+1 || !expired.CreatedAt.Equal(grant.CreatedAt) || !expired.UpdatedAt.Equal(grant.UpdatedAt) || !expired.AccessExpiresAt.Before(time.Now()) {
		t.Fatalf("expiration changed metadata ordering: generation = %d, created = %s, updated = %s, expires = %s", expired.Generation, expired.CreatedAt, expired.UpdatedAt, expired.AccessExpiresAt)
	}
}
