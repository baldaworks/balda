package balda

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
)

const pluginsBrowserPassword = "correct-horse-battery"

// The opt-in application gate uses normal password login and the production
// Backoffice handler, shared Fx plugin owner, catalog, and SQLite database.
func TestBackofficePluginsBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, base := range []string{"", "/balda/backoffice"} {
		for _, width := range []int{1440, 390} {
			t.Run(fmt.Sprintf("%s/%d", base, width), func(t *testing.T) { runPluginsBrowserWorkflow(t, base, width) })
		}
	}
}

type pluginsBrowserStage struct {
	action string
	result chan any
}

func runPluginsBrowserWorkflow(t *testing.T, base string, width int) {
	t.Helper()
	f := newPluginsRuntimeFixture(t, width == 390)
	writePluginsBrowserPackage(t, f, "1.0.0")
	createPluginsBrowserUsers(t, f.provider)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	config := backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{ListenAddr: address, PublicURL: "http://" + address, BasePath: base, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}
	runtime, err := backoffice.NewRuntime(config, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.ConfigurePluginsOperations(f.ui); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Stop(context.Background()) })
	stages := make(chan pluginsBrowserStage)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		stage := pluginsBrowserStage{action: r.URL.Query().Get("action"), result: make(chan any, 1)}
		select {
		case stages <- stage:
		case <-r.Context().Done():
			return
		}
		select {
		case result := <-stage.result:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-r.Context().Done():
		}
	}))
	defer controller.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "qa/backoffice-e2e/plugins.cjs", "http://"+address, base, fmt.Sprint(width), f.definitionSource(), f.sourceRef(), controller.URL)
	command.Dir = root
	type execution struct {
		output []byte
		err    error
	}
	finished := make(chan execution, 1)
	go func() { output, err := command.CombinedOutput(); finished <- execution{output: output, err: err} }()
	var pinned state.SessionRecord
	var beforeFault string
	var releaseCatalog func()
	var beforeCatalog runtimecatalogcmd.SnapshotID
	var verifyRevoked bool
	defer func() {
		if releaseCatalog != nil {
			releaseCatalog()
		}
	}()
	for {
		select {
		case result := <-finished:
			if result.err != nil {
				t.Fatalf("Plugins browser workflow: %v\n%s", result.err, result.output)
			}
			t.Log(string(result.output))
			final, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
			if err != nil || final.Install != nil || len(final.Revisions) != 2 || len(final.Pending) != 0 {
				t.Fatalf("final durable plugin=%+v %v", final, err)
			}
			marketplaces, err := f.plugins.ListMarketplaces(t.Context())
			if err != nil || len(marketplaces) != 0 {
				t.Fatalf("final durable marketplaces=%+v %v", marketplaces, err)
			}
			if pinned.RuntimeSnapshotID == "" {
				t.Fatal("browser never created real pinned session")
			}
			f.assertVersion(t, f.snapshot(t, pinned.RuntimeSnapshotID), "1.0.0")
			return
		case stage := <-stages:
			switch stage.action {
			case "status":
				if verifyRevoked {
					observation, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
					current, catalogErr := f.catalog.Store().Application()
					if err != nil || catalogErr != nil || observation.Token() != beforeFault || current.ID != beforeCatalog {
						t.Fatal("revoked browser request changed accepted state/catalog")
					}
					verifyRevoked = false
				}
			case "pin":
				locator := f.createSession(t, "browser-pinned")
				pinned = f.record(t, locator)
			case "version2":
				writePluginsBrowserPackage(t, f, "2.0.0")
			case "chat-disable":
				if err := f.plugins.Disable(t.Context(), pluginsRuntimeName); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(filepath.Join(f.source, ".agents", "plugins", "marketplace.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				if f.git {
					f.runGit(t, "add", ".")
					f.runGit(t, "commit", "-m", "malformed index fixture")
				}
			case "restore-source":
				writePluginsBrowserPackage(t, f, "2.0.0")
			case "audit-fail":
				observation, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
				if err != nil {
					t.Fatal(err)
				}
				beforeFault = observation.Token()
				f.management.failAudit = true
			case "audit-clear":
				f.management.failAudit = false
				observation, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
				if err != nil || observation.Token() != beforeFault {
					t.Fatalf("browser audit rejection changed durable state=%+v %v", observation, err)
				}
			case "catalog-block":
				observation, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
				if err != nil {
					t.Fatal(err)
				}
				beforeFault = observation.Token()
				current, err := f.catalog.Store().Application()
				if err != nil {
					t.Fatal(err)
				}
				beforeCatalog = current.ID
				releaseCatalog, err = f.catalog.LockCatalogMutation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				f.activation.entered = make(chan struct{}, 1)
			case "revoke-blocked":
				select {
				case <-f.activation.entered:
				case <-ctx.Done():
					t.Fatal("browser did not enter catalog gate before deadline")
				}
				at := time.Now().UTC()
				event := usercmd.AuditEvent{ID: "browser-revoke", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: "administrator", Source: "plugins-browser-fixture", OccurredAt: at}
				if err := f.provider.Users().RevokeUserSessions(ctx, "administrator", at, "synthetic-test", event); err != nil {
					t.Fatal(err)
				}
				f.activation.entered = nil
				releaseCatalog()
				releaseCatalog = nil
				verifyRevoked = true
			case "complete-fail":
				f.completion.fail = true
			case "recover":
				f.completion.fail = false
				if err := f.plugins.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatalf("unknown browser stage %q", stage.action)
			}
			observation, err := f.provider.PluginManagement().ObservePlugin(t.Context(), pluginsRuntimeName)
			if err != nil {
				t.Fatal(err)
			}
			result := map[string]any{"installed": observation.Install != nil, "revisionCount": len(observation.Revisions), "pending": len(observation.Pending)}
			application, err := f.catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			selected, found := application.Sources[runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: pluginsRuntimeName}]
			result["catalogSelected"] = found
			result["catalogRevision"] = string(selected.Revision)
			commands, skills, servers := 0, 0, 0
			for id := range application.Commands {
				if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin && id.Source.Name == pluginsRuntimeName {
					commands++
				}
			}
			for id := range application.Skills {
				if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin && id.Source.Name == pluginsRuntimeName {
					skills++
				}
			}
			for id := range application.MCPServers {
				if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin && id.Source.Name == pluginsRuntimeName {
					servers++
				}
			}
			result["catalogCommands"], result["catalogSkills"], result["catalogMCPServers"] = commands, skills, servers

			if observation.Install != nil {
				result["version"] = observation.Install.Version
				result["enabled"] = observation.Install.Enabled
				result["revision"] = observation.Install.ActiveRevisionID
				result["marketplace"] = observation.Install.OriginMarketplace
			}
			if pinned.RuntimeSnapshotID != "" {
				f.assertVersion(t, f.snapshot(t, pinned.RuntimeSnapshotID), "1.0.0")
			}
			stage.result <- result
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func createPluginsBrowserUsers(t *testing.T, p state.Provider) {
	t.Helper()
	hash, err := userpassword.Hash([]byte(pluginsBrowserPassword))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		id := string(role)
		user := usercmd.User{ID: id, Username: id, NormalizedUsername: id, DisplayName: id, Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
		audit := usercmd.AuditEvent{ID: "browser-" + id, Action: usercmd.AuditActionUserCreated, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: id, Source: "plugins-browser-fixture", OccurredAt: now}
		if err := p.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: id, PasswordHash: hash}, audit); err != nil {
			t.Fatal(err)
		}
	}
}

