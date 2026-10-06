package balda

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

const oauthBrowserClientID = "browser-client"

// This gate starts ordinary Backoffice authentication and the production owners.
// The only remote services are a controlled OAuth issuer, MCP server and model.
func TestBackofficeMCPOAuthBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, scenario := range []struct {
		transport, flow, base string
		width                 int
		js                    bool
	}{
		{"http", "browser", "", 1440, true}, {"http", "device", "/balda", 390, false},
		{"sse", "browser", "/balda", 1440, false}, {"sse", "device", "", 390, true},
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
			bootstrapped := false
			start := func(resource string) (*backoffice.Runtime, *catalogapp.Runtime, *mcpregistry.MapRegistry, baldaagent.SessionCapabilityBinder, *mcpmanage.Authorizations) {
				registry := mcpregistry.New(nil)
				configs := map[string]agentconfig.MCPServerConfig{"worker-tools": {Type: agentconfig.MCPServerType(scenario.transport), URL: issuer.server.URL + resource, Headers: map[string]string{"X-Worker-Secret": "synthetic-oauth-header" + resource}}}
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
				if err := runtime.ConfigureMCPAuthorizations(&oauthBrowserAuthorizations{MCPAuthorizations: operations, deviceBegins: &issuer.deviceBegins}); err != nil {
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
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "fresh", scenario.width, scenario.js)
			pinned, err := catalog.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			verifyOAuthBrowserExecution(t, catalog, registry, binder, providers, pinned.ID, "/mcp")
			if scenario.flow == "device" {
				runOAuthBrowserRestart(t, origin+scenario.base, issuer.server.URL, scenario.flow, "pending-restart", scenario.width, scenario.js, func() {
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
			runOAuthBrowser(t, origin+scenario.base, issuer.server.URL, scenario.flow, "recapture", scenario.width, scenario.js)
			current, err := restarted.Store().Application()
			if err != nil || current.ID == pinned.ID {
				t.Fatal("changed configuration did not publish a new snapshot")
			}
			verifyOAuthBrowserExecution(t, restarted, nextRegistry, nextBinder, providers, pinned.ID, "/mcp")
			verifyOAuthBrowserExecution(t, restarted, nextRegistry, nextBinder, providers, current.ID, "/changed")
			if issuer.hostedCalls.Load() < 3 || issuer.acpCalls.Load() < 3 {
				t.Fatal("native authorization did not reach actual hosted and ACP invocation")
			}
		})
	}
}

func runOAuthBrowser(t *testing.T, base, issuer, flow, phase string, width int, js bool) {
	t.Helper()
	runOAuthBrowserRestart(t, base, issuer, flow, phase, width, js, nil)
}

func runOAuthBrowserRestart(t *testing.T, base, issuer, flow, phase string, width int, js bool, restart func()) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/mcp-oauth.cjs", base, issuer, flow, phase, fmt.Sprint(width), fmt.Sprint(js))
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

type backofficeOAuthRequest struct{ resource, challenge, redirect, state string }

type oauthBrowserAuthorizations struct {
	backoffice.MCPAuthorizations
	deviceBegins *atomic.Int32
}

func (a *oauthBrowserAuthorizations) BeginDevice(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error) {
	a.deviceBegins.Add(1)
	return a.MCPAuthorizations.BeginDevice(ctx, request)
}

type backofficeOAuthIssuer struct {
	server            *httptest.Server
	mu                sync.Mutex
	codes             map[string]backofficeOAuthRequest
	devices           map[string]string
	approved          map[string]bool
	unavailable       atomic.Bool
	deviceUnsupported atomic.Bool
	deviceStarts      atomic.Int32
	deviceBegins      atomic.Int32
	exchanges         atomic.Int32
	hostedCalls       atomic.Int32
	acpCalls          atomic.Int32
}

func newBackofficeOAuthIssuer(t *testing.T, transport string) *backofficeOAuthIssuer {
	t.Helper()
	f := &backofficeOAuthIssuer{codes: make(map[string]backofficeOAuthRequest), devices: make(map[string]string), approved: make(map[string]bool)}
	handlers := make(map[string]http.Handler)
	for _, resource := range []string{"/mcp", "/changed"} {
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
			resource := strings.TrimPrefix(r.URL.Path, "/.well-known/oauth-protected-resource")
			if resource == "" {
				resource = "/mcp"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": origin + resource, "authorization_servers": []string{origin}, "scopes_supported": []string{"tools:read"}})
		case r.URL.Path == "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			metadata := map[string]any{"issuer": origin, "authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"}, "scopes_supported": []string{"tools:read"}, "authorization_response_iss_parameter_supported": true}
			if !f.deviceUnsupported.Load() {
				metadata["device_authorization_endpoint"] = origin + "/device"
			}
			_ = json.NewEncoder(w).Encode(metadata)
		case r.URL.Path == "/authorize":
			if r.Method == http.MethodGet {
				q := r.URL.Query()
				if q.Get("client_id") != oauthBrowserClientID || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("resource") == "" {
					t.Error("browser authorization lost protocol bindings")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				code := rand.Text()
				f.mu.Lock()
				f.codes[code] = backofficeOAuthRequest{resource: q.Get("resource"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), state: q.Get("state")}
				f.mu.Unlock()
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "text/html")
				_, _ = fmt.Fprintf(w, `<!doctype html><form method="post"><input name="code" type="hidden" value="%s"><button name="decision" value="accept">Authorize worker</button><button name="decision" value="deny">Deny worker</button></form>`, code)
				return
			}
			_ = r.ParseForm()
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
			if r.Form.Get("client_id") != oauthBrowserClientID || r.Form.Get("resource") == "" {
				t.Error("device authorization lost worker resource/client")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			code := rand.Text()
			f.deviceStarts.Add(1)
			f.mu.Lock()
			f.devices[code] = r.Form.Get("resource")
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": code, "user_code": "SYNTHETIC-CODE", "verification_uri": origin + "/verify", "verification_uri_complete": origin + "/verify?device=" + code, "expires_in": 120, "interval": 1})
		case r.URL.Path == "/verify":
			code := r.URL.Query().Get("device")
			if r.Method == http.MethodPost {
				_ = r.ParseForm()
				code = r.Form.Get("device")
				f.mu.Lock()
				f.approved[code] = true
				f.mu.Unlock()
				_, _ = io.WriteString(w, "Worker authorization accepted")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<!doctype html><form method="post"><input type="hidden" name="device" value="%s"><button>Authorize worker</button></form>`, code)
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			resource := r.Form.Get("resource")
			valid := false
			f.mu.Lock()
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				request, ok := f.codes[r.Form.Get("code")]
				valid = ok && request.resource == resource && request.redirect == r.Form.Get("redirect_uri") && request.challenge == oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier"))
				delete(f.codes, r.Form.Get("code"))
			case "urn:ietf:params:oauth:grant-type:device_code":
				code := r.Form.Get("device_code")
				valid = f.devices[code] == resource && f.approved[code]
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if !valid {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			if r.Form.Get("client_id") != oauthBrowserClientID {
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
		case r.URL.Path == "/fixture/counts":
			_ = json.NewEncoder(w).Encode(map[string]int32{"exchanges": f.exchanges.Load(), "device_starts": f.deviceStarts.Load(), "device_begins": f.deviceBegins.Load()})
		default:
			resource := "/mcp"
			if strings.HasPrefix(r.URL.Path, "/changed") {
				resource = "/changed"
			}
			if r.Header.Get("X-Worker-Secret") != "synthetic-oauth-header"+resource || r.Header.Get(mcpbridge.CapabilityHeader) != "" {
				t.Error("upstream protected-header boundary crossed")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.Header.Get("Authorization") != "Bearer synthetic-browser-access" {
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
