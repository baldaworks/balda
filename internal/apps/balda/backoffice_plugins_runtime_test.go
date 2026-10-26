package balda

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/pluginbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adksession "google.golang.org/adk/v2/session"
)

const (
	pluginsRuntimeName      = "runtime-demo"
	pluginsRuntimeFixtureID = "fixture"
)

// TestBackofficePluginsRuntimeContracts uses the production Fx owners and the
// real session manager. Only the provider itself is replaced: no LLM is called.
func TestBackofficePluginsRuntimeContracts(t *testing.T) {
	for _, source := range []string{"local", "git"} {
		t.Run(source, func(t *testing.T) { exercisePluginsRuntimeContracts(t, source == "git") })
	}
	t.Run("audit rollback", exercisePluginsRuntimeAcceptanceRollback)
}

func exercisePluginsRuntimeContracts(t *testing.T, git bool) {
	t.Helper()
	f := newPluginsRuntimeFixture(t, git)
	f.writePackage(t, "1.0.0")
	added, err := f.ui.AddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: f.definitionSource(), Ref: f.sourceRef(), Authority: f.authority})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := f.ui.RefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: added.Name, ExpectedState: added.ExpectedState, Authority: f.authority})
	if err != nil || len(refreshed.Candidates) != 1 {
		t.Fatalf("refresh=%+v %v", refreshed, err)
	}
	candidate, err := f.ui.Candidate(t.Context(), pluginsRuntimeName, "market", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	f.mutate(t, plugincmd.PluginMutation{Operation: plugincmd.Install, PluginName: pluginsRuntimeName, MarketplaceName: "market", CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState})
	oldLocator := f.createSession(t, "old")
	oldRecord := f.record(t, oldLocator)
	oldSnapshot := f.snapshot(t, oldRecord.RuntimeSnapshotID)
	f.assertVersion(t, oldSnapshot, "1.0.0")
	f.assertMCPBinding(t, oldLocator, oldSnapshot)

	f.writePackage(t, "2.0.0")
	market, err := f.ui.Marketplace(t.Context(), "market", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ui.RefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: market.ExpectedState, Authority: f.authority}); err != nil {
		t.Fatal(err)
	}
	candidate, err = f.ui.Candidate(t.Context(), pluginsRuntimeName, "", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	f.mutate(t, plugincmd.PluginMutation{Operation: plugincmd.Upgrade, PluginName: pluginsRuntimeName, CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState})
	newLocator := f.createSession(t, "new")
	newSnapshot := f.snapshot(t, f.record(t, newLocator).RuntimeSnapshotID)
	f.assertVersion(t, newSnapshot, "2.0.0")
	f.assertMCPBinding(t, newLocator, newSnapshot)
	if reflect.DeepEqual(f.runtime.bindings[oldLocator.SessionID].MCPServerIDs, f.runtime.bindings[newLocator.SessionID].MCPServerIDs) {
		t.Fatal("upgrade reused old MCP descriptor selection")
	}
	f.assertPinned(t, oldLocator, oldRecord, oldSnapshot)
	f.assertMCPBinding(t, oldLocator, oldSnapshot)

	// Chat and browser share the same concrete owner and catalog. A chat write
	// invalidates a reviewed browser observation, and affects only new sessions.
	stale, err := f.ui.Detail(t.Context(), pluginsRuntimeName, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.plugins.Disable(t.Context(), pluginsRuntimeName); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ui.MutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: pluginsRuntimeName, ExpectedState: stale.ExpectedState, Confirm: true, Authority: f.authority}); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("stale browser mutation=%v", err)
	}
	disabled := f.createSession(t, "disabled")
	f.assertAbsent(t, f.snapshot(t, f.record(t, disabled).RuntimeSnapshotID))
	f.assertNoMCPBinding(t, disabled)
	f.assertPinned(t, oldLocator, oldRecord, oldSnapshot)
	f.assertMCPBinding(t, oldLocator, oldSnapshot)
	detail, err := f.ui.Detail(t.Context(), pluginsRuntimeName, f.authority)
	if err != nil || detail.Enabled || detail.Runtime.RevisionCurrent {
		t.Fatalf("disabled shared projection=%+v %v", detail, err)
	}
	// A disabled upgrade still validates and retains the newly prepared package
	// without selecting it into the current application.
	f.writePackage(t, "3.0.0")
	market, err = f.ui.Marketplace(t.Context(), "market", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ui.RefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: market.ExpectedState, Authority: f.authority}); err != nil {
		t.Fatal(err)
	}
	candidate, err = f.ui.Candidate(t.Context(), pluginsRuntimeName, "", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	f.mutate(t, plugincmd.PluginMutation{Operation: plugincmd.Upgrade, PluginName: pluginsRuntimeName, CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState})
	detail, err = f.ui.Detail(t.Context(), pluginsRuntimeName, f.authority)
	if err != nil || detail.Enabled || detail.Version != "3.0.0" || detail.Runtime.RevisionCurrent {
		t.Fatalf("disabled upgrade=%+v %v", detail, err)
	}
	f.mutate(t, plugincmd.PluginMutation{Operation: plugincmd.Enable, PluginName: pluginsRuntimeName, ExpectedState: detail.ExpectedState})
	enabled := f.createSession(t, "enabled")
	enabledSnapshot := f.snapshot(t, f.record(t, enabled).RuntimeSnapshotID)
	f.assertVersion(t, enabledSnapshot, "3.0.0")
	f.assertMCPBinding(t, enabled, enabledSnapshot)
	detail, err = f.ui.Detail(t.Context(), pluginsRuntimeName, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	f.mutate(t, plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: pluginsRuntimeName, ExpectedState: detail.ExpectedState, Confirm: true})
	detail, err = f.ui.Detail(t.Context(), pluginsRuntimeName, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	f.mutate(t, plugincmd.PluginMutation{Operation: plugincmd.Remove, PluginName: pluginsRuntimeName, ExpectedState: detail.ExpectedState, Confirm: true})
	market, err = f.ui.Marketplace(t.Context(), "market", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ui.RemoveMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: market.ExpectedState, Confirm: true, Authority: f.authority}); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(f.source); err != nil {
		t.Fatal(err)
	}
	f.assertPinned(t, oldLocator, oldRecord, oldSnapshot)
	f.assertMCPBinding(t, oldLocator, oldSnapshot)
	revisions, err := f.provider.Plugins().ListPluginRevisions(t.Context(), pluginsRuntimeName)
	if err != nil || len(revisions) != 3 {
		t.Fatalf("retained revisions=%+v %v", revisions, err)
	}

	// Reopen the actual SQLite provider and Fx catalog owners. The retained
	// snapshot can no longer attach the removed plugin, so restore starts fresh
	// with the current catalog and updates the saved snapshot.
	f.reopen(t)
	restored, err := f.sessions.RestoreSession(t.Context(), session.SessionContext{Locator: oldLocator})
	if err != nil || restored.GetRuntimeSnapshotID() == oldRecord.RuntimeSnapshotID {
		t.Fatalf("restore=%+v %v", restored, err)
	}
	recovered := f.record(t, oldLocator)
	if recovered.RuntimeSnapshotID != restored.GetRuntimeSnapshotID() {
		t.Fatalf("recovered metadata snapshot = %q, want %q", recovered.RuntimeSnapshotID, restored.GetRuntimeSnapshotID())
	}
	f.assertAbsent(t, f.snapshot(t, recovered.RuntimeSnapshotID))
	f.assertNoMCPBinding(t, oldLocator)
	f.assertVersion(t, oldSnapshot, "1.0.0")
	if err = f.sessions.ResetSession(t.Context(), oldLocator); err != nil {
		t.Fatal(err)
	}
	if err = f.sessions.CreateSession(t.Context(), session.SessionContext{Locator: oldLocator, UserID: "actor"}, pluginsRuntimeFixtureID); err != nil {
		t.Fatal(err)
	}
	reset := f.record(t, oldLocator)
	if reset.RuntimeSnapshotID == oldRecord.RuntimeSnapshotID {
		t.Fatal("normal reset retained removed capabilities")
	}
	f.assertAbsent(t, f.snapshot(t, reset.RuntimeSnapshotID))
	f.assertNoMCPBinding(t, oldLocator)
	f.assertVersion(t, f.snapshot(t, oldRecord.RuntimeSnapshotID), "1.0.0")
}

