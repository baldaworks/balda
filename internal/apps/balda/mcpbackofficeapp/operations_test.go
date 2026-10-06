package mcpbackofficeapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestOperationsPersistProtectedEditsAndProbeExactCandidate(t *testing.T) {
	var requests atomic.Int64
	var wantSecret atomic.Value
	wantSecret.Store("original-private-value")
	sdk := mcp.NewServer(&mcp.Implementation{Name: "backoffice-fixture", Version: "1"}, nil)
	mcp.AddTool(sdk, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Worker") != wantSecret.Load().(string) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	operations, p, catalog, credentials, authority := operationsFixture(t, nil)
	ids, err := operations.ProviderIDs(t.Context())
	if err != nil || len(ids) != 1 || ids[0] != "hosted" {
		t.Fatalf("providers = %v/%v", ids, err)
	}
	request := mcpcmd.CreateDefinition{PublicID: "worker", Enabled: true, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL, Targets: mcpcmd.Targets{All: true}}, Values: mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{"X-Worker": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "original-private-value"}}}, Authority: authority}
	created, err := operations.Create(t.Context(), request)
	if err != nil || created.Status != mcpcmd.StatusReady {
		t.Fatalf("create = %s/%v", created.Status, err)
	}
	pinned, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	original, found, err := p.MCP().GetMCPRevision(t.Context(), created.Connection.ID, created.Connection.CurrentRevisionID)
	if err != nil || !found {
		t.Fatal("original revision missing")
	}
	update := mcpcmd.UpdateDefinition{ConnectionID: created.Connection.ID, ExpectedVersion: created.Connection.Version, Definition: created.Definition, Values: mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{"X-Worker": {Operation: mcpcmd.ValueKeep}}}, Enabled: true, Authority: authority}
	candidate, err := operations.ProbeUpdate(t.Context(), update)
	if err != nil || candidate.Status != mcpcmd.StatusPending || candidate.ToolCount != 1 {
		t.Fatalf("candidate probe = %s/%d/%v", candidate.Status, candidate.ToolCount, err)
	}
	current, _ := catalog.Store().Application()
	revisions, err := p.MCP().ListMCPRevisions(t.Context())
	if err != nil || len(revisions) != 1 || current.ID != pinned.ID {
		t.Fatal("probe persisted/published a candidate")
	}
	update.ExpectedVersion++
	before := requests.Load()
	if _, err := operations.ProbeUpdate(t.Context(), update); !errors.Is(err, mcpcmd.ErrConflict) || requests.Load() != before {
		t.Fatal("stale probe reached transport")
	}
	update.ExpectedVersion = created.Connection.Version
	wantSecret.Store("replacement-private-value")
	update.Values.Headers["X-Worker"] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "replacement-private-value"}
	changed, err := operations.Update(t.Context(), update)
	if err != nil || changed.Connection.CurrentRevisionID == original.ID {
		t.Fatalf("update = %+v/%v", changed.Connection, err)
	}
	values, err := credentials.ResolveValues(original)
	if err != nil || values.Headers["X-Worker"] != "original-private-value" {
		t.Fatal("update rewrote retained protected values")
	}
	items, err := operations.Inventory(t.Context())
	if err != nil || len(items) != 1 || items[0].Status != mcpcmd.StatusReady {
		t.Fatalf("inventory = %+v/%v", items, err)
	}
	encoded, _ := json.Marshal(items)
	if bytes.Contains(encoded, []byte("private-value")) {
		t.Fatal("inventory exposed protected values")
	}
	selection := mcpcmd.ChangeSelection{ConnectionID: changed.Connection.ID, ExpectedVersion: changed.Connection.Version, Enabled: false, Authority: authority}
	disabled, err := operations.SetEnabled(t.Context(), selection)
	if err != nil || disabled.Status != mcpcmd.StatusDisabled {
		t.Fatalf("disable = %s/%v", disabled.Status, err)
	}
	selection.ExpectedVersion = disabled.Connection.Version
	deleted, err := operations.Delete(t.Context(), selection)
	if err != nil || deleted.Status != mcpcmd.StatusDeleted {
		t.Fatalf("delete = %s/%v", deleted.Status, err)
	}
	retained, found, err := p.MCP().GetMCPRevision(t.Context(), original.ConnectionID, original.ID)
	if err != nil || !found || !bytes.Equal(retained.ProtectedValues, original.ProtectedValues) {
		t.Fatal("selection change lost exact old protected revision")
	}
	if err := p.Users().RevokeSession(t.Context(), authority.SessionID, authority.SessionVersion, time.Now(), "revoked", fixtureAudit("revoke", usercmd.AuditActionSessionRevoked, usercmd.AuditTargetSession, authority.SessionID)); err != nil {
		t.Fatal(err)
	}
	before = requests.Load()
	if _, err := operations.Probe(t.Context(), request); !errors.Is(err, mcpcmd.ErrForbidden) || requests.Load() != before {
		t.Fatal("revoked authority launched candidate")
	}
	update.ExpectedVersion = deleted.Connection.Version
	if _, err := operations.ProbeUpdate(t.Context(), update); !errors.Is(err, mcpcmd.ErrForbidden) || requests.Load() != before {
		t.Fatal("revoked authority launched stored edit")
	}
}

