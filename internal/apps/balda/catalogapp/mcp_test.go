package catalogapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestRemoteDiscoveryAndExecutionUseSameProtectedBridge(t *testing.T) {
	for _, source := range []mcpcmd.Source{mcpcmd.SourceManaged, mcpcmd.SourceConfig} {
		t.Run(string(source), func(t *testing.T) { testRemoteDiscoveryAndExecutionUseSameProtectedBridge(t, source, false, false) })
	}
}

func TestOAuthDiscoveryAndProviderExecutionKeepSameBinding(t *testing.T) {
	for _, source := range []mcpcmd.Source{mcpcmd.SourceManaged, mcpcmd.SourceConfig} {
		t.Run(string(source), func(t *testing.T) { testRemoteDiscoveryAndExecutionUseSameProtectedBridge(t, source, true, false) })
	}
}

func TestHeaderlessConfiguredRemoteInvokesHostedAndACPDirectly(t *testing.T) {
	testRemoteDiscoveryAndExecutionUseSameProtectedBridge(t, mcpcmd.SourceConfig, false, true)
}

func testRemoteDiscoveryAndExecutionUseSameProtectedBridge(t *testing.T, source mcpcmd.Source, oauth, headerless bool) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			p, original, _, mutation, credentials := hybridCatalogFixture(t)
			server := mcp.NewServer(&mcp.Implementation{Name: "hybrid-remote", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
				Text string `json:"text"`
			}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
			})
			var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			if transport == mcpcmd.TransportSSE {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			}
			t.Setenv("BALDA_MCP_PROJECTED_HEADER_FIXTURE", "deployment-remote-secret")
			var attack atomic.Bool
			var foreignRequests atomic.Int64
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				foreignRequests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer foreign.Close()
			var worker *workerGrantFixture
			var requests atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if (!headerless && (r.Header.Get("X-Worker-Secret") != "protected-remote-secret" || r.Header.Get("X-Deployment-Secret") != "deployment-remote-secret")) || r.Header.Get(mcpbridge.CapabilityHeader) != "" {
					t.Error("remote request lost scoped secret or exposed local capability")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if oauth && r.Header.Get("Authorization") != "Bearer "+worker.access.Load().(string) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if attack.Load() {
					if transport == mcpcmd.TransportHTTP {
						http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", foreign.URL)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer func() { upstream.CloseClientConnections(); upstream.Close() }()
			revision := *mutation.Revision
			revision.Definition = mcpcmd.Definition{Transport: transport, URL: upstream.URL, Targets: mcpcmd.Targets{All: true}}
			if source == mcpcmd.SourceManaged {
				revision.Definition.Targets = mcpcmd.Targets{Providers: []string{"alpha"}}
			}
			if oauth && source == mcpcmd.SourceManaged {
				worker = newWorkerGrantFixture(t, p, credentials, mutation.Authority, revision.ConnectionID, upstream.URL)
				revision.Definition.OAuth, revision.Definition.AuthBinding = true, &worker.binding
				revision.Definition.Scopes = []string{"tools:read"}
			}
			var err error
			values := mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
				"X-Worker-Secret":     {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "protected-remote-secret"},
				"X-Deployment-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueEnvironment, Value: "BALDA_MCP_PROJECTED_HEADER_FIXTURE"},
			}}
			if headerless {
				values.Headers = nil
			}
			revision, err = credentials.PrepareRevision(nil, revision, values)
			if err != nil {
				t.Fatal(err)
			}
			mutation.Revision = &revision
			var configured map[string]agentconfig.MCPServerConfig
			if source == mcpcmd.SourceManaged {
				if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
					t.Fatal(err)
				}
				if oauth {
					worker.authorize(t)
				}
			} else {
				configured = map[string]agentconfig.MCPServerConfig{"worker-tools": {Type: agentconfig.MCPServerType(transport), URL: upstream.URL, Headers: map[string]string{"X-Worker-Secret": "protected-remote-secret", "X-Deployment-Secret": "deployment-remote-secret"}}}
				if headerless {
					config := configured["worker-tools"]
					config.Headers = nil
					configured["worker-tools"] = config
				}
				if oauth {
					probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), nil)
					if err != nil {
						t.Fatal(err)
					}
					definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configured, map[string]agentconfig.Config{"alpha": {MCPServers: []string{"worker-tools"}}, "beta": {}}, "alpha", nil), original, probe)
					if err != nil {
						t.Fatal(err)
					}
					revision, err = definitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: "config:worker-tools", Scopes: []string{"tools:read"}, Authority: mutation.Authority})
					if err != nil {
						t.Fatal(err)
					}
					worker = newWorkerGrantFixture(t, p, credentials, mutation.Authority, revision.ConnectionID, upstream.URL)
					worker.authorize(t)
					item, err := definitions.BindAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: revision.ConnectionID, ExpectedRevisionID: revision.ID, Binding: worker.binding, Authority: mutation.Authority})
					if err != nil {
						t.Fatal(err)
					}
					mutation.Connection = item.Connection
					revision, _, err = p.MCP().GetMCPRevision(t.Context(), item.Connection.ID, item.Connection.CurrentRevisionID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			var grantCredentials mcpbridge.Credentials
			if oauth {
				grantCredentials = mcpfx.GrantCredentials{Grants: worker.grants}
			}
			bridge := mcpbridge.New(grantCredentials, nil)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = bridge.Close(t.Context()) }()
			registry := mcpregistry.New(nil)
			catalog, err := NewRuntime(original.stateDir, "", "", p, nil, configured, registry, commandcmd.NewRegistry(), credentials, bridge)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.configureProviderMCP(map[string]agentconfig.Config{"alpha": {MCPServers: []string{"worker-tools"}}, "beta": {}}, "alpha", nil); err != nil && source == mcpcmd.SourceConfig {
				t.Fatal(err)
			}
			reconciler := catalog.MCP()
			defer func() { _ = reconciler.Shutdown(t.Context()) }()
			descriptor := managedMCPDescriptor(mutation.Connection, revision)
			if source == mcpcmd.SourceConfig {
				descriptor = catalog.configuredMCP[0].MCPServers[0]
				if oauth {
					sources, err := catalog.currentConfiguredMCPSources(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					descriptor = sources[0].MCPServers[0]
				}
			}
			key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
			health := reconciler.Reconcile(t.Context(), runtimecatalogcmd.Snapshot{MCPServers: map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor{descriptor.ID: descriptor}})
			if len(health) != 1 || health[0].State != mcpruntime.HealthReady {
				t.Fatalf("protected managed transport cannot attach: %+v", health)
			}
			projected, found := registry.Get(mcpfx.RegistryID(key))
			if !found || (!headerless && (projected.URL == upstream.URL || projected.Headers[mcpbridge.CapabilityHeader] == "")) || (headerless && (projected.URL != upstream.URL || len(projected.Headers) != 0)) || projected.Headers["X-Worker-Secret"] != "" || projected.Headers["X-Deployment-Secret"] != "" {
				t.Fatal("provider config bypassed protected bridge")
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "projected-provider", Version: "1"}, nil)
			httpClient := &http.Client{Transport: projectedHeaders{headers: projected.Headers}}
			var clientTransport mcp.Transport = &mcp.StreamableClientTransport{Endpoint: projected.URL, HTTPClient: httpClient}
			if transport == mcpcmd.TransportSSE {
				clientTransport = &mcp.SSEClientTransport{Endpoint: projected.URL, HTTPClient: httpClient}
			}
			session, err := client.Connect(t.Context(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "actual protected tool"}})
			if err != nil || result.IsError || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "actual protected tool" {
				t.Fatalf("projected transport invocation failed: result=%+v error=%v", result, err)
			}
			pinned, err := catalog.compiler.CompileApplication([]runtimecatalogcmd.Source{{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: descriptor.ID.Source, Revision: descriptor.Revision}, MCPServers: []runtimecatalogcmd.MCPServerDescriptor{descriptor}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.persistSnapshot(t.Context(), pinned); err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.store.PublishApplication(pinned); err != nil {
				t.Fatal(err)
			}
			testHostedPoolInvocation(t, catalog, registry, pinned.ID, source, worker)
			testACPInvocation(t, catalog, registry, pinned.ID, source, worker)
			if !headerless {
				attack.Store(true)
				testActualProviderOriginGuard(t, catalog, registry, pinned.ID)
				attack.Store(false)
				if foreignRequests.Load() != 0 {
					t.Fatal("actual hosted or external ACP execution reached a foreign credential origin")
				}
			}
			if oauth && worker.renewals.Load() != 2 {
				t.Fatal("actual hosted and ACP sessions did not both renew their worker credentials")
			}
			if oauth && source == mcpcmd.SourceConfig {
				testConfiguredOAuthRecovery(t, catalog, worker, pinned, configured)
			}
			if source == mcpcmd.SourceManaged {
				skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
				if err != nil {
					t.Fatal(err)
				}
				provider := agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture", Model: "fixture"}}
				binder := &sessionCapabilityBinder{catalog: catalog, skills: skills, providers: map[string]agentconfig.Config{"alpha": provider, "beta": provider}}
				excluded, err := binder.BindSessionCapabilities(t.Context(), "beta", baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: string(pinned.ID)})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = excluded.Close() }()
				if len(excluded.MCPServerIDs) != 0 {
					t.Fatal("beta received an alpha-only managed MCP")
				}
			}
			if source == mcpcmd.SourceConfig {
				selection, release, err := catalog.AcquireProviderMCPServerIDs(t.Context(), pinned.ID, map[string][]string{"alpha": {}, "beta": {"worker-tools"}})
				if err != nil {
					t.Fatal(err)
				}
				release()
				if len(selection["alpha"]) != 1 || len(selection["beta"]) != 0 {
					t.Fatal("restored pin borrowed changed configured provider targets")
				}
			}
			ids, release, err := catalog.AcquireMCPServerIDs(t.Context(), pinned.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 1 || ids[0] != mcpfx.RegistryID(key) {
				t.Fatalf("exact source pin was omitted from session acquisition: %v", ids)
			}
			reconciler.Reconcile(t.Context(), runtimecatalogcmd.Snapshot{})
			if _, found := registry.Get(mcpfx.RegistryID(key)); !found {
				t.Fatal("removing current selection dropped active session projection")
			}
			if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "retained active session"}}); err != nil {
				t.Fatal("removing current selection broke active tool session")
			}
			if oauth {
				authority := worker.authority
				authority.At = time.Now().UTC()
				if err := worker.grants.DisconnectConnection(t.Context(), worker.binding.ConnectionID, authority); err != nil {
					t.Fatal(err)
				}
				before := requests.Load()
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "disconnected call"}})
				cancel()
				if err == nil || requests.Load() != before {
					t.Fatal("disconnected retained session dispatched an upstream call")
				}
				status, _, err := catalog.MCPHealth(t.Context(), mutation.Connection)
				if err != nil || status != mcpcmd.StatusDisconnected {
					t.Fatalf("disconnected grant still reported ready: status=%s error=%v", status, err)
				}
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			release()
			if _, found := registry.Get(mcpfx.RegistryID(key)); found {
				t.Fatal("drained projection remains selectable")
			}
			if headerless {
				return
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, projected.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := httpClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("drained revision still exposes bridge capability: HTTP %d", response.StatusCode)
			}
		})
	}
}