type pluginsRuntimeFixture struct {
	dir, source, endpoint string
	provider              state.Provider
	plugins               *pluginapp.Service
	catalog               *catalogapp.Runtime
	ui                    *pluginbackofficeapp.Operations
	sessions              *session.Manager
	authority             plugincmd.Authority
	git                   bool
	registry              *mcpregistry.MapRegistry
	management            *pluginsRuntimeManagement
	completion            *pluginsRuntimeCompletion
	activation            *pluginsRuntimeActivator
	runtime               *pluginsFixtureRuntime
}

func newPluginsRuntimeFixture(t *testing.T, git bool) *pluginsRuntimeFixture {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "plugin-fixture", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(endpoint.Close)
	f := &pluginsRuntimeFixture{dir: t.TempDir(), source: t.TempDir(), endpoint: endpoint.URL, git: git}
	t.Cleanup(func() {
		f.close()
		_ = filepath.WalkDir(f.dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.Type()&os.ModeSymlink == 0 {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	f.open(t)
	now := time.Now().UTC()
	user := usercmd.User{ID: "plugin-admin", DisplayName: "Admin", Username: "plugin-admin", NormalizedUsername: "plugin-admin", Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now}
	audit := usercmd.AuditEvent{ID: "plugin-admin-create", Action: usercmd.AuditActionUserCreated, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "test", OccurredAt: now}
	if err := f.provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: pluginsRuntimeFixtureID}, audit); err != nil {
		t.Fatal(err)
	}
	family := usercmd.SessionFamily{ID: "plugin-browser", UserID: user.ID, Assurance: usercmd.SessionAssuranceNormal, CredentialVersion: 1, Access: usercmd.AccessCredential{Selector: "plugin-access", VerifierDigest: []byte("access"), ExpiresAt: now.Add(time.Hour)}, CSRFVerifierDigest: []byte("csrf"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(2 * time.Hour), Version: 1, RefreshTokens: []usercmd.RefreshToken{{Selector: "plugin-refresh", VerifierDigest: []byte("refresh"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(2 * time.Hour)}}}
	audit.ID, audit.Action, audit.TargetID = "plugin-login", usercmd.AuditActionLoginSucceeded, family.ID
	if err := f.provider.Users().CreateSession(t.Context(), family, audit); err != nil {
		t.Fatal(err)
	}
	f.authority = plugincmd.Authority{UserID: user.ID, UserVersion: 1, CredentialVersion: 1, SessionID: family.ID, SessionVersion: 1, At: now}
	return f
}

func (f *pluginsRuntimeFixture) open(t *testing.T) {
	t.Helper()
	var err error
	f.provider, err = state.NewSQLiteProvider(t.Context(), filepath.Join(f.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.management = &pluginsRuntimeManagement{PluginManagementStore: f.provider.PluginManagement()}
	f.completion = &pluginsRuntimeCompletion{PluginStore: f.provider.Plugins()}
	var binder baldaagent.SessionCapabilityBinder
	var lifecycle *catalogapp.Lifecycle
	config := runtimeconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{pluginsRuntimeFixtureID: {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: pluginsRuntimeFixtureID, APIKey: pluginsRuntimeFixtureID}}}}
	f.registry = mcpregistry.New(nil)
	f.activation = &pluginsRuntimeActivator{}
	container := fx.New(pluginapp.Module, catalogapp.Module, fx.NopLogger,
		fx.Decorate(func(activator pluginapp.CatalogActivator) pluginapp.CatalogActivator {
			f.activation.CatalogActivator = activator
			return f.activation
		}),
		fx.Supply(config, f.registry),
		fx.Provide(func() state.Provider {
			return pluginsRuntimeProvider{Provider: f.provider, management: f.management, completion: f.completion}
		}, func() *mcpmanage.Service { return nil }, func() *mcpbridge.Bridge { return nil },
			fx.Annotate(func() string { return f.dir }, fx.ResultTags(`name:"balda_state_dir"`)),
			fx.Annotate(func() string { return pluginsRuntimeFixtureID }, fx.ResultTags(`name:"balda_provider"`)),
			fx.Annotate(func() []string { return nil }, fx.ResultTags(`name:"balda_mcp_servers"`)),
			func(plugins *pluginapp.Service, catalog *catalogapp.Runtime) *pluginbackofficeapp.Operations {
				return pluginbackofficeapp.New(plugins, pluginbackofficeapp.CatalogHealth{Catalog: catalog})
			}),
		fx.Populate(&f.plugins, &f.catalog, &f.ui, &binder, &lifecycle))
	if err := container.Err(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtime := &pluginsFixtureRuntime{binder: binder, service: adksession.InMemoryService(), bindings: make(map[string]baldaagent.SessionCapabilityBinding)}
	f.runtime = runtime
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{NormaCfg: config})
	f.sessions, err = session.NewManager(session.ManagerParams{AgentBuilder: sessionapp.SessionAgentBuilderAdapter{Builder: builder}, RuntimeManager: runtime, BaldaProviderID: pluginsRuntimeFixtureID, WorkingDir: f.dir, SessionsPersistent: true, SessionStore: f.provider.Sessions(), RuntimeOperations: f.provider.RuntimeOperations(), Logger: zerolog.Nop()})
	if err != nil {
		t.Fatal(err)
	}
}
func (f *pluginsRuntimeFixture) close() {
	if f.sessions != nil {
		_ = f.sessions.Stop(context.Background())
	}
	if f.catalog != nil {
		_ = f.catalog.MCP().Shutdown(context.Background())
	}
	if f.provider != nil {
		_ = f.provider.Close()
	}
}
func (f *pluginsRuntimeFixture) reopen(t *testing.T) { t.Helper(); f.close(); f.open(t) }

// This provider substitute does not choose capabilities: the production binder
// receives the exact request produced by the real session manager.
type pluginsFixtureRuntime struct {
	binder   baldaagent.SessionCapabilityBinder
	service  adksession.Service
	bindings map[string]baldaagent.SessionCapabilityBinding
}

func (r *pluginsFixtureRuntime) ProviderID() string { return pluginsRuntimeFixtureID }
func (r *pluginsFixtureRuntime) Runtime(context.Context) (*session.BuiltRuntime, error) {
	return &session.BuiltRuntime{SessionSvc: r.service, AppName: pluginsRuntimeFixtureID}, nil
}
func (r *pluginsFixtureRuntime) RuntimeForSession(ctx context.Context, request session.SessionRuntimeRequest) (*session.BuiltRuntime, error) {
	bound, err := r.binder.BindSessionCapabilities(ctx, pluginsRuntimeFixtureID, baldaagent.SessionRuntimeRequest{Locator: request.Locator, UserID: request.UserID, AgentSessionID: request.AgentSessionID, WorkspaceDir: request.WorkspaceDir, RuntimeSnapshotID: request.RuntimeSnapshotID})
	if err != nil {
		return nil, err
	}
	r.bindings[request.Locator.SessionID] = bound
	return &session.BuiltRuntime{SessionSvc: r.service, AppName: pluginsRuntimeFixtureID, RuntimeSnapshotID: string(bound.SnapshotID), Close: bound.Close}, nil
}
func (f *pluginsRuntimeFixture) writePackage(t *testing.T, version string) {
	t.Helper()
	files := map[string]string{
		"plugins/runtime-demo/mcp.json":             fmt.Sprintf(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"tools":{"type":"streamable-http","url":%q}}}`, f.endpoint),
		".agents/plugins/marketplace.json":          `{"name":"runtime-index","plugins":[{"name":"runtime-demo","path":"plugins/runtime-demo"}]}`,
		"plugins/runtime-demo/plugin.json":          fmt.Sprintf(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"runtime-demo","version":%q,"extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"ship","description":"Ship","instruction":%q}]}}}`, version, "command-"+version),
		"plugins/runtime-demo/skills/ship/SKILL.md": "---\nname: ship\ndescription: Ship safely.\n---\nbody-" + version + "\n",
	}
	for name, body := range files {
		path := filepath.Join(f.source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if f.git {
		if version == "1.0.0" {
			f.runGit(t, "init", "-b", pluginsRuntimeFixtureID)
			f.runGit(t, "config", "user.name", "Plugin Fixture")
			f.runGit(t, "config", "user.email", "plugins@example.test")
		}
		f.runGit(t, "add", ".")
		f.runGit(t, "commit", "-m", "fixture "+version)
	}
}

func (f *pluginsRuntimeFixture) mutate(t *testing.T, r plugincmd.PluginMutation) {
	t.Helper()
	r.Authority = f.authority
	outcome, err := f.ui.MutatePlugin(t.Context(), r)
	if err != nil || !outcome.Accepted || outcome.RecoveryPending {
		t.Fatalf("mutation %s=%+v %v", r.Operation, outcome, err)
	}
}
func (f *pluginsRuntimeFixture) createSession(t *testing.T, name string) session.SessionLocator {
	t.Helper()
	locator, err := session.NewSessionLocator("telegram", name, `{}`, "plugins-"+name)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.sessions.CreateSession(t.Context(), session.SessionContext{Locator: locator, UserID: "actor"}, pluginsRuntimeFixtureID); err != nil {
		t.Fatal(err)
	}
	return locator
}
func (f *pluginsRuntimeFixture) record(t *testing.T, locator session.SessionLocator) state.SessionRecord {
	t.Helper()
	r, found, err := f.provider.Sessions().GetBySessionID(t.Context(), locator.SessionID)
	if err != nil || !found || r.RuntimeSnapshotID == "" {
		t.Fatalf("durable session=%+v %v %v", r, found, err)
	}
	return r
}
func (f *pluginsRuntimeFixture) snapshot(t *testing.T, id string) runtimecatalogcmd.Snapshot {
	t.Helper()
	s, err := f.catalog.ResolveCommandSnapshot(t.Context(), runtimecatalogcmd.SnapshotID(id))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f *pluginsRuntimeFixture) assertPinned(t *testing.T, locator session.SessionLocator, r state.SessionRecord, s runtimecatalogcmd.Snapshot) {
	t.Helper()
	if got := f.record(t, locator); got.RuntimeSnapshotID != r.RuntimeSnapshotID {
		t.Fatalf("existing session changed snapshot=%+v", got)
	}
	got := f.snapshot(t, r.RuntimeSnapshotID)
	if !reflect.DeepEqual(got.Commands, s.Commands) || !reflect.DeepEqual(got.Skills, s.Skills) || !reflect.DeepEqual(got.MCPServers, s.MCPServers) {
		t.Fatal("retained snapshot capabilities changed")
	}
	f.assertVersion(t, got, "1.0.0")
}
func (f *pluginsRuntimeFixture) assertVersion(t *testing.T, s runtimecatalogcmd.Snapshot, version string) {
	t.Helper()
	var commands, skills int
	for _, c := range s.Commands {
		if c.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			commands++
			if c.Instruction != "command-"+version {
				t.Fatalf("command=%+v", c)
			}
		}
	}
	for _, skill := range s.Skills {
		if skill.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			skills++
			loaded, err := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: runtimecatalogcmd.SkillRef{Source: skill.ID.Source, Revision: skill.Revision, Name: skill.Name}, MainResource: skill.Resource})
			if err != nil || loaded.Instructions != "---\nname: ship\ndescription: Ship safely.\n---\nbody-"+version+"\n" {
				t.Fatalf("skill=%+v %v", loaded, err)
			}
		}
	}
	if commands != 1 || skills != 1 {
		t.Fatalf("plugin contributions commands=%d skills=%d", commands, skills)
	}
}
func (f *pluginsRuntimeFixture) assertAbsent(t *testing.T, s runtimecatalogcmd.Snapshot) {
	t.Helper()
	for id := range s.Commands {
		if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			t.Fatal("new session selected removed plugin command")
		}
	}
	for id := range s.Skills {
		if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			t.Fatal("new session selected removed plugin skill")
		}
	}
}

