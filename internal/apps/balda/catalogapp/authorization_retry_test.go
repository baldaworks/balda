package catalogapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

type authorizationMCPFixture struct {
	server      *httptest.Server
	worker      atomic.Pointer[workerGrantFixture]
	unavailable atomic.Bool
}

func newAuthorizationMCPFixture(t *testing.T, transport mcpcmd.Transport, header string) *authorizationMCPFixture {
	t.Helper()
	f := &authorizationMCPFixture{}
	server := mcp.NewServer(&mcp.Implementation{Name: "authorization-retry", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	if transport == mcpcmd.TransportSSE {
		handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("X-Worker-Secret") != header || r.Header.Get(mcpbridge.CapabilityHeader) != "" {
			t.Error("private upstream header/capability boundary changed")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		worker := f.worker.Load()
		if worker == nil || r.Header.Get("Authorization") != "Bearer "+worker.access.Load().(string) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, f.server.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { f.server.CloseClientConnections(); f.server.Close() })
	return f
}

func TestConfiguredAuthorizationRetryAndRecaptureInvokeActualProviders(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			p, original, _, mutation, credentials := hybridCatalogFixture(t)
			grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(p.MCP()), mcpfx.NewOAuthProvider(nil))
			if err != nil {
				t.Fatal(err)
			}
			first := newAuthorizationMCPFixture(t, transport, "first-private-header")
			configured := map[string]agentconfig.MCPServerConfig{"worker-tools": {Type: agentconfig.MCPServerType(transport), URL: first.server.URL, Headers: map[string]string{"X-Worker-Secret": "first-private-header"}}}
			providers := map[string]agentconfig.Config{"alpha": {MCPServers: []string{"worker-tools"}}, "gamma": {}}
			newCatalog := func(configs map[string]agentconfig.MCPServerConfig, targets map[string]agentconfig.Config) (*Runtime, *mcpregistry.MapRegistry, *mcpmanage.Definitions) {
				bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: grants}, nil)
				if err := bridge.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = bridge.Close(context.Background()) })
				registry := mcpregistry.New(nil)
				catalog, err := NewRuntime(original.stateDir, "", "", p, nil, configs, registry, commandcmd.NewRegistry(), credentials, bridge)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = catalog.MCP().Shutdown(context.Background()) })
				if err := catalog.configureProviderMCP(targets, "alpha", nil); err != nil {
					t.Fatal(err)
				}
				probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), bridge)
				if err != nil {
					t.Fatal(err)
				}
				definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configs, targets, "alpha", nil), catalog, probe)
				if err != nil {
					t.Fatal(err)
				}
				return catalog, registry, definitions
			}
			catalog, registry, definitions := newCatalog(configured, providers)
			fresh, err := catalog.PreparePluginCandidate(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.PublishCandidate(t.Context(), fresh); err != nil {
				t.Fatal(err)
			}
			_, lease, blocked := catalog.AcquireProviderMCPServerIDs(t.Context(), fresh.ID, map[string][]string{"alpha": {"worker-tools"}})
			if lease != nil || !catalog.MCPAuthorizationPending(t.Context(), blocked) {
				t.Fatalf("fresh capture-free startup: %v", blocked)
			}
			capture, err := definitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: "config:worker-tools", Scopes: []string{"tools:read"}, Authority: mutation.Authority})
			if err != nil {
				t.Fatal(err)
			}
			assertCurrentRecovery(t, catalog, definitions, mcpcmd.RecoveryAuthorizationRequired, mcpcmd.GrantAuthRequired)
			worker := newWorkerGrantFixture(t, p, credentials, mutation.Authority, capture.ConnectionID, first.server.URL)
			worker.authorize(t)
			first.worker.Store(worker)
			first.unavailable.Store(true)
			request := mcpcmd.SelectAuthorization{ConnectionID: capture.ConnectionID, ExpectedRevisionID: capture.ID, Binding: worker.binding, Authority: mutation.Authority}
			bound, err := definitions.BindAuthorization(t.Context(), request)
			if err != nil || bound.Status == mcpcmd.StatusReady {
				t.Fatalf("saved grant with failed remote = %s, %v", bound.Status, err)
			}
			assertCurrentRecovery(t, catalog, definitions, "", mcpcmd.GrantAuthorized)
			pinned, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			request.ExpectedRevisionID = bound.Connection.CurrentRevisionID
			failed, err := definitions.BindAuthorization(t.Context(), request)
			if !errors.Is(err, mcpcmd.ErrUnavailable) || failed.Status == mcpcmd.StatusReady {
				t.Fatal("retry failure claimed readiness")
			}
			grant, found, err := p.MCP().GetMCPGrant(t.Context(), worker.binding)
			if err != nil || !found || grant.Status != mcpcmd.GrantAuthorized {
				t.Fatal("retry failure erased the saved grant")
			}
			first.unavailable.Store(false)
			ready, err := definitions.BindAuthorization(t.Context(), request)
			if err != nil || ready.Status != mcpcmd.StatusReady || ready.ToolCount != 1 {
				t.Fatalf("same-binding retry = %s/%d/%v", ready.Status, ready.ToolCount, err)
			}
			assertCurrentRecovery(t, catalog, definitions, "", mcpcmd.GrantAuthorized)
			unchanged, err := catalog.Store().Application()
			if err != nil || unchanged.ID != pinned.ID || ready.Connection.CurrentRevisionID != bound.Connection.CurrentRevisionID || ready.Connection.Version != bound.Connection.Version {
				t.Fatal("same binding retry rewrote definition or snapshot")
			}
			testHostedPoolInvocation(t, catalog, registry, pinned.ID, mcpcmd.SourceConfig, nil)
			testACPInvocation(t, catalog, registry, pinned.ID, mcpcmd.SourceConfig, nil)
			second := newAuthorizationMCPFixture(t, transport, "changed-private-header")
			changed := map[string]agentconfig.MCPServerConfig{"worker-tools": {Type: agentconfig.MCPServerType(transport), URL: second.server.URL, Headers: map[string]string{"X-Worker-Secret": "changed-private-header"}}}
			providers["gamma"] = agentconfig.Config{MCPServers: []string{"worker-tools"}}
			restarted, nextRegistry, nextDefinitions := newCatalog(changed, providers)
			pending, err := restarted.PreparePluginCandidate(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.PublishCandidate(t.Context(), pending); err != nil {
				t.Fatal(err)
			}
			_, lease, blocked = restarted.AcquireProviderMCPServerIDs(t.Context(), pending.ID, map[string][]string{"gamma": {"worker-tools"}})
			if lease != nil || !restarted.MCPAuthorizationPending(t.Context(), blocked) {
				t.Fatalf("trusted recapture startup: %v", blocked)
			}
			assertCurrentRecovery(t, restarted, nextDefinitions, mcpcmd.RecoveryCaptureRequired, "")
			recapture, err := nextDefinitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: capture.ConnectionID, Authority: mutation.Authority})
			if err != nil || recapture.ID == capture.ID {
				t.Fatalf("changed config capture: %v", err)
			}
			nextWorker := newWorkerGrantFixture(t, p, credentials, mutation.Authority, capture.ConnectionID, second.server.URL)
			nextWorker.authorize(t)
			second.worker.Store(nextWorker)
			next, err := nextDefinitions.BindAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: recapture.ConnectionID, ExpectedRevisionID: recapture.ID, Binding: nextWorker.binding, Authority: mutation.Authority})
			if err != nil || next.Status != mcpcmd.StatusReady {
				t.Fatalf("new capture/binding publication: %s/%v", next.Status, err)
			}
			current, err := restarted.Store().Application()
			if err != nil || current.ID == pinned.ID {
				t.Fatal("recapture failed to publish new current capabilities")
			}
			selection, release, err := restarted.AcquireProviderMCPServerIDs(t.Context(), current.ID, map[string][]string{"alpha": {}, "gamma": {}})
			if err != nil || len(selection["alpha"]) != 1 || len(selection["gamma"]) != 1 {
				t.Fatalf("changed file selection: %v/%v", selection, err)
			}
			release()
			oldSelection, oldRelease, err := restarted.AcquireProviderMCPServerIDs(t.Context(), pinned.ID, map[string][]string{"alpha": {}, "gamma": {}})
			if err != nil || len(oldSelection["alpha"]) != 1 || len(oldSelection["gamma"]) != 0 {
				t.Fatalf("old pin borrowed new file targeting: %v/%v", oldSelection, err)
			}
			oldRelease()
			testHostedPoolInvocation(t, restarted, nextRegistry, pinned.ID, mcpcmd.SourceConfig, nil)
			testACPInvocation(t, restarted, nextRegistry, pinned.ID, mcpcmd.SourceConfig, nil)
			testHostedPoolInvocation(t, restarted, nextRegistry, current.ID, mcpcmd.SourceConfig, nil)
			testACPInvocation(t, restarted, nextRegistry, current.ID, mcpcmd.SourceConfig, nil)
		})
	}
}

