package catalogapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

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
	runtime, err := NewRuntime(original.stateDir, "", "", p, nil, map[string]agentconfig.MCPServerConfig{mutation.Connection.PublicID: {Type: agentconfig.MCPServerTypeStdio, Cmd: []string{"configured-fixture-mcp"}}}, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials)
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
	if err != nil || status != mcpcmd.StatusPending || count != 0 {
		t.Fatal("health silently rebound to another revision")
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
	runtime, err := NewRuntime(original.stateDir, "", "", p, nil, map[string]agentconfig.MCPServerConfig{"configured": {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.URL}}, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials)
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
			reopened, err := NewRuntime(runtime.stateDir, "", "", p, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials)
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
	dir := t.TempDir()
	t.Cleanup(func() { makeWritable(dir) })
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
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
	runtime, err := NewRuntime(dir, "", "", p, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials)
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
