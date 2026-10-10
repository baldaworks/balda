package catalogapp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestConfiguredUpgradeRequiresMarkedMatchingDefinition(t *testing.T) {
	registerConfiguredUpgradeFixtureMigrations(t)
	config := agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeStdio, Cmd: []string{"/usr/bin/upgrade-tools", "--stdio"}, Args: []string{"a b"}, WorkingDir: "/workspace", Env: map[string]string{"Z": "secret-z", "A": "secret-a"}}
	// This literal is SHA-256 of the independently checked 4685c92a producer
	// projection: {"type":"stdio","cmd":["/usr/bin/upgrade-tools","--stdio"],"args":["a b"],"working_dir":"/workspace","env_keys":["A","Z"]}.
	pin := runtimecatalogcmd.MCPServerDescriptor{ID: runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: "worker-tools"}, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: "worker-tools"}, Revision: "4a70d3342f69d9a0e3f8e528bbb6d9c0d53f14ef8ac2f70ea3f8574b52e721ea", Name: "worker-tools", Transport: "stdio"}
	for _, tc := range []struct {
		name    string
		change  func(*agentconfig.MCPServerConfig, *runtimecatalogcmd.MCPServerDescriptor)
		marked  bool
		want    bool
		missing bool
		corrupt bool
	}{
		{name: "matching marked pin", marked: true, want: true},
		{name: "binding values follow historical key-only contract", marked: true, want: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.Env["A"] = "replacement"
		}},
		{name: "unmarked pin"},
		{name: "missing configured server", marked: true, missing: true},
		{name: "corrupt migration marker", marked: true, corrupt: true},
		{name: "changed executable", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.Cmd[0] = "/usr/bin/changed"
		}},
		{name: "changed command arguments", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) { c.Cmd[1] = "changed" }},
		{name: "changed arguments", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) { c.Args[0] = "changed" }},
		{name: "changed directory", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.WorkingDir = "/changed"
		}},
		{name: "changed URL", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.URL = "https://changed.example/mcp"
		}},
		{name: "changed transport", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.Type = agentconfig.MCPServerTypeSSE
		}},
		{name: "added environment key", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.Env["EXTRA"] = "value"
		}},
		{name: "removed environment key", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) { delete(c.Env, "A") }},
		{name: "added header key", marked: true, change: func(c *agentconfig.MCPServerConfig, _ *runtimecatalogcmd.MCPServerDescriptor) {
			c.Headers = map[string]string{"X-Extra": "value"}
		}},
		{name: "mismatched name", marked: true, change: func(_ *agentconfig.MCPServerConfig, d *runtimecatalogcmd.MCPServerDescriptor) { d.Name = "foreign" }},
		{name: "mismatched transport", marked: true, change: func(_ *agentconfig.MCPServerConfig, d *runtimecatalogcmd.MCPServerDescriptor) { d.Transport = "sse" }},
		{name: "capture descriptor", marked: true, change: func(_ *agentconfig.MCPServerConfig, d *runtimecatalogcmd.MCPServerDescriptor) {
			d.ConfigRef = "config:worker-tools"
		}},
		{name: "unknown targeting with targets", marked: true, change: func(_ *agentconfig.MCPServerConfig, d *runtimecatalogcmd.MCPServerDescriptor) {
			d.TargetProviderIDs = []string{"alpha"}
		}},
		{name: "known descriptor cannot use upgrade alias", marked: true, change: func(_ *agentconfig.MCPServerConfig, d *runtimecatalogcmd.MCPServerDescriptor) {
			d.TargetingKnown = true
		}},
		{name: "unknown current full revision", marked: true, change: func(c *agentconfig.MCPServerConfig, d *runtimecatalogcmd.MCPServerDescriptor) {
			d.Revision = configuredMCPRevision(*c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var current agentconfig.MCPServerConfig
			if err := json.Unmarshal(mustUpgradeJSON(t, config), &current); err != nil {
				t.Fatal(err)
			}
			descriptor := pin
			if tc.change != nil {
				tc.change(&current, &descriptor)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "state.db")
			db := prepareConfiguredUpgradeDatabase(t, path)
			if tc.marked {
				snapshot, err := runtimecatalog.NewCompiler().CompileApplication([]runtimecatalogcmd.Source{{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: pin.ID.Source, Revision: pin.Revision}, MCPServers: []runtimecatalogcmd.MCPServerDescriptor{pin}}})
				if err != nil {
					t.Fatal(err)
				}
				persistUpgradeSnapshot(t, db, snapshot)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			p, err := state.NewSQLiteProvider(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			if tc.corrupt {
				if err := p.AppKV().SetJSON(t.Context(), "runtime_catalog_configured_upgrade:worker-tools:"+string(pin.Revision), map[string]any{"version": 2, "descriptor": pin}); err != nil {
					t.Fatal(err)
				}
			}
			configs := map[string]agentconfig.MCPServerConfig{"worker-tools": current}
			if tc.missing {
				delete(configs, "worker-tools")
			}
			catalog, err := NewRuntime(dir, "", "", p, nil, configs, mcpregistry.New(nil), commandcmd.NewRegistry(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			resolver := configuredUpgradeResolver{catalog: catalog, current: configuredMCPResolver(configs)}
			launch, err := resolver.ResolveLaunch(t.Context(), descriptor)
			if !tc.want {
				if !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
					t.Fatalf("ResolveLaunch = %+v / %v, want revision unavailable", launch, err)
				}
				return
			}
			if err != nil || launch.Command != current.Cmd[0] || launch.Env["A"] != current.Env["A"] {
				t.Fatalf("ResolveLaunch = %+v / %v", launch, err)
			}
			// New descriptors still bind values exactly, even after old pins
			// have SQL markers for the same configured source.
			known := configuredMCPSources(configs)[0].MCPServers[0]
			current.Env["A"] = "later-change"
			changed := map[string]agentconfig.MCPServerConfig{"worker-tools": current}
			catalog.configuredMCP = configuredMCPSources(changed)
			strict := configuredUpgradeResolver{catalog: catalog, current: configuredMCPResolver(changed)}
			if _, err := strict.ResolveLaunch(t.Context(), known); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
				t.Fatalf("known full-value pin accepted changed values: %v", err)
			}
		})
	}
}