func writePluginsBrowserPackage(t *testing.T, f *pluginsRuntimeFixture, version string) {
	t.Helper()
	f.writePackage(t, version)
	root := filepath.Join(f.source, "plugins", pluginsRuntimeName)
	manifestPath := filepath.Join(root, "plugin.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest = []byte(strings.ReplaceAll(string(manifest), `"description":"Ship"`, `"description":"<img src=x onerror=window.pluginInjected=true>"`))
	if err = os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"install", "marketplaces"} {
		path := filepath.Join(f.source, "plugins", name, "plugin.json")
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(fmt.Sprintf(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":%q,"version":"1.0.0"}`, name)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := `{"name":"runtime-index","plugins":[{"name":"runtime-demo","path":"plugins/runtime-demo"},{"name":"install","path":"plugins/install"},{"name":"marketplaces","path":"plugins/marketplaces"}]}`
	if err = os.WriteFile(filepath.Join(f.source, ".agents", "plugins", "marketplace.json"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}
	if f.git {
		f.runGit(t, "add", ".")
		f.runGit(t, "commit", "-m", "browser form fixture "+version)
	}
}

// This test-only store fault preserves the production SQL commit and catalog
// publication, while leaving the durable activation intent incomplete.
type pluginsRuntimeCompletion struct {
	state.PluginStore
	fail bool
}

func (s *pluginsRuntimeCompletion) CompletePluginActivation(ctx context.Context, id string, at time.Time) error {
	if s.fail {
		return fmt.Errorf("synthetic completion fault")
	}
	return s.PluginStore.CompletePluginActivation(ctx, id, at)
}

// The optional signal records entry to the real catalog mutation gate. All
// locking, compilation and publication remain owned by the production runtime.
type pluginsRuntimeActivator struct {
	pluginapp.CatalogActivator
	entered chan struct{}
}

func (a *pluginsRuntimeActivator) LockCatalogMutation(ctx context.Context) (func(), error) {
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
	}
	return a.CatalogActivator.LockCatalogMutation(ctx)
}
