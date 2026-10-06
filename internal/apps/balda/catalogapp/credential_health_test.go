package catalogapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestTransientRefreshFailureReportsUnavailableAndRecovers(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) { testTransientRefreshFailureReportsUnavailableAndRecovers(t, transport, false) })
	}
}

func TestCurrentCredentialRetryPreservesRetainedScopeFailure(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) { testTransientRefreshFailureReportsUnavailableAndRecovers(t, transport, true) })
	}
}

func testTransientRefreshFailureReportsUnavailableAndRecovers(t *testing.T, transport mcpcmd.Transport, retained bool) {
	provider, original, _, mutation, credentials := hybridCatalogFixture(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "credential-health", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	if transport == mcpcmd.TransportSSE {
		handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
	}
	upstream := httptest.NewServer(handler)
	defer upstream.Close()
	worker := newWorkerGrantFixture(t, provider, credentials, mutation.Authority, mutation.Connection.ID, upstream.URL)
	revision := *mutation.Revision
	revision.Definition = mcpcmd.Definition{Transport: transport, URL: upstream.URL, OAuth: true, AuthBinding: &worker.binding, Scopes: []string{"tools:read"}, Targets: mcpcmd.Targets{All: true}}
	var err error
	revision, err = credentials.PrepareRevision(nil, revision, mcpcmd.ValueEdits{})
	if err != nil {
		t.Fatal(err)
	}
	var oldRevision mcpcmd.Revision
	if retained {
		oldRevision = revision
		oldRevision.ID = "0-retained"
		oldRevision.Definition.Scopes = []string{"tools:read", "tools:write"}
		oldMutation := mutation
		oldMutation.Connection.CurrentRevisionID = oldRevision.ID
		oldMutation.Revision = &oldRevision
		oldMutation.Audit.ID = "create-retained"
		if err := provider.MCP().SaveMCPConnection(t.Context(), oldMutation); err != nil {
			t.Fatal(err)
		}
		mutation.ExpectedVersion = 1
		mutation.Audit.ID = "create-current"
	}
	mutation.Revision = &revision
	if err = provider.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	worker.authorize(t)
	setScopes := func(scopes []string) {
		t.Helper()
		grant, found, err := provider.MCP().GetMCPGrant(t.Context(), worker.binding)
		if err != nil || !found {
			t.Fatal("grant missing")
		}
		secrets, err := credentials.OpenGrant(grant)
		if err != nil {
			t.Fatal(err)
		}
		expected := grant.Generation
		grant.Generation++
		grant.Scopes = scopes
		grant.UpdatedAt = time.Now().UTC()
		worker.save(t, grant, secrets, mcpcmd.GrantRenew, expected, nil)
	}
	if retained {
		setScopes(oldRevision.Definition.Scopes)
	}
	bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: worker.grants}, nil)
	if err = bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	registry := mcpregistry.New(nil)
	catalog, err := NewRuntime(original.stateDir, "", "", provider, nil, nil, registry, commandcmd.NewRegistry(), credentials, bridge)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.MCP().Shutdown(context.Background()) }()
	descriptor := managedMCPDescriptor(mutation.Connection, revision)
	snapshot := runtimecatalogcmd.Snapshot{MCPServers: map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor{descriptor.ID: descriptor}}
	health := catalog.MCP().Reconcile(t.Context(), snapshot)
	if len(health) != 1 || health[0].State != mcpruntime.HealthReady {
		t.Fatalf("initial attachment: %+v", health)
	}
	key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
	projected, found := registry.Get(mcpfx.RegistryID(key))
	if !found {
		t.Fatal("projection missing")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "credential-health-client", Version: "1"}, nil)
	httpClient := &http.Client{Transport: projectedHeaders{headers: projected.Headers}}
	var wire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: projected.URL, HTTPClient: httpClient}
	if transport == mcpcmd.TransportSSE {
		wire = &mcp.SSEClientTransport{Endpoint: projected.URL, HTTPClient: httpClient}
	}
	session, err := client.Connect(t.Context(), wire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	before, found, err := provider.MCP().GetMCPConnection(t.Context(), revision.ConnectionID)
	if err != nil || !found {
		t.Fatal("current definition missing")
	}
	originalJSON, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if retained {
		oldDescriptor := managedMCPDescriptor(mutation.Connection, oldRevision)
		keys, release, err := catalog.MCP().AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{oldDescriptor})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		oldProjection, found := registry.Get(mcpfx.RegistryID(keys[0]))
		if !found {
			t.Fatal("retained projection missing")
		}
		oldHTTP := &http.Client{Transport: projectedHeaders{headers: oldProjection.Headers}}
		var oldWire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: oldProjection.URL, HTTPClient: oldHTTP}
		if transport == mcpcmd.TransportSSE {
			oldWire = &mcp.SSEClientTransport{Endpoint: oldProjection.URL, HTTPClient: oldHTTP}
		}
		oldSession, err := client.Connect(t.Context(), oldWire, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = oldSession.Close() }()
		setScopes(revision.Definition.Scopes)
		result, callErr := oldSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
		if callErr == nil && (result == nil || !result.IsError) {
			t.Fatal("narrow grant permitted retained wider-scope request")
		}
	}
	worker.expire(t)
	worker.transientRefresh.Store(true)
	result, callErr := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if callErr == nil && (result == nil || !result.IsError) {
		t.Fatal("transient refresh did not block tool call")
	}
	status, count, err := catalog.MCPHealth(t.Context(), mutation.Connection)
	grant, found, grantErr := provider.MCP().GetMCPGrant(t.Context(), worker.binding)
	if err != nil || grantErr != nil || !found || grant.Status != mcpcmd.GrantAuthorized {
		t.Fatalf("transient error destroyed grant or inventory: %v/%v", err, grantErr)
	}
	t.Logf("tool call failed; inventory status=%s tools=%d; grant=%s", status, count, grant.Status)
	if status != mcpcmd.StatusUnavailable || count != 0 {
		t.Error("failed refresh is still reported Ready")
	}
	worker.transientRefresh.Store(false)
	if err := catalog.RetryMCPAuthorization(t.Context(), worker.binding); err != nil {
		t.Errorf("credential retry: %v", err)
	}
	status, count, err = catalog.MCPHealth(t.Context(), mutation.Connection)
	if err != nil || status != mcpcmd.StatusReady || count != 1 {
		t.Errorf("after credential retry = %s/%d/%v", status, count, err)
	}
	if worker.renewals.Load() != 1 {
		t.Errorf("retry renewals = %d, want 1", worker.renewals.Load())
	}
	if err := catalog.RetryMCPAuthorization(t.Context(), worker.binding); err != nil || worker.renewals.Load() != 1 {
		t.Errorf("ready retry renewed credentials or failed: %v", err)
	}
	if transport == mcpcmd.TransportSSE {
		// The SDK closes a legacy SSE client after a failed message POST.
		// Reconnect through the same retained projection, without a new catalog instance.
		session, err = client.Connect(t.Context(), wire, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = session.Close() }()
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("same session cannot recover: %v", err)
	}
	after, found, err := provider.MCP().GetMCPConnection(t.Context(), revision.ConnectionID)
	if err != nil || !found || after != before {
		t.Fatal("credential recovery changed connection selection or version")
	}
	saved, found, err := provider.MCP().GetMCPRevision(t.Context(), revision.ConnectionID, revision.ID)
	if err != nil || !found {
		t.Fatal("current revision missing after recovery")
	}
	savedJSON, err := json.Marshal(saved)
	if err != nil || string(savedJSON) != string(originalJSON) {
		t.Fatal("credential recovery rewrote the pinned definition")
	}

	if retained {
		if _, err := worker.grants.RequestCredentials(t.Context(), worker.binding, oldRevision.Definition.Scopes); !errors.Is(err, mcpcmd.ErrAuthRequired) {
			t.Fatalf("current recovery permitted retained wider scopes: %v", err)
		}
	}
}