// This exercises the supported upgrade path: persisted pre-upgrade pins must
// survive provider-open migrations and restore with their original history.
func TestConfiguredSessionUpgradeRestoresPersistedPin(t *testing.T) {
	registerConfiguredUpgradeFixtureMigrations(t)
	for _, transport := range []agentconfig.MCPServerType{agentconfig.MCPServerTypeHTTP, agentconfig.MCPServerTypeSSE, agentconfig.MCPServerTypeStdio} {
		t.Run(string(transport), func(t *testing.T) {
			config := configuredUpgradeServer(t, transport)
			configs := map[string]agentconfig.MCPServerConfig{"worker-tools": config}
			snapshot := configuredUpgradeSnapshot(t, configs)
			dir := t.TempDir()
			path := filepath.Join(dir, "state.db")
			workspace := t.TempDir()
			db := prepareConfiguredUpgradeDatabase(t, path)
			record := state.SessionRecord{SessionID: "tg-upgrade", UserID: "fixture-user", ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`, AgentName: "auto", WorkspaceDir: workspace, RuntimeSnapshotID: string(snapshot.ID), Status: state.SessionStatusActive}
			persistUpgradeSnapshot(t, db, snapshot)
			event := adksession.NewEvent(t.Context(), "before-upgrade")
			event.Author = "user"
			event.Content = genai.NewContentFromText("remember upgrade history", genai.RoleUser)
			persistUpgradeSession(t, db, record, event)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			originalBytes := configuredUpgradeBytes(t, path, string(snapshot.ID), record.SessionID)
			var before any
			if err := json.Unmarshal([]byte(originalBytes[0]), &before); err != nil {
				t.Fatal(err)
			}
			p, err := state.NewSQLiteProvider(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			if migrated := configuredUpgradeBytes(t, path, string(snapshot.ID), record.SessionID); !reflect.DeepEqual(originalBytes, migrated) {
				t.Fatal("SQL migration changed snapshot, session metadata, runtime state or history bytes")
			}
			registry := mcpregistry.New(nil)
			bundled := mcp.NewServer(&mcp.Implementation{Name: "upgrade-bundled", Version: "1"}, nil)
			bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
			t.Cleanup(func() { bundledHTTP.CloseClientConnections(); bundledHTTP.Close() })
			registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
			bridge := mcpbridge.New(nil, nil)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bridge.Close(context.Background()) })
			catalog, err := NewRuntime(dir, "", "", p, nil, configs, registry, commandcmd.NewRegistry(), nil, bridge)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = catalog.MCP().Shutdown(context.Background()) })
			providers := map[string]agentconfig.Config{"alpha": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "alpha"}, MCPServers: []string{"worker-tools"}}, "beta": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "beta"}}}
			if err := catalog.configureProviderMCP(providers, "alpha", nil); err != nil {
				t.Fatal(err)
			}
			plugins, err := pluginapp.NewManaged(dir, p.AppKV(), p.Plugins(), catalog)
			if err != nil {
				t.Fatal(err)
			}
			if err := NewLifecycle(catalog, plugins).Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
			if err != nil {
				t.Fatal(err)
			}
			binder := &sessionCapabilityBinder{catalog: catalog, skills: skills, providers: providers}
			configuredUpgradeModel(t)
			builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(providers, registry), ScopedFactory: NewProviderFactory(providers, registry), NormaCfg: runtimeconfig.RuntimeConfig{Providers: providers}, SessionService: p.RuntimeSessions()})
			runtimes := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: "alpha", WorkingDir: workspace, StateDir: dir, CapabilityBinder: binder, MCPRegistry: registry, Logger: zerolog.Nop()})
			t.Cleanup(func() { _ = runtimes.Stop(context.Background()) })
			manager, err := session.NewManager(session.ManagerParams{AgentBuilder: sessionapp.SessionAgentBuilderAdapter{Builder: builder}, RuntimeManager: sessionapp.SessionRuntimeManagerAdapter{Manager: runtimes}, BaldaProviderID: "alpha", WorkingDir: workspace, SessionsPersistent: true, SessionStore: p.Sessions(), Logger: zerolog.Nop()})
			if err != nil {
				t.Fatal(err)
			}
			locator, err := deliverycmd.NewLocator(record.ChannelType, record.AddressKey, record.AddressJSON, record.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := manager.RestoreSession(t.Context(), session.SessionContext{Locator: locator})
			if err != nil {
				t.Fatalf("RestoreSession after SQL upgrade: %v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })
			if restored.GetSessionID() != record.SessionID || restored.GetAgentSessionID() != record.SessionID || restored.GetRuntimeSnapshotID() != string(snapshot.ID) {
				t.Fatal("restore changed persisted session or capability identity")
			}
			afterRecord, found, err := p.Sessions().GetBySessionID(t.Context(), record.SessionID)
			if err != nil || !found || afterRecord != record {
				t.Fatalf("session metadata changed: %+v / %v", afterRecord, err)
			}
			after, _, err := p.AppKV().GetJSON(t.Context(), snapshotKeyPrefix+string(snapshot.ID))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("upgrade rewrote immutable snapshot")
			}
			history, err := p.RuntimeSessions().Get(t.Context(), &adksession.GetRequest{AppName: "norma-balda", UserID: record.UserID, SessionID: record.SessionID})
			if err != nil || history.Session.Events().Len() != 1 || history.Session.Events().At(0).ID != event.ID {
				t.Fatal("restore replaced existing session history")
			}
			restoredBytes := configuredUpgradeBytes(t, path, string(snapshot.ID), record.SessionID)
			if restoredBytes[0] != originalBytes[0] || restoredBytes[2] != originalBytes[2] {
				t.Fatal("restore changed snapshot or historical event bytes")
			}
			var final strings.Builder
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			for event, err := range restored.GetRunner().Run(ctx, record.UserID, restored.GetAgentSessionID(), genai.NewContentFromText("invoke worker", genai.RoleUser), adkagent.RunConfig{}) {
				if err != nil {
					t.Fatal(err)
				}
				if event.Content != nil {
					for _, part := range event.Content.Parts {
						final.WriteString(part.Text)
					}
				}
			}
			if !strings.Contains(final.String(), "upgraded tool completed") {
				t.Fatalf("restored runner did not invoke actual tool: %q", final.String())
			}
			// The same migrated pin must retain provider-default isolation through
			// both hosted pools and the actual ACP transport.
			testHostedPoolInvocationWithTools(t, catalog, registry, snapshot.ID, []string{"worker-tools"}, nil, []string{"echo"}, nil, false)
			testACPInvocationWithServers(t, catalog, registry, snapshot.ID, []string{"worker-tools"}, nil, nil, false, false)
		})
	}
}

func configuredUpgradeModel(t *testing.T) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == "tool" {
			if !strings.Contains(string(request.Messages[len(request.Messages)-1].Content), "actual hosted tool") {
				t.Error("provider lost actual tool result")
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"upgraded tool completed"}}]}`)
			return
		}
		if !strings.Contains(string(mustUpgradeJSON(t, request)), "remember upgrade history") {
			t.Error("restored provider lost pre-upgrade conversation")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"upgrade-call","type":"function","function":{"name":"echo","arguments":"{\"text\":\"actual hosted tool\"}"}}]}}]}`)
	}))
	t.Cleanup(model.Close)
	t.Setenv("OPENAI_BASE_URL", model.URL)
}

func configuredUpgradeServer(t *testing.T, transport agentconfig.MCPServerType) agentconfig.MCPServerConfig {
	t.Helper()
	if transport == agentconfig.MCPServerTypeStdio {
		t.Setenv("BALDA_MCP_CATALOG_STDIO_HOST", "host-value")
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		directory := t.TempDir()
		return agentconfig.MCPServerConfig{Type: transport, Cmd: []string{executable}, Args: append([]string{"-test.run=^TestMCPCatalogStdioChild$", "--"}, stdioCatalogLiteralArgs()...), WorkingDir: directory, Env: map[string]string{"BALDA_MCP_CATALOG_STDIO_CHILD": "1", "BALDA_MCP_CATALOG_STDIO_OVERLAY": "child-value", "BALDA_MCP_CATALOG_STDIO_DIRECTORY": directory, "BALDA_MCP_CATALOG_STDIO_TOOL": "echo", "BALDA_MCP_CATALOG_STDIO_REFERENCE": "referenced-value"}}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "upgrade-configured", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	if transport == agentconfig.MCPServerTypeSSE {
		handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Upgrade") != "configured-secret" || r.Header.Get(mcpbridge.CapabilityHeader) != "" {
			t.Error("migrated remote launch lost configured header or exposed local capability")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { upstream.CloseClientConnections(); upstream.Close() })
	return agentconfig.MCPServerConfig{Type: transport, URL: upstream.URL, Headers: map[string]string{"X-Upgrade": "configured-secret"}}
}

// Reproduce the independently inspected 4685c92a producer representation, not
// the current configured revision helper. Its JSON field order is significant.
func configuredUpgradeSnapshot(t *testing.T, configs map[string]agentconfig.MCPServerConfig) runtimecatalogcmd.Snapshot {
	t.Helper()
	var sources []runtimecatalogcmd.Source
	for name, config := range configs {
		var envKeys, headerKeys []string
		for key := range config.Env {
			envKeys = append(envKeys, key)
		}
		for key := range config.Headers {
			headerKeys = append(headerKeys, key)
		}
		sort.Strings(envKeys)
		sort.Strings(headerKeys)
		projection := struct {
			Type       agentconfig.MCPServerType `json:"type"`
			Cmd        []string                  `json:"cmd,omitempty"`
			Args       []string                  `json:"args,omitempty"`
			WorkingDir string                    `json:"working_dir,omitempty"`
			URL        string                    `json:"url,omitempty"`
			EnvKeys    []string                  `json:"env_keys,omitempty"`
			HeaderKeys []string                  `json:"header_keys,omitempty"`
		}{config.Type, config.Cmd, config.Args, config.WorkingDir, config.URL, envKeys, headerKeys}
		digest := sha256.Sum256(mustUpgradeJSON(t, projection))
		revision := runtimecatalogcmd.RevisionID(hex.EncodeToString(digest[:]))
		id := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: name}
		transport := string(config.Type)
		if config.Type == agentconfig.MCPServerTypeHTTP {
			transport = "streamable-http"
		}
		sources = append(sources, runtimecatalogcmd.Source{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: id, Revision: revision}, MCPServers: []runtimecatalogcmd.MCPServerDescriptor{{ID: runtimecatalogcmd.ContributionID{Source: id, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: name}, Revision: revision, Name: name, Transport: transport}}})
	}
	snapshot, err := runtimecatalog.NewCompiler().CompileApplication(sources)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mustUpgradeJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func persistUpgradeSnapshot(t *testing.T, db *sql.DB, snapshot runtimecatalogcmd.Snapshot) {
	t.Helper()
	record := snapshotRecord{ID: snapshot.ID, Scope: snapshot.Scope}
	for _, source := range snapshot.Sources {
		record.Sources = append(record.Sources, source)
	}
	for _, server := range snapshot.MCPServers {
		record.MCPServers = append(record.MCPServers, server)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_app_kv
		(namespace, key, value_json, updated_at) VALUES ('balda.app', ?, ?, ?)`,
		snapshotKeyPrefix+string(snapshot.ID), string(mustUpgradeJSON(t, record)), time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

