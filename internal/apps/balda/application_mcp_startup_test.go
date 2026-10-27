package balda

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookfx"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/agentproduct"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestMain(m *testing.M) {
	if handled, code := mcpfx.RunStdioMode(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func installPendingStartupOAuth(t *testing.T, provider state.Provider, credentials *mcpmanage.Service, userID, resource string) {
	t.Helper()
	user, found, err := provider.Users().GetUser(t.Context(), userID)
	if err != nil || !found {
		t.Fatal("bootstrap administrator unavailable")
	}
	now := time.Now().UTC()
	family := usercmd.SessionFamily{ID: "startup-browser", UserID: userID, Version: 1, CredentialVersion: user.Credential.Version, Assurance: usercmd.SessionAssuranceNormal,
		Access: usercmd.AccessCredential{Selector: "startup-access", VerifierDigest: []byte("fixture-digest"), ExpiresAt: now.Add(15 * time.Minute)}, CSRFVerifierDigest: []byte("fixture-csrf"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour),
		RefreshTokens: []usercmd.RefreshToken{{Selector: "startup-refresh", VerifierDigest: []byte("fixture-refresh"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	audit := usercmd.AuditEvent{ID: "startup-session", Action: usercmd.AuditActionLoginSucceeded, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: family.ID, Source: "startup-fixture", OccurredAt: now}
	if err := provider.Users().CreateSession(t.Context(), family, audit); err != nil {
		t.Fatal(err)
	}
	revision, err := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "pending-oauth", ID: "exact-unbound", CreatedAt: now, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: resource, OAuth: true, Targets: mcpcmd.Targets{All: true}}}, mcpcmd.ValueEdits{})
	if err != nil {
		t.Fatal(err)
	}
	audit.ID, audit.Action, audit.TargetType, audit.TargetID = "startup-mcp", usercmd.AuditActionMCPDefinitionChanged, usercmd.AuditTargetMCP, revision.ConnectionID
	audit.ActorUserID, audit.ActorSessionID = userID, family.ID
	mutation := state.MCPMutation{Connection: mcpcmd.Connection{ID: revision.ConnectionID, PublicID: revision.ConnectionID, Source: mcpcmd.SourceManaged, CurrentRevisionID: revision.ID, Enabled: true, CreatedAt: now, UpdatedAt: now}, Revision: &revision, Audit: audit,
		Authority: mcpcmd.Authority{UserID: userID, UserVersion: user.Version, CredentialVersion: user.Credential.Version, SessionID: family.ID, SessionVersion: family.Version, At: now}}
	if err := provider.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
}

func verifyUnavailableInvocationKeepsRuntime(t *testing.T, builder *baldaagent.Builder, manager *baldaagent.RuntimeManager, built *baldaagent.BuiltRuntime, workspace string) {
	t.Helper()
	created, err := builder.CreateFreshRuntimeSession(t.Context(), built, "alpha", "startup-user", "startup-session", workspace, baldaagent.RuntimeSessionContext{BaldaSessionID: "preserved-conversation"})
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "retained-history")
	event.Author = "user"
	event.Content = genai.NewContentFromText("earlier conversation", genai.RoleUser)
	if err := built.SessionSvc.AppendEvent(t.Context(), created, event); err != nil {
		t.Fatal(err)
	}
	snapshotID, sessionID := built.RuntimeSnapshotID, created.ID()
	for attempt := range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		var invocationErr error
		for _, err := range built.Runner.Run(ctx, "startup-user", sessionID, genai.NewContentFromText("use configured tools", genai.RoleUser), adkagent.RunConfig{}) {
			if err != nil {
				invocationErr = err
				break
			}
		}
		failed := invocationErr != nil || ctx.Err() != nil
		cancel()
		if !failed {
			t.Fatalf("unavailable invocation %d unexpectedly succeeded", attempt+1)
		}
	}
	after, err := manager.Runtime(t.Context())
	if err != nil || after != built || after.RuntimeSnapshotID != snapshotID {
		t.Fatalf("invocation outage replaced runtime or pins: %v", err)
	}
	restored, err := built.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: built.AppName, UserID: "startup-user", SessionID: sessionID})
	if err != nil || restored.Session.ID() != sessionID {
		t.Fatalf("invocation outage replaced session: %v", err)
	}
	retained := false
	for entry := range restored.Session.Events().All() {
		if entry.Content != nil {
			for _, part := range entry.Content.Parts {
				retained = retained || part.Text == "earlier conversation"
			}
		}
	}
	if !retained {
		t.Fatal("invocation outage erased retained history")
	}
}