func fixtureAudit(id string, action usercmd.AuditAction, target usercmd.AuditTargetType, targetID string) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: target, TargetID: targetID, Source: "mcp-backoffice-fixture", OccurredAt: time.Now().UTC()}
}

func operationsFixture(t *testing.T, configured map[string]agentconfig.MCPServerConfig) (*Operations, state.Provider, *catalogapp.Runtime, *mcpmanage.Service, mcpcmd.Authority) {
	t.Helper()
	p, err := state.NewSQLiteProvider(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	now := time.Now().UTC()
	user := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	if err := p.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: "fixture-hash"}, fixtureAudit("admin", usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, user.ID)); err != nil {
		t.Fatal(err)
	}
	family := usercmd.SessionFamily{ID: "browser", UserID: user.ID, Version: 1, CredentialVersion: 1, Assurance: usercmd.SessionAssuranceNormal, Access: usercmd.AccessCredential{Selector: "access", VerifierDigest: []byte("access-digest"), ExpiresAt: now.Add(15 * time.Minute)}, CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour), RefreshTokens: []usercmd.RefreshToken{{Selector: "refresh", VerifierDigest: []byte("refresh-digest"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	if err := p.Users().CreateSession(t.Context(), family, fixtureAudit("browser", usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, family.ID)); err != nil {
		t.Fatal(err)
	}
	credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	catalog, err := catalogapp.NewRuntime(t.TempDir(), "", "", p, nil, configured, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.MCP().Shutdown(context.Background()) })
	probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), bridge)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configured, map[string]agentconfig.Config{"hosted": {}}, "hosted", nil), catalog, probe)
	if err != nil {
		t.Fatal(err)
	}
	return New(definitions, catalog), p, catalog, credentials, mcpcmd.Authority{UserID: user.ID, UserVersion: 1, CredentialVersion: 1, SessionID: family.ID, SessionVersion: 1, At: now}
}

func TestOperationsInventoryRecoveryReadsNeverLaunch(t *testing.T) {
	var requests atomic.Int64
	var origin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+origin+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	origin = upstream.URL
	configured := map[string]agentconfig.MCPServerConfig{"file-worker": {Type: agentconfig.MCPServerTypeHTTP, URL: origin}}
	operations, p, catalog, _, authority := operationsFixture(t, configured)
	snapshot, err := catalog.PreparePluginCandidate(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.PublishCandidate(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	before := requests.Load()
	items, err := operations.Inventory(t.Context())
	if err != nil || len(items) != 1 || items[0].Recovery != mcpcmd.RecoveryFirstAuthorization || items[0].Authorization != "" {
		t.Fatalf("fresh recovery = %+v/%v", items, err)
	}
	if requests.Load() != before {
		t.Fatal("inventory metadata made a remote request")
	}
	c := items[0].Connection
	if _, err := operations.Delete(t.Context(), mcpcmd.ChangeSelection{ConnectionID: c.ID, ExpectedVersion: c.Version, Authority: authority}); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatal("file-owned row became deletable")
	}
	capture, err := operations.definitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: c.ID, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	before = requests.Load()
	items, err = operations.Inventory(t.Context())
	if err != nil || len(items) != 1 || items[0].Recovery != mcpcmd.RecoveryAuthorizationRequired || items[0].Authorization != mcpcmd.GrantAuthRequired || items[0].Status == mcpcmd.StatusReady {
		t.Fatalf("captured recovery = %+v/%v", items, err)
	}
	if requests.Load() != before {
		t.Fatal("captured inventory retried discovery")
	}
	if _, found, err := p.MCP().GetMCPRevision(t.Context(), capture.ConnectionID, capture.ID); err != nil || !found {
		t.Fatal("metadata lost protected capture")
	}
}