// The provider registers the actual historical Go migrations in Goose's global
// registry. Use an unrelated scratch database so the upgrade fixture itself
// starts at version 49 rather than downgrading a current database.
func registerConfiguredUpgradeFixtureMigrations(t *testing.T) {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "migration-registration.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
}

// Build the historical schema directly so the upgrade proof does not depend on
// newer migrations having a reversible downgrade.
func prepareConfiguredUpgradeDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../state/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 49); err != nil {
		t.Fatal(err)
	}
	return db
}

func persistUpgradeSession(t *testing.T, db *sql.DB, record state.SessionRecord, event *adksession.Event) {
	t.Helper()
	updatedAt := event.Timestamp.UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_session_metadata
		(session_id, user_id, chat_id, topic_id, channel_type, address_key, address_json,
		 agent_name, workspace_dir, branch_name, runtime_snapshot_id, status, updated_at)
		VALUES (?, ?, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.SessionID, record.UserID,
		record.ChannelType, record.AddressKey, record.AddressJSON, record.AgentName,
		record.WorkspaceDir, record.BranchName, record.RuntimeSnapshotID, record.Status, updatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_runtime_sessions
		(app_name, user_id, session_id, state_json, updated_at) VALUES ('norma-balda', ?, ?, '{}', ?)`,
		record.UserID, record.SessionID, updatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_runtime_events
		(app_name, user_id, session_id, event_id, ordinal, timestamp, event_json)
		VALUES ('norma-balda', ?, ?, ?, 1, ?, ?)`, record.UserID, record.SessionID,
		event.ID, updatedAt, string(mustUpgradeJSON(t, event))); err != nil {
		t.Fatal(err)
	}
}

func configuredUpgradeBytes(t *testing.T, path, snapshotID, sessionID string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	queries := []struct {
		sql string
		id  string
	}{
		{`SELECT value_json FROM balda_app_kv WHERE namespace = 'balda.app' AND key = ?`, snapshotKeyPrefix + snapshotID},
		{`SELECT json_array(session_id, user_id, chat_id, topic_id, channel_type, address_key, address_json, agent_name, workspace_dir, branch_name, runtime_snapshot_id, status, updated_at) FROM balda_session_metadata WHERE session_id = ?`, sessionID},
		{`SELECT event_json FROM balda_runtime_events WHERE session_id = ? ORDER BY ordinal LIMIT 1`, sessionID},
		{`SELECT state_json FROM balda_runtime_sessions WHERE session_id = ?`, sessionID},
	}
	bytes := make([]string, len(queries))
	for i, query := range queries {
		if err := db.QueryRowContext(t.Context(), query.sql, query.id).Scan(&bytes[i]); err != nil {
			t.Fatal(err)
		}
	}
	return bytes
}
