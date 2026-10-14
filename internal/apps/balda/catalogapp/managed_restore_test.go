package catalogapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const managedRestoreProviderID = "managed-restore"

// TestManagedMCPRevisionRestoreIsLazy proves that a saved session remains
// restorable when its immutable managed MCP endpoint later becomes unavailable.
// Managed MCP connectivity is checked on a turn, not during session restore.
func TestManagedMCPRevisionRestoreIsLazy(t *testing.T) {
	provider, fixtureCatalog, _, mutation, credentials := hybridCatalogFixture(t)
	if err := fixtureCatalog.MCP().Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	config := runtimeconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{
		managedRestoreProviderID: {
			Type:   agentconfig.AgentTypeOpenAI,
			OpenAI: &agentconfig.LocalAPIConfig{Model: managedRestoreProviderID, APIKey: "fixture"},
		},
	}}
	configuredUpgradeModel(t)
	service := &managedRestoreSessionService{Service: provider.RuntimeSessions()}
	store := &managedRestoreSessionStore{SessionStore: provider.Sessions()}
	workingDir := t.TempDir()
	bundled := newManagedRestoreBundledServer(t)

	oldEndpoint := newManagedRestoreEndpoint(t, "old-secret")
	initialRegistry := managedRestoreRegistry(bundled.URL)
	initialCatalog, initialBridge := newManagedRestoreCatalog(
		t, fixtureCatalog.stateDir, provider, initialRegistry, credentials, config,
	)
	oldRevision, err := credentials.PrepareRevision(nil, mcpcmd.Revision{
		ConnectionID: mutation.Connection.ID,
		ID:           mutation.Revision.ID,
		CreatedAt:    mutation.Revision.CreatedAt,
		Definition: mcpcmd.Definition{
			Transport: mcpcmd.TransportHTTP,
			URL:       oldEndpoint.URL,
			Targets:   mcpcmd.Targets{All: true},
		},
	}, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
		"X-Managed-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "old-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mutation.Revision = &oldRevision
	if err := initialCatalog.PublishMCP(t.Context(), func() error {
		return provider.MCP().SaveMCPConnection(t.Context(), mutation)
	}); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := initialCatalog.CurrentSkillSnapshot(t.Context(), workingDir)
	if err != nil {
		t.Fatal(err)
	}

	locator, err := session.NewSessionLocator("telegram", "managed-restore-address", `{}`, "managed-restore-session")
	if err != nil {
		t.Fatal(err)
	}
	initialObserver := &managedRestoreBoundaryObserver{}
	initialManager, initialRuntimes, _ := newManagedRestoreManager(
		t, initialCatalog, initialRegistry, config, workingDir, service, store, initialObserver, "",
	)
	if err := initialManager.CreateSession(
		t.Context(), session.SessionContext{Locator: locator, UserID: "actor"}, managedRestoreProviderID,
	); err != nil {
		t.Fatal(err)
	}
	initialSession, err := initialManager.GetSession(locator)
	if err != nil {
		t.Fatal(err)
	}
	turnCtx, cancelTurn := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelTurn()
	for _, err := range initialSession.GetRunner().Run(
		turnCtx,
		initialSession.GetUserID(),
		initialSession.GetAgentSessionID(),
		genai.NewContentFromText("invoke managed tool", genai.RoleUser),
		adkagent.RunConfig{},
	) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if oldEndpoint.authorized.Load() == 0 || oldEndpoint.rejected.Load() != 0 {
		t.Fatalf("initial old endpoint requests: authorized=%d rejected=%d", oldEndpoint.authorized.Load(), oldEndpoint.rejected.Load())
	}
	record, found, err := store.GetByAddress(t.Context(), locator.ChannelType, locator.AddressKey)
	if err != nil || !found {
		t.Fatalf("read initial session metadata: found=%v err=%v", found, err)
	}
	if record.RuntimeSnapshotID != string(oldSnapshot.ID) {
		t.Fatalf("initial runtime snapshot = %q, want %q", record.RuntimeSnapshotID, oldSnapshot.ID)
	}
	if err := initialManager.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := initialRuntimes.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	oldEndpoint.Disable()

	newEndpoint := newManagedRestoreEndpoint(t, "new-secret")
	revisionTime := mutation.Authority.At.Add(time.Second)
	newRevision, err := credentials.PrepareRevision(&oldRevision, mcpcmd.Revision{
		ConnectionID: mutation.Connection.ID,
		ID:           "revision-two",
		CreatedAt:    revisionTime,
		Definition: mcpcmd.Definition{
			Transport: mcpcmd.TransportHTTP,
			URL:       newEndpoint.URL,
			Targets:   mcpcmd.Targets{All: true},
		},
	}, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
		"X-Managed-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "new-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	nextMutation := mutation
	nextMutation.Connection.CurrentRevisionID = newRevision.ID
	nextMutation.Connection.UpdatedAt = revisionTime
	nextMutation.Revision = &newRevision
	nextMutation.ExpectedVersion = 1
	nextMutation.Authority.At = revisionTime
	nextMutation.Audit.ID = "revise-managed-mcp"
	nextMutation.Audit.OccurredAt = revisionTime
	if err := initialCatalog.PublishMCP(t.Context(), func() error {
		return provider.MCP().SaveMCPConnection(t.Context(), nextMutation)
	}); err != nil {
		t.Fatal(err)
	}
	if err := initialCatalog.MCP().Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := initialBridge.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	currentRegistry := managedRestoreRegistry(bundled.URL)
	currentCatalog, _ := newManagedRestoreCatalog(
		t, fixtureCatalog.stateDir, provider, currentRegistry, credentials, config,
	)
	candidate, err := currentCatalog.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := currentCatalog.PublishCandidate(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	currentSnapshot, err := currentCatalog.CurrentSkillSnapshot(t.Context(), workingDir)
	if err != nil {
		t.Fatal(err)
	}
	if currentSnapshot.ID == oldSnapshot.ID {
		t.Fatal("managed endpoint and credentials revision did not create a new snapshot")
	}
	assertCurrentManagedEndpoint(t, currentCatalog, currentRegistry, config, workingDir, string(currentSnapshot.ID), newEndpoint)

	for attempt := 1; attempt <= 2; attempt++ {
		oldRequests := oldEndpoint.requests.Load()
		newRequests := newEndpoint.requests.Load()
		creates := service.createCalls.Load()
		deletes := service.deleteCalls.Load()
		gets := service.getCalls.Load()
		upserts := store.upsertCalls.Load()
		observer := &managedRestoreBoundaryObserver{}
		manager, runtimes, binder := newManagedRestoreManager(
			t, currentCatalog, currentRegistry, config, workingDir, service, store, observer, string(oldSnapshot.ID),
		)
		restored, err := manager.RestoreSession(
			t.Context(), session.SessionContext{Locator: locator, UserID: "actor"},
		)
		if err != nil {
			t.Fatalf("exact restore %d: %v", attempt, err)
		}
		if got := restored.GetRuntimeSnapshotID(); got != string(oldSnapshot.ID) {
			t.Fatalf("exact restore %d snapshot = %q, want %q", attempt, got, oldSnapshot.ID)
		}
		if got := binder.exactCalls.Load(); got != 1 {
			t.Fatalf("exact restore %d snapshot binds = %d, want 1", attempt, got)
		}
		if got := service.getCalls.Load(); got != gets+1 {
			t.Fatalf("exact restore %d ADK Get calls = %d, want %d", attempt, got, gets+1)
		}
		if got := service.createCalls.Load(); got != creates {
			t.Fatalf("exact restore %d ADK Create calls = %d, want %d", attempt, got, creates)
		}
		if got := service.deleteCalls.Load(); got != deletes {
			t.Fatalf("exact restore %d ADK Delete calls = %d, want %d", attempt, got, deletes)
		}
		if got := store.upsertCalls.Load(); got != upserts {
			t.Fatalf("exact restore %d metadata Upsert calls = %d, want %d", attempt, got, upserts)
		}
		if got := observer.calls.Load(); got != 0 {
			t.Fatalf("exact restore %d rotation boundaries = %d, want 0", attempt, got)
		}
		if got := oldEndpoint.requests.Load(); got != oldRequests {
			t.Fatalf("exact restore %d probed unavailable old endpoint: requests=%d, want %d", attempt, got, oldRequests)
		}
		if got := newEndpoint.requests.Load(); got != newRequests {
			t.Fatalf("exact restore %d eagerly probed current endpoint: requests=%d, want %d", attempt, got, newRequests)
		}
		if err := manager.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := runtimes.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	record, found, err = store.GetByAddress(t.Context(), locator.ChannelType, locator.AddressKey)
	if err != nil || !found || record.RuntimeSnapshotID != string(oldSnapshot.ID) {
		t.Fatalf("persisted snapshot after exact reopens = %q, found=%v err=%v", record.RuntimeSnapshotID, found, err)
	}
}

func newManagedRestoreCatalog(
	t *testing.T,
	stateDir string,
	provider state.Provider,
	registry *mcpregistry.MapRegistry,
	credentials *mcpmanage.Service,
	config runtimeconfig.RuntimeConfig,
) (*Runtime, *mcpbridge.Bridge) {
	t.Helper()
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewRuntime(
		stateDir, "", "", provider, nil, nil, registry, commandcmd.NewRegistry(), credentials, bridge,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.configureProviderMCP(config.Providers, managedRestoreProviderID, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = catalog.MCP().Shutdown(context.Background())
		_ = bridge.Close(context.Background())
	})
	return catalog, bridge
}

func newManagedRestoreManager(
	t *testing.T,
	catalog *Runtime,
	registry *mcpregistry.MapRegistry,
	config runtimeconfig.RuntimeConfig,
	workingDir string,
	service adksession.Service,
	store state.SessionStore,
	observer session.BoundaryObserver,
	exactSnapshot string,
) (*session.Manager, *baldaagent.RuntimeManager, *managedRestoreBinder) {
	t.Helper()
	skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	binder := &managedRestoreBinder{exactSnapshot: exactSnapshot, delegate: &sessionCapabilityBinder{
		catalog: catalog, skills: skills, providers: config.Providers,
	}}
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		Factory:        agentfactory.New(config.Providers, registry),
		ScopedFactory:  NewProviderFactory(config.Providers, registry),
		NormaCfg:       config,
		SessionService: service,
	})
	runtimes := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  managedRestoreProviderID,
		WorkingDir:       workingDir,
		StateDir:         catalog.stateDir,
		CapabilityBinder: binder,
		MCPRegistry:      registry,
		Logger:           zerolog.Nop(),
	})
	manager, err := session.NewManager(session.ManagerParams{
		AgentBuilder:       sessionapp.SessionAgentBuilderAdapter{Builder: builder},
		RuntimeManager:     sessionapp.SessionRuntimeManagerAdapter{Manager: runtimes},
		BaldaProviderID:    managedRestoreProviderID,
		WorkingDir:         workingDir,
		SessionsPersistent: true,
		SessionStore:       store,
		BoundaryObserver:   observer,
		Logger:             zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = manager.Stop(context.Background())
		_ = runtimes.Stop(context.Background())
	})
	return manager, runtimes, binder
}