// The fixture deliberately selects one missing native stdio executable. If
// discovery reaches its execution shim, SDK Close waits/reaps and returns that
// child's exit status. Product-aware factories redact that provider detail to
// ErrCloseFailed. Preserve every other shutdown error, including timeouts.
func verifyUnavailableStartupShutdown(t *testing.T, err error, hosted bool) {
	t.Helper()
	for _, unexpected := range unexpectedUnavailableStartupShutdownErrors(err, hosted) {
		t.Error(unexpected)
	}
}

func unexpectedUnavailableStartupShutdownErrors(err error, hosted bool) []error {
	if err == nil {
		return nil
	}
	switch failure := err.(type) {
	case interface{ Unwrap() []error }:
		var unexpected []error
		for _, child := range failure.Unwrap() {
			unexpected = append(unexpected, unexpectedUnavailableStartupShutdownErrors(child, hosted)...)
		}
		return unexpected
	case interface{ Unwrap() error }:
		return unexpectedUnavailableStartupShutdownErrors(failure.Unwrap(), hosted)
	case *exec.ExitError:
		if hosted && failure.ProcessState != nil && failure.Exited() && failure.ExitCode() == 1 {
			return nil
		}
	case error:
		if hosted && failure == agentproduct.ErrCloseFailed {
			return nil
		}
	}
	return []error{err}
}

func TestUnavailableStartupShutdownPreservesUnexpectedProductErrors(t *testing.T) {
	unexpected := errors.New("unexpected cleanup failure")
	err := errors.Join(agentproduct.ErrCloseFailed, unexpected)

	got := unexpectedUnavailableStartupShutdownErrors(err, true)
	if len(got) != 1 || !errors.Is(got[0], unexpected) {
		t.Fatalf("unexpected shutdown errors = %v, want only unexpected cleanup failure", got)
	}
	got = unexpectedUnavailableStartupShutdownErrors(agentproduct.ErrCloseFailed, false)
	if len(got) != 1 || got[0] != agentproduct.ErrCloseFailed {
		t.Fatalf("ACP shutdown errors = %v, want bounded product close failure", got)
	}
}