func (f *pluginsRuntimeFixture) assertMCPBinding(t *testing.T, locator session.SessionLocator, s runtimecatalogcmd.Snapshot) {
	t.Helper()
	binding := f.runtime.bindings[locator.SessionID]
	if binding.SnapshotID != s.ID || binding.Skills.Snapshot != s.ID {
		t.Fatalf("session capability binding=%+v", binding)
	}
	var count int
	for _, descriptor := range s.MCPServers {
		if descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindPlugin {
			continue
		}
		count++
		id := mcpfx.RegistryID(mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name})
		projected, found := f.registry.Get(id)
		if !found {
			t.Fatalf("retained MCP instance %s missing from registry", id)
		}
		if !slices.Contains(binding.MCPServerIDs, id) || !slices.Contains(binding.ProviderMCPServerIDs[pluginsRuntimeFixtureID], id) {
			t.Fatalf("MCP selection lost retained revision: descriptor=%+v binding=%+v", descriptor, binding)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "pinned-session-check", Version: "1"}, nil)
		connected, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: projected.URL}, nil)
		if err != nil {
			t.Fatalf("connect retained MCP: %v", err)
		}
		result, err := connected.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": string(s.ID)}})
		_ = connected.Close()
		if err != nil || result.IsError || len(result.Content) != 1 {
			t.Fatalf("retained MCP call=%+v %v", result, err)
		}
		content, ok := result.Content[0].(*mcp.TextContent)
		if !ok || content.Text != string(s.ID) {
			t.Fatalf("retained MCP output=%+v", result)
		}
	}
	if count != 1 {
		t.Fatalf("plugin MCP descriptors=%d", count)
	}
}