func TestBoundAuthorizationStartupRecovery(t *testing.T) {
	for _, state := range []string{"missing binding", "auth required", "disconnected"} {
		t.Run(state, func(t *testing.T) {
			p, original, _, mutation, credentials := hybridCatalogFixture(t)
			upstream := newAuthorizationMCPFixture(t, mcpcmd.TransportHTTP, "private bound fixture")
			configured := map[string]agentconfig.MCPServerConfig{"worker-tools": {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.server.URL, Headers: map[string]string{"X-Worker-Secret": "private bound fixture"}}}
			providers := map[string]agentconfig.Config{"alpha": {MCPServers: []string{"worker-tools"}}}
			grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(p.MCP()), mcpfx.NewOAuthProvider(nil))
			if err != nil {
				t.Fatal(err)
			}
			newCatalog := func() (*Runtime, *mcpmanage.Definitions) {
				bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: grants}, nil)
				if err := bridge.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = bridge.Close(context.Background()) })
				r, err := NewRuntime(original.stateDir, "", "", p, nil, configured, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = r.MCP().Shutdown(context.Background()) })
				if err := r.configureProviderMCP(providers, "alpha", nil); err != nil {
					t.Fatal(err)
				}
				probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), bridge)
				if err != nil {
					t.Fatal(err)
				}
				definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configured, providers, "alpha", nil), r, probe)
				if err != nil {
					t.Fatal(err)
				}
				return r, definitions
			}
			_, definitions := newCatalog()
			capture, err := definitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: "config:worker-tools", Scopes: []string{"tools:read"}, Authority: mutation.Authority})
			if err != nil {
				t.Fatal(err)
			}
			if state != "missing binding" {
				worker := newWorkerGrantFixture(t, p, credentials, mutation.Authority, capture.ConnectionID, upstream.server.URL)
				worker.authorize(t)
				upstream.worker.Store(worker)
				bound, err := definitions.BindAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: capture.ConnectionID, ExpectedRevisionID: capture.ID, Binding: worker.binding, Authority: mutation.Authority})
				if err != nil || bound.Status != mcpcmd.StatusReady {
					t.Fatalf("initial bound connection: %s/%v", bound.Status, err)
				}
				if state == "disconnected" {
					if err := grants.DisconnectConnection(t.Context(), capture.ConnectionID, mutation.Authority); err != nil {
						t.Fatal(err)
					}
				} else {
					worker.expire(t)
					worker.rejectRefresh.Store(true)
					if _, err := grants.RequestCredentials(t.Context(), worker.binding, []string{"tools:read"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
						t.Fatalf("rejected refresh = %v", err)
					}
				}
			}
			restarted, _ := newCatalog()
			snapshot, err := restarted.PreparePluginCandidate(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.PublishCandidate(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			_, lease, err := restarted.AcquireProviderMCPServerIDs(t.Context(), snapshot.ID, map[string][]string{"alpha": {"worker-tools"}})
			if lease != nil || !restarted.MCPAuthorizationPending(t.Context(), err) {
				t.Fatalf("known bound startup state %s = %v", state, err)
			}
		})
	}
}

func assertCurrentRecovery(t *testing.T, catalog *Runtime, definitions *mcpmanage.Definitions, want mcpcmd.RecoveryReason, authorization mcpcmd.GrantStatus) {
	t.Helper()
	items, err := definitions.Inventory(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("current inventory = %d/%v", len(items), err)
	}
	recovery, err := catalog.CurrentMCPRecovery(t.Context(), items[0])
	if err != nil || recovery != want {
		t.Fatalf("current recovery = %s/%v, want %s", recovery, err, want)
	}
	status, err := definitions.WorkerAuthorization(t.Context(), items[0])
	if err != nil || status != authorization {
		t.Fatalf("worker authorization = %s/%v, want %s", status, err, authorization)
	}
}