func TestUnavailableMCPStartupKeepsManagementRunningAndDefersProviderBuild(t *testing.T) {
	const hostedProvider = "hosted"
	for _, providerKind := range []string{hostedProvider, "acp"} {
		for _, validProvider := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/valid-provider=%t", providerKind, validProvider), func(t *testing.T) {
				var origin string
				var attempts atomic.Int32
				release := make(chan struct{})
				released := false
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					attempts.Add(1)
					if request.URL.Path == "/hung" {
						select {
						case <-request.Context().Done():
						case <-release:
						}
						return
					}
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, origin))
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer func() {
					if !released {
						close(release)
					}
					upstream.Close()
				}()
				origin = upstream.URL
				t.Setenv("OPENAI_BASE_URL", upstream.URL+"/v1")
				stateDir := t.TempDir()
				// Catalog archives deliberately retain read-only source copies.
				// Restore fixture permissions after all owners have stopped.
				t.Cleanup(func() {
					err := filepath.WalkDir(stateDir, func(path string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						mode := fs.FileMode(0600)
						if entry.IsDir() {
							mode = 0700
						}
						return os.Chmod(path, mode)
					})
					if err != nil {
						t.Error(err)
					}
				})
				provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(stateDir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = provider.Close() }()
				reserved, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address := reserved.Addr().String()
				if err := reserved.Close(); err != nil {
					t.Fatal(err)
				}
				management, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{ListenAddr: address, PublicURL: "http://" + address, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, provider)
				if err != nil {
					t.Fatal(err)
				}
				admin, err := management.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")})
				if err != nil {
					t.Fatal(err)
				}
				credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
				if err != nil {
					t.Fatal(err)
				}
				bridge := mcpbridge.New(nil, nil)
				registry := mcpregistry.New(nil)
				bundledServer := mcp.NewServer(&mcp.Implementation{Name: "bundled-startup-fixture", Version: "1"}, nil)
				bundled := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundledServer }, nil))
				defer bundled.Close()
				registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundled.URL})
				selected := []string{"auth-http", "auth-sse", "hung-http", "hung-sse", "unreachable-http", "unreachable-sse", "missing-stdio"}
				selectedProvider := agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "fixture"}, MCPServers: selected}
				if providerKind == "acp" {
					selectedProvider = agentconfig.Config{Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{filepath.Join(stateDir, "unavailable-acp")}}, MCPServers: selected}
				}
				config := runtimeconfig.RuntimeConfig{
					Providers: map[string]agentconfig.Config{"alpha": selectedProvider},
					MCPServers: map[string]agentconfig.MCPServerConfig{
						"auth-http":        {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.URL + "/auth", Headers: map[string]string{"X-Worker": "private startup fixture"}},
						"auth-sse":         {Type: agentconfig.MCPServerTypeSSE, URL: upstream.URL + "/auth"},
						"hung-http":        {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.URL + "/hung"},
						"hung-sse":         {Type: agentconfig.MCPServerTypeSSE, URL: upstream.URL + "/hung"},
						"unreachable-http": {Type: agentconfig.MCPServerTypeHTTP, URL: "http://127.0.0.1:1/mcp"},
						"unreachable-sse":  {Type: agentconfig.MCPServerTypeSSE, URL: "http://127.0.0.1:1/mcp"},
						"missing-stdio":    {Type: agentconfig.MCPServerTypeStdio, Cmd: []string{filepath.Join(stateDir, "unavailable-mcp")}, WorkingDir: stateDir},
					},
				}
				if !validProvider {
					if providerKind == hostedProvider {
						config.Providers["alpha"].OpenAI.APIKey = ""
					} else {
						config.Providers["alpha"].GenericACP.Cmd = nil
					}
				}
				installPendingStartupOAuth(t, provider, credentials, admin.UserID, upstream.URL+"/auth")
				var catalog *catalogapp.Runtime
				var lifecycle *catalogapp.Lifecycle
				var binder baldaagent.SessionCapabilityBinder
				container := fx.New(catalogapp.Module, fx.NopLogger,
					fx.Supply(config, registry, credentials, bridge),
					fx.Provide(func() state.Provider { return provider },
						fx.Annotate(func() string { return stateDir }, fx.ResultTags(`name:"balda_state_dir"`)),
						fx.Annotate(func() string { return "alpha" }, fx.ResultTags(`name:"balda_provider"`)),
						fx.Annotate(func() []string { return nil }, fx.ResultTags(`name:"balda_mcp_servers"`)),
						func(r *catalogapp.Runtime) (*pluginapp.Service, error) {
							return pluginapp.NewManaged(stateDir, provider.AppKV(), provider.Plugins(), r, provider.PluginManagement())
						}),
					fx.Populate(&catalog, &lifecycle, &binder))
				if err := container.Err(); err != nil {
					t.Fatal(err)
				}
				builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(config.Providers, registry), ScopedFactory: catalogapp.NewProviderFactory(config.Providers, registry), NormaCfg: config})
				manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{
					Builder: builder, BaldaProviderID: "alpha", WorkingDir: stateDir, StateDir: stateDir,
					CapabilityBinder: binder, ProviderService: providers.New(config.Providers, provider.ProviderManagement(), nil),
					MCPRegistry: registry, Logger: zerolog.Nop(),
				})
				httpRegistry, err := httpfx.NewRegistry("")
				if err != nil {
					t.Fatal(err)
				}
				params := applicationLifecycleParams{Catalog: lifecycle, Runtime: manager, Backoffice: management, StateProvider: provider, MCPManagement: credentials, MCPBridge: bridge, Logger: zerolog.Nop(),
					Config:     BaldaConfig{HTTP: HTTPConfig{ListenAddr: address, BaseURL: "http://" + address}},
					HTTPConfig: ResolvedHTTPConfig{ListenAddr: address, BaseURL: "http://" + address}, HTTPRegistry: httpRegistry,
					WebhookHTTP: webhookfx.HTTPContribution(func(context.Context, *httpfx.Registry) error { return nil }),
				}
				var stages []lifecycleStage
				for _, stage := range applicationLifecycleStages(params, &telegramLifecycle{}) {
					switch stage.name {
					case "user readiness", "managed MCP credential readiness", "MCP credential bridge", "runtime contribution catalog", "provider runtime", sharedHTTPIngressStage:
						stages = append(stages, stage)
					}
				}
				coordinator := newApplicationLifecycle(zerolog.Nop(), stages)
				if err := coordinator.Start(t.Context()); err != nil {
					t.Fatalf("fresh authorization-required startup: %v", err)
				}
				defer func() {
					verifyUnavailableStartupShutdown(t, coordinator.Stop(context.Background()), providerKind == hostedProvider)
				}()
				if attempts.Load() != 0 {
					t.Fatalf("startup initiated %d remote MCP requests", attempts.Load())
				}
				managementClient := verifyAuthenticatedMCPManagement(t, "http://"+address+"/backoffice")
				if !validProvider {
					if _, err := manager.Runtime(t.Context()); err == nil {
						t.Fatal("first provider use unexpectedly accepted invalid static configuration")
					}
					verifyMCPManagementOverview(t, managementClient, "http://"+address+"/backoffice")
					return
				}
				built, err := manager.Runtime(t.Context())
				if err != nil || built == nil {
					t.Fatalf("first provider use did not construct complete provider shell: %v", err)
				}
				snapshot, err := catalog.Store().Application()
				if err != nil || len(snapshot.MCPServers) != len(selected)+1 {
					t.Fatalf("startup omitted exact selected configuration: %d/%v", len(snapshot.MCPServers), err)
				}
				for _, descriptor := range snapshot.MCPServers {
					key := mcpfx.RegistryID(mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name})
					if _, found := registry.Get(key); !found {
						t.Fatalf("selected exact configuration %s absent", descriptor.Name)
					}
				}
				captures, err := provider.MCP().ListMCPConnections(t.Context())
				if err != nil || len(captures) != 1 {
					t.Fatal("startup captured OAuth without administrator action")
				}
				verifyUnavailableInvocationKeepsRuntime(t, builder, manager, built, stateDir)
				verifyMCPManagementOverview(t, managementClient, "http://"+address+"/backoffice")
				// Release the deliberately stalled peer before owner shutdown so
				// the retained SDK cancellation notification can finish its cleanup.
				close(release)
				released = true
			})
		}
	}
}

func verifyAuthenticatedMCPManagement(t *testing.T, origin string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The real login performs bcrypt work at the production cost. Allow the
	// server's 30-second response budget during concurrent full race tests.
	client := &http.Client{Jar: jar, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(origin + "/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login GET = %d", response.StatusCode)
	}
	u, _ := url.Parse(origin + "/login")
	var csrf string
	for _, cookie := range jar.Cookies(u) {
		if cookie.Name == security.CSRFCookieName {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		t.Fatal("management did not issue login CSRF")
	}
	form := url.Values{"username": {"superuser"}, "password": {"correct horse battery staple"}, "csrf_token": {csrf}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", u.Scheme+"://"+u.Host)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("authenticated login = %d", response.StatusCode)
	}
	verifyMCPManagementOverview(t, client, origin)
	return client
}

func verifyMCPManagementOverview(t *testing.T, client *http.Client, origin string) {
	t.Helper()
	response, err := client.Get(origin + "/overview")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated management = %d", response.StatusCode)
	}
}
