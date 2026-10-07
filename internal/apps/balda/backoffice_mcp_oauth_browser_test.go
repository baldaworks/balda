package balda

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"go.uber.org/fx"
	"golang.org/x/oauth2"
)

const (
	oauthBrowserClientID             = "browser-client"
	oauthBrowserConfidentialClientID = "confidential-client"
)

// This gate starts ordinary Backoffice authentication and the production owners.
// The only remote services are a controlled OAuth issuer, MCP server and model.
func TestBackofficeMCPOAuthBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, scenario := range []struct {
		transport, flow, base string
		width                 int
	}{
		{"http", "browser", "", 1440}, {"http", "device", "/balda", 390},
		{"sse", "browser", "/balda", 1440}, {"sse", "device", "", 390},
	} {
		t.Run(scenario.transport+"/"+scenario.flow, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() {
				_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					mode := fs.FileMode(0600)
					if e.IsDir() {
						mode = 0700
					}
					return os.Chmod(path, mode)
				})
			})
			p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			issuer := newBackofficeOAuthIssuer(t, scenario.transport)
			credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(p.MCP()), mcpfx.NewOAuthProvider(issuer.server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: grants}, nil)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bridge.Close(context.Background()) })
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			_ = listener.Close()
			origin := "http://" + address
			providers := oauthBrowserProviders(t)
			for _, prefix := range []string{"managed", "static", "partial", "public"} {
				for _, kind := range []string{"hosted", "acp"} {
					provider := providers[kind]
					provider.MCPServers = nil
					providers[prefix+"-"+kind] = provider
				}
			}
			bootstrapped := false
			var retryChecks atomic.Int32
			var currentOperations *mcpbackofficeapp.Operations
			start := func(resource string) (*backoffice.Runtime, *catalogapp.Runtime, *mcpregistry.MapRegistry, baldaagent.SessionCapabilityBinder, *mcpmanage.Authorizations) {
				registry := mcpregistry.New(nil)
				configs := map[string]agentconfig.MCPServerConfig{
					"worker-tools": {Type: agentconfig.MCPServerType(scenario.transport), URL: issuer.server.URL + resource, Headers: map[string]string{"X-Worker-Secret": "synthetic-oauth-header" + resource}},
					"public-tools": {Type: agentconfig.MCPServerType(scenario.transport), URL: issuer.server.URL + "/public", Headers: map[string]string{"X-Worker-Secret": "synthetic-oauth-header/public"}},
				}
				for _, id := range []string{"public-hosted", "public-acp"} {
					provider := providers[id]
					provider.MCPServers = []string{"public-tools"}
					providers[id] = provider
				}
				config := runtimeconfig.RuntimeConfig{Providers: providers, MCPServers: configs}
				var catalog *catalogapp.Runtime
				var binder baldaagent.SessionCapabilityBinder
				container := fx.New(catalogapp.Module, fx.NopLogger, fx.Supply(config, registry, credentials, bridge), fx.Provide(func() state.Provider { return p }, fx.Annotate(func() string { return dir }, fx.ResultTags(`name:"balda_state_dir"`)), fx.Annotate(func() string { return "hosted" }, fx.ResultTags(`name:"balda_provider"`)), fx.Annotate(func() []string { return nil }, fx.ResultTags(`name:"balda_mcp_servers"`))), fx.Populate(&catalog, &binder))
				if err := container.Err(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = catalog.MCP().Shutdown(context.Background()) })
				candidate, err := catalog.PreparePluginCandidate(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := catalog.PublishCandidate(t.Context(), candidate); err != nil {
					t.Fatal(err)
				}
				probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), bridge)
				if err != nil {
					t.Fatal(err)
				}
				definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configs, providers, "hosted", nil), catalog, probe)
				if err != nil {
					t.Fatal(err)
				}
				flows, err := mcpmanage.NewAuthorizations(grants, origin+scenario.base+"/mcp/oauth/callback", definitions)
				if err != nil {
					t.Fatal(err)
				}
				operations := mcpbackofficeapp.New(definitions, catalog)
				currentOperations = operations
				if err := operations.ConfigureAuthorizations(flows); err != nil {
					t.Fatal(err)
				}
				runtime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{ListenAddr: address, PublicURL: origin, BasePath: scenario.base, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, p)
				if err != nil {
					t.Fatal(err)
				}
				if !bootstrapped {
					if _, err := runtime.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{Username: "superuser", Password: []byte("correct-horse-battery")}); err != nil {
						t.Fatal(err)
					}
				}
				bootstrapped = true
				if err := runtime.ConfigureMCPOperations(operations); err != nil {
					t.Fatal(err)
				}
				if err := runtime.ConfigureMCPAuthorizations(&oauthBrowserAuthorizations{MCPAuthorizations: operations, deviceBegins: &issuer.deviceBegins, retryChecks: &retryChecks, t: t}); err != nil {
					t.Fatal(err)
				}
				if err := runtime.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { flows.Close(); _ = runtime.Stop(context.Background()) })
				return runtime, catalog, registry, binder, flows
			}
			runtime, catalog, registry, binder, flows := start("/mcp")
			captures, err := p.MCP().ListMCPConnections(t.Context())
			if err != nil || len(captures) != 0 {
				t.Fatal("startup must leave fresh configured capture empty")
			}
			inventory, err := currentOperations.Inventory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			publicFound := false
			for _, item := range inventory {
				if item.Connection.PublicID == "public-tools" {
					publicFound = !item.Definition.OAuth && item.Recovery == "" && item.Authorization == ""
				}
			}
			if !publicFound {
				t.Fatal("configured public entry unexpectedly requires OAuth or recovery")
			}
			publicBefore, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, publicBefore.ID, "/public", "public-hosted", "public-acp")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "configured-public", scenario.width)
			publicAfter, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, publicBefore.ID, "/public", "public-hosted", "public-acp")
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, publicAfter.ID, "/public", "public-hosted", "public-acp")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "fresh", scenario.width)
			pinned, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, pinned.ID, "/mcp")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "managed-create-"+scenario.transport, scenario.width)
			managedPin, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, managedPin.ID, "/mcp", "managed-hosted", "managed-acp")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "managed-partial-"+scenario.transport, scenario.width)
			partialPin, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, partialPin.ID, "/mcp", "partial-hosted", "partial-acp")
			_ = oauthBrowserConnectionRevision(t, p, "partial-tools")
			managedBefore := oauthBrowserConnectionRevision(t, p, "managed-tools")
			for _, intent := range []string{"missing", "equal"} {
				runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "managed-"+intent+"-"+scenario.transport, scenario.width)
				unchanged := oauthBrowserConnectionRevision(t, p, "managed-tools")
				if !sameOAuthBrowserRevision(unchanged, managedBefore) {
					t.Fatal("missing or unchanged native scopes replaced the bound revision")
				}
				verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, managedPin.ID, "/mcp", "managed-hosted", "managed-acp")
			}
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "managed-scopes-"+scenario.transport, scenario.width)
			managedAfter := oauthBrowserConnectionRevision(t, p, "managed-tools")
			retained, found, err := p.MCP().GetMCPRevision(t.Context(), managedBefore.ConnectionID, managedBefore.ID)
			if err != nil || !found || !sameOAuthBrowserRevision(retained, managedBefore) || managedAfter.ID == managedBefore.ID || len(managedAfter.Definition.Scopes) != 0 || managedAfter.Definition.AuthBinding == nil {
				t.Fatal("native scope change lost the exact historical revision or current authorization")
			}
			if _, err := grants.RequestCredentials(t.Context(), *managedBefore.Definition.AuthBinding, managedBefore.Definition.Scopes); !errors.Is(err, mcpcmd.ErrAuthRequired) {
				t.Fatal("narrowed shared authorization did not fail the scoped historical pin closed")
			}
			scopePin, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, scopePin.ID, "/mcp", "managed-hosted", "managed-acp")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "managed-restore-"+scenario.transport, scenario.width)
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, managedPin.ID, "/mcp", "managed-hosted", "managed-acp")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "managed-static-"+scenario.transport, scenario.width)
			staticPin, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, staticPin.ID, "/mcp", "static-hosted", "static-acp")
			staticCurrent := oauthBrowserConnectionRevision(t, p, "static-tools")
			if staticCurrent.Definition.AuthBinding == nil || staticCurrent.Definition.AuthBinding.ClientID != oauthBrowserConfidentialClientID {
				t.Fatal("native static entry lost its confidential client identity")
			}
			staticGrant, found, err := p.MCP().GetMCPGrant(t.Context(), *staticCurrent.Definition.AuthBinding)
			if err != nil || !found || staticGrant.TokenEndpointAuthMethod != mcpcmd.ClientAuthSecretBasic {
				t.Fatal("native confidential client did not retain its required authentication method")
			}
			revisions, err := p.MCP().ListMCPRevisions(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			staticRetained := false
			for _, revision := range revisions {
				if revision.ConnectionID == staticCurrent.ConnectionID && !revision.Definition.OAuth && revision.Definition.Headers["X-Worker-Secret"].Kind == mcpcmd.ValueProtected {
					staticRetained = revision.ID != staticCurrent.ID && revision.Definition.AuthBinding == nil
				}
			}
			if !staticRetained || staticCurrent.Definition.AuthBinding == nil {
				t.Fatal("native authorization of static managed server changed its historical definition")
			}
			if scenario.flow == "device" {
				runOAuthBrowserRestart(t, origin+scenario.base, issuer.server.URL, scenario.flow, "pending-restart", scenario.width, func() {
					flows.Close()
					if err := runtime.Stop(t.Context()); err != nil {
						t.Fatal(err)
					}
					if err := catalog.MCP().Shutdown(t.Context()); err != nil {
						t.Fatal(err)
					}
					runtime, catalog, registry, binder, flows = start("/mcp")
				})
			}
			// Restart replaces the current declaration while retained pins keep their
			// original protected headers and exact resource identity.
			flows.Close()
			if err := runtime.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := catalog.MCP().Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, restarted, nextRegistry, nextBinder, _ := start("/changed")
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "recapture", scenario.width)
			current, err := restarted.Store().Application()
			if err != nil || current.ID == pinned.ID {
				t.Fatal("changed configuration did not publish a new snapshot")
			}
			verifyOAuthBrowserExecution(t, restarted, nextRegistry, nextBinder, providers, pinned.ID, "/mcp")
			verifyOAuthBrowserExecution(t, restarted, nextRegistry, nextBinder, providers, current.ID, "/changed")
			verifyOAuthBrowserExecution(t, restarted, nextRegistry, nextBinder, providers, managedPin.ID, "/mcp", "managed-hosted", "managed-acp")
			verifyOAuthBrowserExecution(t, restarted, nextRegistry, nextBinder, providers, staticPin.ID, "/mcp", "static-hosted", "static-acp")
			if scenario.flow == "browser" && issuer.registrations.Load() == 0 {
				t.Fatal("managed browser onboarding did not use supported client registration")
			}
			if issuer.hostedCalls.Load() < 3 || issuer.acpCalls.Load() < 3 {
				t.Fatal("native authorization did not reach actual hosted and ACP invocation")
			}
			if retryChecks.Load() < 8 {
				t.Fatal("native retries were not verified against actual returned revision identities")
			}
		})
	}
}

