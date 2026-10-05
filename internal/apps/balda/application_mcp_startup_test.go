package balda

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

func TestMCPAuthorizationStartupKeepsAuthenticatedManagementReachable(t *testing.T) {
	for _, transport := range []agentconfig.MCPServerType{agentconfig.MCPServerTypeHTTP, agentconfig.MCPServerTypeSSE} {
		for _, validProvider := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/valid-provider=%t", transport, validProvider), func(t *testing.T) {
				var origin string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, origin))
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer upstream.Close()
				origin = upstream.URL
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
				if _, err := management.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")}); err != nil {
					t.Fatal(err)
				}
				credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
				if err != nil {
					t.Fatal(err)
				}
				bridge := mcpbridge.New(nil, nil)
				registry := mcpregistry.New(nil)
				config := runtimeconfig.RuntimeConfig{
					Providers:  map[string]agentconfig.Config{"alpha": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "fixture"}, MCPServers: []string{"protected"}}},
					MCPServers: map[string]agentconfig.MCPServerConfig{"protected": {Type: transport, URL: upstream.URL, Headers: map[string]string{"X-Worker": "private startup fixture"}}},
				}
				if !validProvider {
					config.Providers["alpha"].OpenAI.APIKey = ""
				}
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
							return pluginapp.NewManaged(stateDir, provider.AppKV(), provider.Plugins(), r)
						}),
					fx.Populate(&catalog, &lifecycle, &binder))
				if err := container.Err(); err != nil {
					t.Fatal(err)
				}
				builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(config.Providers, registry), ScopedFactory: catalogapp.NewProviderFactory(config.Providers, registry), NormaCfg: config})
				manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: "alpha", WorkingDir: stateDir, StateDir: stateDir, CapabilityBinder: binder, MCPRegistry: registry, Logger: zerolog.Nop()})
				params := applicationLifecycleParams{Catalog: lifecycle, CatalogRuntime: catalog, Runtime: manager, Backoffice: management, StateProvider: provider, MCPManagement: credentials, MCPBridge: bridge, Logger: zerolog.Nop()}
				var stages []lifecycleStage
				for _, stage := range applicationLifecycleStages(params, &telegramLifecycle{}) {
					switch stage.name {
					case "user readiness", "managed MCP credential readiness", "MCP credential bridge", "runtime contribution catalog", "provider runtime", "Backoffice HTTP":
						stages = append(stages, stage)
					}
				}
				coordinator := newApplicationLifecycle(zerolog.Nop(), stages)
				startErr := coordinator.Start(t.Context())
				if !validProvider {
					if startErr == nil {
						_ = coordinator.Stop(context.Background())
						t.Fatal("invalid selected provider bypassed startup rollback")
					}
					return
				}
				if err := startErr; err != nil {
					t.Fatalf("fresh authorization-required startup: %v", err)
				}
				defer func() {
					if err := coordinator.Stop(context.Background()); err != nil {
						t.Error(err)
					}
				}()
				if _, err := manager.Runtime(t.Context()); err == nil {
					t.Fatal("startup constructed an incomplete execution runtime")
				}
				captures, err := provider.MCP().ListMCPConnections(t.Context())
				if err != nil || len(captures) != 0 {
					t.Fatal("startup captured OAuth without administrator action")
				}
				verifyAuthenticatedMCPManagement(t, "http://"+address)
			})
		}
	}
}

func verifyAuthenticatedMCPManagement(t *testing.T, origin string) {
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
	u, _ := url.Parse(origin)
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
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("authenticated login = %d", response.StatusCode)
	}
	response, err = client.Get(origin + "/overview")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated management = %d", response.StatusCode)
	}
}