type projectedHeaders struct{ headers map[string]string }

func (p projectedHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	for key, value := range p.headers {
		clone.Header.Set(key, value)
	}
	return http.DefaultTransport.RoundTrip(clone)
}

func TestConfiguredPinsNeverRebindChangedStaticValues(t *testing.T) {
	first := map[string]agentconfig.MCPServerConfig{"configured": {Type: agentconfig.MCPServerTypeHTTP, URL: "https://worker.example/mcp", Headers: map[string]string{"X-Worker-Secret": "original-configured-secret"}}}
	descriptor := configuredMCPSources(first)[0].MCPServers[0]
	oldResolver := configuredMCPResolver(first)
	first["configured"].Headers["X-Worker-Secret"] = "new-configured-secret"
	old, err := oldResolver.ResolveLaunch(t.Context(), descriptor)
	if err != nil || old.Headers["X-Worker-Secret"] != "original-configured-secret" {
		t.Fatal("existing configured pin lost original static value")
	}
	restartedResolver := configuredMCPResolver(first)
	if _, err := restartedResolver.ResolveLaunch(t.Context(), descriptor); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatal("restored pin silently rebound to changed configured credentials")
	}
	public, err := json.Marshal(configuredMCPSources(first))
	if err != nil || strings.Contains(string(public), "new-configured-secret") {
		t.Fatal("configured snapshot contains resolved static credential")
	}
}