func oauthBrowserConnectionRevision(t *testing.T, p state.Provider, publicID string) mcpcmd.Revision {
	t.Helper()
	connections, err := p.MCP().ListMCPConnections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var selected mcpcmd.Connection
	count := 0
	for _, connection := range connections {
		if connection.PublicID == publicID {
			selected = connection
			count++
		}
	}
	if count != 1 {
		t.Fatal("native onboarding did not retain exactly one connection")
	}
	revision, found, err := p.MCP().GetMCPRevision(t.Context(), selected.ID, selected.CurrentRevisionID)
	if err != nil || !found {
		t.Fatal("current native authorization revision unavailable")
	}
	return revision
}

func sameOAuthBrowserRevision(a, b mcpcmd.Revision) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return bytes.Equal(first, second)
}

func runOAuthBrowser(t *testing.T, base, issuer, flow, phase string, width int) {
	t.Helper()
	runOAuthBrowserRestart(t, base, issuer, flow, phase, width, nil)
}

func runOAuthBrowserRestart(t *testing.T, base, issuer, flow, phase string, width int, restart func()) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/mcp-oauth.cjs", base, issuer, flow, phase, fmt.Sprint(width))
	command.Dir = root
	var output []byte
	if restart == nil {
		output, err = command.CombinedOutput()
	} else {
		markers := t.TempDir()
		command.Args = append(command.Args, markers)
		var captured bytes.Buffer
		command.Stdout, command.Stderr = &captured, &captured
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill() })
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		deadline := time.NewTimer(45 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		waiting := true
		for waiting {
			select {
			case err = <-done:
				t.Fatalf("pending device phase ended before restart: %v\n%s", err, captured.Bytes())
			case <-deadline.C:
				t.Fatal("pending device phase did not reach restart checkpoint")
			case <-ticker.C:
				if _, markerErr := os.Stat(filepath.Join(markers, "pending")); markerErr == nil {
					waiting = false
				}
			}
		}
		restart()
		if err := os.WriteFile(filepath.Join(markers, "restarted"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		err = <-done
		output = captured.Bytes()
	}
	if err != nil {
		t.Fatalf("ordinary-login OAuth browser phase %s failed: %v\n%s", phase, err, output)
	}
	t.Log(string(output))
}

type backofficeOAuthRequest struct{ resource, challenge, redirect, state, clientID string }
type backofficeOAuthDevice struct{ resource, clientID string }

type oauthBrowserAuthorizations struct {
	backoffice.MCPAuthorizations
	deviceBegins *atomic.Int32
	retryChecks  *atomic.Int32
	t            *testing.T
}

func (a *oauthBrowserAuthorizations) BeginDevice(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error) {
	a.deviceBegins.Add(1)
	return a.MCPAuthorizations.BeginDevice(ctx, request)
}

func (a *oauthBrowserAuthorizations) CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error) {
	a.deviceBegins.Add(1)
	return a.MCPAuthorizations.CreateAndBeginDevice(ctx, creation, request)
}