func (f *pluginsRuntimeFixture) definitionSource() string {
	if f.git {
		return "file://" + f.source
	}
	return f.source
}
func (f *pluginsRuntimeFixture) runGit(t *testing.T, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = f.source
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git: %v: %s", err, output)
	}
}

func (f *pluginsRuntimeFixture) sourceRef() string {
	if f.git {
		return pluginsRuntimeFixtureID
	}
	return ""
}

func (f *pluginsRuntimeFixture) assertNoMCPBinding(t *testing.T, locator session.SessionLocator) {
	t.Helper()
	bound := f.runtime.bindings[locator.SessionID]
	if len(bound.MCPServerIDs) != 0 || len(bound.ProviderMCPServerIDs[pluginsRuntimeFixtureID]) != 0 {
		t.Fatalf("new session retained removed MCP selection=%+v", bound)
	}
}

// Inject only the durable audit acceptance fault; preparation, SQL transaction,
// catalog owner and publication all remain the real production implementations.
type pluginsRuntimeManagement struct {
	state.PluginManagementStore
	failAudit bool
}

func (s *pluginsRuntimeManagement) CommitPlugin(ctx context.Context, request state.PluginCommit) error {
	if s.failAudit {
		request.AuditID = "plugin-login"
	}
	return s.PluginManagementStore.CommitPlugin(ctx, request)
}