func assertCurrentManagedEndpoint(
	t *testing.T,
	catalog *Runtime,
	registry *mcpregistry.MapRegistry,
	config runtimeconfig.RuntimeConfig,
	workingDir string,
	wantSnapshot string,
	endpoint *managedRestoreEndpoint,
) {
	t.Helper()
	skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	binder := &sessionCapabilityBinder{catalog: catalog, skills: skills, providers: config.Providers}
	bound, err := binder.BindSessionCapabilities(t.Context(), managedRestoreProviderID, baldaagent.SessionRuntimeRequest{WorkspaceDir: workingDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bound.Close() }()
	if string(bound.SnapshotID) != wantSnapshot || len(bound.MCPServerIDs) != 1 {
		t.Fatalf("current managed binding = snapshot %q servers %v", bound.SnapshotID, bound.MCPServerIDs)
	}
	projected, found := registry.Get(bound.MCPServerIDs[0])
	if !found {
		t.Fatal("current managed MCP projection is unavailable")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "managed-restore-check", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   projected.URL,
		HTTPClient: &http.Client{Transport: projectedHeaders{headers: projected.Headers}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"text": "current managed revision"},
	})
	if err != nil || result.IsError {
		t.Fatalf("current managed MCP call = %+v, %v", result, err)
	}
	if endpoint.authorized.Load() == 0 || endpoint.rejected.Load() != 0 {
		t.Fatalf("current endpoint requests: authorized=%d rejected=%d", endpoint.authorized.Load(), endpoint.rejected.Load())
	}
}

func newManagedRestoreBundledServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "managed-restore-bundled", Version: "1"}, nil)
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(endpoint.Close)
	return endpoint
}

func managedRestoreRegistry(bundledURL string) *mcpregistry.MapRegistry {
	registry := mcpregistry.New(nil)
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledURL})
	return registry
}

type managedRestoreEndpoint struct {
	*httptest.Server
	available  atomic.Bool
	requests   atomic.Int64
	authorized atomic.Int64
	rejected   atomic.Int64
}

func newManagedRestoreEndpoint(t *testing.T, secret string) *managedRestoreEndpoint {
	t.Helper()
	endpoint := &managedRestoreEndpoint{}
	endpoint.available.Store(true)
	server := mcp.NewServer(&mcp.Implementation{Name: "managed-restore", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.requests.Add(1)
		if !endpoint.available.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("X-Managed-Secret") != secret {
			endpoint.rejected.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		endpoint.authorized.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	return endpoint
}

func (e *managedRestoreEndpoint) Disable() { e.available.Store(false) }

type managedRestoreSessionService struct {
	adksession.Service
	getCalls    atomic.Int64
	createCalls atomic.Int64
	deleteCalls atomic.Int64
}

func (s *managedRestoreSessionService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	s.getCalls.Add(1)
	return s.Service.Get(ctx, req)
}

func (s *managedRestoreSessionService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	s.createCalls.Add(1)
	return s.Service.Create(ctx, req)
}

func (s *managedRestoreSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	s.deleteCalls.Add(1)
	return s.Service.Delete(ctx, req)
}

type managedRestoreSessionStore struct {
	state.SessionStore
	upsertCalls atomic.Int64
}

func (s *managedRestoreSessionStore) Upsert(ctx context.Context, record state.SessionRecord) error {
	s.upsertCalls.Add(1)
	return s.SessionStore.Upsert(ctx, record)
}

type managedRestoreBoundaryObserver struct{ calls atomic.Int64 }

func (o *managedRestoreBoundaryObserver) BeforeSessionBoundary(context.Context, session.SessionBoundary) error {
	o.calls.Add(1)
	return nil
}

type managedRestoreBinder struct {
	delegate      baldaagent.SessionCapabilityBinder
	exactSnapshot string
	exactCalls    atomic.Int64
}

func (b *managedRestoreBinder) BindSessionCapabilities(
	ctx context.Context,
	providerID string,
	request baldaagent.SessionRuntimeRequest,
) (baldaagent.SessionCapabilityBinding, error) {
	if request.RuntimeSnapshotID == b.exactSnapshot && b.exactSnapshot != "" {
		b.exactCalls.Add(1)
	}
	return b.delegate.BindSessionCapabilities(ctx, providerID, request)
}