func (a *oauthBrowserAuthorizations) RetryAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	item, err := a.MCPAuthorizations.RetryAuthorization(ctx, request)
	if err == nil {
		if item.Connection.ID != request.ConnectionID || item.Connection.CurrentRevisionID != request.ExpectedRevisionID {
			a.t.Error("native retry changed its exact selected connection or revision")
		}
		a.retryChecks.Add(1)
	}
	return item, err
}

type backofficeOAuthIssuer struct {
	server              *httptest.Server
	mu                  sync.Mutex
	codes               map[string]backofficeOAuthRequest
	devices             map[string]backofficeOAuthDevice
	approved            map[string]bool
	unavailable         atomic.Bool
	deviceUnsupported   atomic.Bool
	metadataUnavailable atomic.Bool
	registrations       atomic.Int32
	deviceStarts        atomic.Int32
	deviceBegins        atomic.Int32
	exchanges           atomic.Int32
	hostedCalls         atomic.Int32
	acpCalls            atomic.Int32
}

func newBackofficeOAuthIssuer(t *testing.T, transport string) *backofficeOAuthIssuer {
	t.Helper()
	f := &backofficeOAuthIssuer{codes: make(map[string]backofficeOAuthRequest), devices: make(map[string]backofficeOAuthDevice), approved: make(map[string]bool)}
	handlers := make(map[string]http.Handler)
	for _, resource := range []string{"/mcp", "/changed", "/public"} {
		sdk := mcp.NewServer(&mcp.Implementation{Name: "oauth-browser-tools", Version: "1"}, nil)
		mcp.AddTool(sdk, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, any, error) {
			if args.Text == "actual hosted tool" {
				f.hostedCalls.Add(1)
			}
			if args.Text == "actual ACP tool" {
				f.acpCalls.Add(1)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text + " " + resource}}}, nil, nil
		})
		var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, nil)
		if transport == "sse" {
			handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return sdk }, nil)
		}
		handlers[resource] = handler
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := f.server.URL
		switch {
		case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource"):
			if f.metadataUnavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			resource := strings.TrimPrefix(r.URL.Path, "/.well-known/oauth-protected-resource")
			if resource == "" {
				resource = "/mcp"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": origin + resource, "authorization_servers": []string{origin}, "scopes_supported": []string{"tools:read"}})
		case r.URL.Path == "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			metadata := map[string]any{"issuer": origin, "authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "registration_endpoint": origin + "/register", "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic"}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"}, "scopes_supported": []string{"tools:read"}, "authorization_response_iss_parameter_supported": true}
			if !f.deviceUnsupported.Load() {
				metadata["device_authorization_endpoint"] = origin + "/device"
			}
			_ = json.NewEncoder(w).Encode(metadata)
		case r.URL.Path == "/register":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.registrations.Add(1)
			var registration struct {
				RedirectURIs []string `json:"redirect_uris"`
			}
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil || len(registration.RedirectURIs) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": oauthBrowserClientID, "token_endpoint_auth_method": "none", "redirect_uris": registration.RedirectURIs, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
		case r.URL.Path == "/authorize":
			if r.Method == http.MethodGet {
				q := r.URL.Query()
				if (q.Get("client_id") != oauthBrowserClientID && q.Get("client_id") != oauthBrowserConfidentialClientID) || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("resource") == "" {
					t.Error("browser authorization lost protocol bindings")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				code := rand.Text()
				f.mu.Lock()
				f.codes[code] = backofficeOAuthRequest{resource: q.Get("resource"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), state: q.Get("state"), clientID: q.Get("client_id")}
				f.mu.Unlock()
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "text/html")
				_, _ = fmt.Fprintf(w, `<!doctype html><form method="post"><label>Service account<input name="account" required></label><label>Service password<input name="password" type="password" required></label><input name="code" type="hidden" value="%s"><button name="decision" value="accept">Authorize worker</button><button name="decision" value="deny">Deny worker</button></form>`, code)
				return
			}
			_ = r.ParseForm()
			if r.Form.Get("account") != "service-user" || r.Form.Get("password") != "synthetic-service-password" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.mu.Lock()
			request, ok := f.codes[r.Form.Get("code")]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			target, _ := url.Parse(request.redirect)
			q := target.Query()
			q.Set("state", request.state)
			q.Set("iss", origin)
			if r.Form.Get("decision") == "deny" {
				q.Set("error", "access_denied")
			} else {
				q.Set("code", r.Form.Get("code"))
			}
			target.RawQuery = q.Encode()
			http.Redirect(w, r, target.String(), http.StatusSeeOther)
		case r.URL.Path == "/device":
			_ = r.ParseForm()
			if !validOAuthBrowserClient(r) || r.Form.Get("resource") == "" {
				t.Error("device authorization lost worker resource/client")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			code := rand.Text()
			f.deviceStarts.Add(1)
			f.mu.Lock()
			f.devices[code] = backofficeOAuthDevice{resource: r.Form.Get("resource"), clientID: oauthBrowserRequestClientID(r)}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": code, "user_code": "SYNTHETIC-CODE", "verification_uri": origin + "/verify", "verification_uri_complete": origin + "/verify?device=" + code, "expires_in": 120, "interval": 1})
		case r.URL.Path == "/verify":
			code := r.URL.Query().Get("device")
			if r.Method == http.MethodPost {
				_ = r.ParseForm()
				if r.Form.Get("account") != "service-user" || r.Form.Get("password") != "synthetic-service-password" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				code = r.Form.Get("device")
				f.mu.Lock()
				f.approved[code] = true
				f.mu.Unlock()
				_, _ = io.WriteString(w, "Worker authorization accepted")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<!doctype html><form method="post"><label>Service account<input name="account" required></label><label>Service password<input name="password" type="password" required></label><input type="hidden" name="device" value="%s"><button>Authorize worker</button></form>`, code)
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			resource := r.Form.Get("resource")
			valid := false
			f.mu.Lock()
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				request, ok := f.codes[r.Form.Get("code")]
				valid = ok && request.resource == resource && request.clientID == oauthBrowserRequestClientID(r) && request.redirect == r.Form.Get("redirect_uri") && request.challenge == oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier"))
				delete(f.codes, r.Form.Get("code"))
			case "urn:ietf:params:oauth:grant-type:device_code":
				code := r.Form.Get("device_code")
				valid = f.devices[code].resource == resource && f.devices[code].clientID == oauthBrowserRequestClientID(r) && f.approved[code]
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if !valid {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			if !validOAuthBrowserClient(r) {
				t.Error("token exchange lost client")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.exchanges.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic-browser-access", "refresh_token": "synthetic-browser-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "tools:read", "resource": resource})
		case r.URL.Path == "/fixture/unavailable":
			f.unavailable.Store(r.URL.Query().Get("value") == "true")
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/fixture/device-support":
			f.deviceUnsupported.Store(r.URL.Query().Get("value") == "false")
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/fixture/metadata-unavailable":
			f.metadataUnavailable.Store(r.URL.Query().Get("value") == "true")
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/fixture/counts":
			_ = json.NewEncoder(w).Encode(map[string]int32{"exchanges": f.exchanges.Load(), "device_starts": f.deviceStarts.Load(), "device_begins": f.deviceBegins.Load()})
		default:
			resource := "/mcp"
			if strings.HasPrefix(r.URL.Path, "/changed") {
				resource = "/changed"
			}
			if strings.HasPrefix(r.URL.Path, "/public") {
				resource = "/public"
			}
			if r.Header.Get("X-Worker-Secret") != "synthetic-oauth-header"+resource || r.Header.Get(mcpbridge.CapabilityHeader) != "" {
				t.Error("upstream protected-header boundary crossed")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if resource != "/public" && r.Header.Get("Authorization") != "Bearer synthetic-browser-access" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource%s"`, origin, r.URL.Path))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if f.unavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			handlers[resource].ServeHTTP(w, r)
		}
	}))
	// A foreign site, rather than only a different port, exercises native
	// OAuth callback cookie eligibility against Backoffice at 127.0.0.1.
	f.server.URL = strings.Replace(f.server.URL, "127.0.0.1", "localhost", 1)
	t.Cleanup(func() { f.server.CloseClientConnections(); f.server.Close() })
	return f
}

func validOAuthBrowserClient(r *http.Request) bool {
	if id, secret, basic := r.BasicAuth(); basic {
		return id == oauthBrowserConfidentialClientID && secret == "synthetic-client-secret"
	}
	return r.Form.Get("client_id") == oauthBrowserClientID
}

func oauthBrowserRequestClientID(r *http.Request) string {
	if id, _, basic := r.BasicAuth(); basic {
		return id
	}
	return r.Form.Get("client_id")
}