func TestRecoveryFailsExplicitlyOnConfiguredManagedIDCollision(t *testing.T) {
	p, original, _, mutation, credentials := hybridCatalogFixture(t)
	if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(original.stateDir, "", "", p, nil, map[string]agentconfig.MCPServerConfig{mutation.Connection.PublicID: {Type: agentconfig.MCPServerTypeStdio, Cmd: []string{"configured-fixture-mcp"}}}, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, nil)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := pluginapp.NewManaged(runtime.stateDir, p.AppKV(), p.Plugins(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(t.Context()); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("conflicting sources were shadowed: %v", err)
	}
	if _, err := runtime.Store().Application(); err == nil {
		t.Fatal("conflicting sources published a usable application snapshot")
	}
}

func TestMCPHealthUsesObservedExactRevisionRatherThanSavedSelection(t *testing.T) {
	p, runtime, plugins, mutation, _ := hybridCatalogFixture(t)
	if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, found, err := p.MCP().GetMCPConnection(t.Context(), mutation.Connection.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	status, count, err := runtime.MCPHealth(t.Context(), c)
	if err != nil || status != mcpcmd.StatusUnavailable || count != 0 {
		t.Fatalf("failed actual MCP launch health = %s/%d/%v, want unavailable", status, count, err)
	}
	c.CurrentRevisionID = "another-revision"
	status, count, err = runtime.MCPHealth(t.Context(), c)
	if !errors.Is(err, mcpcmd.ErrUnavailable) || status != mcpcmd.StatusUnavailable || count != 0 {
		t.Fatal("missing exact revision borrowed another revision health")
	}
}

func TestConfiguredHealthReportsRealDiscoveredTools(t *testing.T) {
	p, original, _, _, credentials := hybridCatalogFixture(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "catalog-health", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer upstream.Close()
	runtime, err := NewRuntime(original.stateDir, "", "", p, nil, map[string]agentconfig.MCPServerConfig{"configured": {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.URL}}, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.MCP().Shutdown(t.Context()) }()
	plugins, err := pluginapp.NewManaged(runtime.stateDir, p.AppKV(), p.Plugins(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, count, err := runtime.MCPHealth(t.Context(), mcpcmd.Connection{Source: mcpcmd.SourceConfig, PublicID: "configured", Enabled: true})
	if err != nil || status != mcpcmd.StatusReady || count != 1 {
		t.Fatalf("discovered configured MCP health = %s/%d/%v, want ready/1", status, count, err)
	}
}

type failedSnapshotWrite struct{ state.KVStore }

func (*failedSnapshotWrite) SetJSON(context.Context, string, any) error {
	return errors.New("injected catalog persistence failure")
}

type failedMCPMarker struct{ state.MCPStore }

func (*failedMCPMarker) MarkMCPPublished(context.Context, string, uint64) error {
	return mcpcmd.ErrUnavailable
}

func TestPublicationRecoveryAfterCommitOrCompletionFailureUsesLatestState(t *testing.T) {
	for _, failure := range []string{"snapshot persistence", "completion marker"} {
		t.Run(failure, func(t *testing.T) {
			p, runtime, _, mutation, credentials := hybridCatalogFixture(t)
			if failure == "snapshot persistence" {
				runtime.kv = &failedSnapshotWrite{KVStore: p.AppKV()}
			} else {
				runtime.managedMCP = &failedMCPMarker{MCPStore: p.MCP()}
			}
			if err := runtime.PublishMCP(t.Context(), func() error { return p.MCP().SaveMCPConnection(t.Context(), mutation) }); err == nil {
				t.Fatal("injected publication failure reported success")
			}
			c, found, err := p.MCP().GetMCPConnection(t.Context(), mutation.Connection.ID)
			if err != nil || !found || c.Version != 1 || c.PublishedVersion != 0 {
				t.Fatal("failed publication lost save or falsely finished marker")
			}
			next := *mutation.Revision
			next.ID = "latest-durable-revision"
			next, err = credentials.PrepareRevision(mutation.Revision, next, mcpcmd.ValueEdits{})
			if err != nil {
				t.Fatal(err)
			}
			mutation.ExpectedVersion, mutation.Connection.CurrentRevisionID, mutation.Revision, mutation.Audit.ID = 1, next.ID, &next, "latest-edit"
			if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			p, err = state.NewSQLiteProvider(t.Context(), filepath.Join(runtime.stateDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			reopened, err := NewRuntime(runtime.stateDir, "", "", p, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.MCP().Shutdown(t.Context()) })
			plugins, err := pluginapp.NewManaged(runtime.stateDir, p.AppKV(), p.Plugins(), reopened)
			if err != nil {
				t.Fatal(err)
			}
			if err := plugins.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot, err := reopened.Store().Application()
			if err != nil || len(snapshot.MCPServers) != 1 {
				t.Fatal("restart did not reconstruct current saved definition")
			}
			for _, descriptor := range snapshot.MCPServers {
				if descriptor.Revision != runtimecatalogcmd.RevisionID(next.ID) {
					t.Fatal("restart replayed stale pre-failure catalog candidate")
				}
			}
			c, found, err = p.MCP().GetMCPConnection(t.Context(), c.ID)
			if err != nil || !found || c.Version != 2 || c.PublishedVersion != 2 {
				t.Fatal("restart did not finish exact latest publication version")
			}
		})
	}
}

func TestManagedResolverRestoresExactProtectedRevisionAfterEditAndDisable(t *testing.T) {
	p, _, _, mutation, credentials := hybridCatalogFixture(t)
	if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	old := managedMCPDescriptor(mutation.Connection, *mutation.Revision)
	next := *mutation.Revision
	next.ID = "revision-two"
	var err error
	next, err = credentials.PrepareRevision(mutation.Revision, next, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{"WORKER_SECRET": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "new-worker-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	mutation.ExpectedVersion, mutation.Connection.CurrentRevisionID, mutation.Connection.Enabled = 1, next.ID, false
	mutation.Revision, mutation.Audit.ID = &next, "edit-and-disable"
	if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	resolver := &mcpfx.ManagedResolver{Store: p.MCP(), Values: credentials}
	resolved, err := resolver.ResolveLaunch(t.Context(), old)
	if err != nil {
		t.Fatalf("old retained pin cannot restore after edit and disable: %v", err)
	}
	if resolved.Env["WORKER_SECRET"] != "catalog-worker-secret" {
		t.Fatal("old pin silently used new static credential")
	}
	resolved.Env["WORKER_SECRET"] = "caller-mutation"
	resolved, err = resolver.ResolveLaunch(t.Context(), old)
	if err != nil || resolved.Env["WORKER_SECRET"] != "catalog-worker-secret" {
		t.Fatal("caller changed retained launch state")
	}
	old.Revision = "missing-revision"
	if _, err := resolver.ResolveLaunch(t.Context(), old); err == nil {
		t.Fatal("missing nonempty pin silently rebound to current revision")
	}
}

type gatedPluginCommit struct {
	state.PluginStore
	prepared chan struct{}
	release  chan struct{}
}

func (s *gatedPluginCommit) ActivatePlugin(ctx context.Context, intent state.PluginActivationIntent, install state.PluginInstallRecord) error {
	close(s.prepared)
	<-s.release
	return s.PluginStore.ActivatePlugin(ctx, intent, install)
}

func TestConcurrentPluginAndMCPPublicationKeepsBothDurableChanges(t *testing.T) {
	t.Run("disable", func(t *testing.T) { testConcurrentPluginAndMCPPublication(t, false) })
	t.Run("enable", func(t *testing.T) { testConcurrentPluginAndMCPPublication(t, true) })
}

func testConcurrentPluginAndMCPPublication(t *testing.T, enable bool) {
	p, runtime, plugins, mutation, _ := hybridCatalogFixture(t)
	writeFile(t, filepath.Join(runtime.stateDir, "plugins", "release-tools", "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"release-tools","version":"1.0.0","extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"release","description":"Release","instruction":"Release safely."}]}}}`)
	if err := NewLifecycle(runtime, plugins).Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	initial, initialErr := runtime.Store().Application()
	if initialErr != nil || len(initial.Commands) != 1 {
		t.Fatal("fixture plugin did not contribute a valid command")
	}
	if enable {
		if err := plugins.Disable(t.Context(), "release-tools"); err != nil {
			t.Fatal(err)
		}
	}
	gated := &gatedPluginCommit{PluginStore: p.Plugins(), prepared: make(chan struct{}), release: make(chan struct{})}
	plugins, err := pluginapp.NewManaged(runtime.stateDir, p.AppKV(), gated, runtime)
	if err != nil {
		t.Fatal(err)
	}
	disabled := make(chan error, 1)
	go func() {
		if enable {
			disabled <- plugins.Enable(t.Context(), "release-tools")
		} else {
			disabled <- plugins.Disable(t.Context(), "release-tools")
		}
	}()
	<-gated.prepared // The plugin candidate is prepared, but its write is pending.
	managed := make(chan error, 1)
	go func() {
		managed <- runtime.PublishMCP(t.Context(), func() error { return p.MCP().SaveMCPConnection(t.Context(), mutation) })
	}()
	// Allow an unguarded managed publication to overtake the pending plugin
	// commit; a guarded publication waits until the shared transaction ends.
	select {
	case err := <-managed:
		if err != nil {
			t.Error(err)
		}
		managed = nil
	case <-time.After(100 * time.Millisecond):
	}
	close(gated.release)
	if err := <-disabled; err != nil {
		t.Error(err)
	}
	if managed != nil {
		if err := <-managed; err != nil {
			t.Error(err)
		}
	}
	snapshot, err := runtime.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.MCPServers) != 1 {
		t.Fatal("plugin publication lost committed managed MCP source")
	}
	hasPlugin := false
	for id := range snapshot.Commands {
		if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			hasPlugin = true
		}
	}
	if hasPlugin != enable {
		t.Fatal("managed publication lost committed plugin selection")
	}
}

func TestRecoveryPublishesSavedManagedDefinitionWithoutLeakingValues(t *testing.T) {
	p, runtime, plugins, mutation, _ := hybridCatalogFixture(t)
	if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.MCPServers) != 1 {
		t.Fatalf("recovered %d MCP definitions, want saved managed definition", len(snapshot.MCPServers))
	}
	for _, descriptor := range snapshot.MCPServers {
		if descriptor.Name != mutation.Connection.PublicID || descriptor.Revision != runtimecatalogcmd.RevisionID(mutation.Revision.ID) {
			t.Fatal("recovery replaced exact managed revision")
		}
	}
	c, found, err := p.MCP().GetMCPConnection(t.Context(), mutation.Connection.ID)
	if err != nil || !found || c.PublishedVersion != c.Version {
		t.Fatal("recovery did not finish saved version marker")
	}
	raw, found, err := p.AppKV().GetJSON(t.Context(), snapshotKeyPrefix+string(snapshot.ID))
	if err != nil || !found {
		t.Fatal("snapshot not retained durably")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "catalog-worker-secret") || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(mutation.Revision.ProtectedValues)) {
		t.Fatal("snapshot contains credential material")
	}
}

func hybridCatalogFixture(t *testing.T) (state.Provider, *Runtime, *pluginapp.Service, state.MCPMutation, *mcpmanage.Service) {
	t.Helper()
	return hybridCatalogFixtureWithDatabase(t, "")
}

func hybridCatalogFixtureWithDatabase(t *testing.T, databasePath string) (state.Provider, *Runtime, *pluginapp.Service, state.MCPMutation, *mcpmanage.Service) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { makeWritable(dir) })
	if databasePath == "" {
		databasePath = filepath.Join(dir, "state.db")
	}
	p, err := state.NewSQLiteProvider(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	now := time.Now().UTC()
	audit := func(id string, action usercmd.AuditAction, target usercmd.AuditTargetType, targetID string) usercmd.AuditEvent {
		return usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: target, TargetID: targetID, Source: "hybrid-catalog-fixture", OccurredAt: now}
	}
	u := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: u.ID, PasswordHash: "fixture-hash"}, audit("create-admin", usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, u.ID)); err != nil {
		t.Fatal(err)
	}
	f := usercmd.SessionFamily{ID: "browser", UserID: u.ID, Version: 1, CredentialVersion: 1, Assurance: usercmd.SessionAssuranceNormal,
		Access: usercmd.AccessCredential{Selector: "access", VerifierDigest: []byte("access-digest"), ExpiresAt: now.Add(15 * time.Minute)}, CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour),
		RefreshTokens: []usercmd.RefreshToken{{Selector: "refresh", VerifierDigest: []byte("refresh-digest"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	if err := p.Users().CreateSession(t.Context(), f, audit("create-browser", usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, f.ID)); err != nil {
		t.Fatal(err)
	}
	credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "worker", ID: "revision-one", CreatedAt: now, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: "catalog-fixture-mcp", Targets: mcpcmd.Targets{All: true}}}, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{"WORKER_SECRET": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "catalog-worker-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	event := audit("create-mcp", usercmd.AuditActionMCPDefinitionChanged, usercmd.AuditTargetMCP, r.ConnectionID)
	event.ActorUserID, event.ActorSessionID = u.ID, f.ID
	m := state.MCPMutation{Connection: mcpcmd.Connection{ID: r.ConnectionID, PublicID: "worker-tools", Source: mcpcmd.SourceManaged, CurrentRevisionID: r.ID, Enabled: true, CreatedAt: now, UpdatedAt: now}, Revision: &r, Audit: event,
		Authority: mcpcmd.Authority{UserID: u.ID, UserVersion: 1, CredentialVersion: 1, SessionID: f.ID, SessionVersion: 1, At: now, FreshProofAge: time.Minute}}
	runtime, err := NewRuntime(dir, "", "", p, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.MCP().Shutdown(t.Context()) })
	plugins, err := pluginapp.NewManaged(dir, p.AppKV(), p.Plugins(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	return p, runtime, plugins, m, credentials
}