type pluginsRuntimeProvider struct {
	state.Provider
	management state.PluginManagementStore
	completion state.PluginStore
}

func (p pluginsRuntimeProvider) PluginManagement() state.PluginManagementStore { return p.management }

func exercisePluginsRuntimeAcceptanceRollback(t *testing.T) {
	t.Helper()
	f := newPluginsRuntimeFixture(t, false)
	f.writePackage(t, "1.0.0")
	added, err := f.ui.AddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: f.source, Authority: f.authority})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ui.RefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: f.authority}); err != nil {
		t.Fatal(err)
	}
	candidate, err := f.ui.Candidate(t.Context(), pluginsRuntimeName, "market", f.authority)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
	if err != nil {
		t.Fatal(err)
	}
	application, err := f.catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	f.management.failAudit = true
	outcome, err := f.ui.MutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Install, PluginName: pluginsRuntimeName, MarketplaceName: "market", CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState, Authority: f.authority})
	if !errors.Is(err, plugincmd.ErrUnavailable) || outcome.Accepted {
		t.Fatalf("audit fault=%+v %v", outcome, err)
	}
	after, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
	if err != nil || after.Token() != before.Token() {
		t.Fatalf("audit fault changed SQL metadata/revision/intent/generation=%+v %v", after, err)
	}
	current, err := f.catalog.Store().Application()
	if err != nil || current.ID != application.ID {
		t.Fatalf("audit fault published catalog=%+v %v", current, err)
	}
	// The already validated archive is allowed to exist, but it is unselected.
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := loader.LoadPlugin(filepath.Join(f.source, "plugins", pluginsRuntimeName))
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range source.Skills {
		loaded, err := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: runtimecatalogcmd.SkillRef{Source: skill.ID.Source, Revision: skill.Revision, Name: skill.Name}, MainResource: skill.Resource})
		if err != nil || loaded.Instructions == "" {
			t.Fatalf("preacceptance archive missing=%+v %v", loaded, err)
		}
	}
}

func (p pluginsRuntimeProvider) Plugins() state.PluginStore { return p.completion }
