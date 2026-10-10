package balda

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

const sharedTestBasePath = "/balda"

func TestBackofficeSharedHandlerContribution(t *testing.T) {
	basePath := sharedTestBasePath
	cfg := BaldaConfig{
		Backoffice: backoffice.ServerConfig{ListenAddr: "127.0.0.1:19095", PublicURL: "https://legacy.example.test", BasePath: "/old"},
		HTTP:       HTTPConfig{ListenAddr: testHTTPListenAddr, BaseURL: "https://lab.metalagman.dev", BasePath: &basePath},
	}
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	config, err := backofficeRuntimeConfig(cfg, state.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := backoffice.NewRuntime(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	handler, err := backofficeSharedHandler(t.Context(), runtime, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := httpfx.NewRegistry(sharedTestBasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddBackoffice("backoffice", handler); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		want int
	}{
		{path: "/balda/backoffice/login", want: http.StatusOK},
		{path: "/old/login", want: http.StatusNotFound},
		{path: "/balda/webhooks/orders", want: http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, check.path, nil))
		if response.Code != check.want {
			t.Errorf("GET %s = %d, want %d", check.path, response.Code, check.want)
		}
		if check.want == http.StatusOK && !strings.Contains(response.Body.String(), `action="/balda/backoffice/login"`) {
			t.Errorf("GET %s has wrong browser mount", check.path)
		}
	}
}

func TestBackofficeRuntimeConfigRejectsInvalidSharedHTTPBeforeStartup(t *testing.T) {
	cfg := BaldaConfig{HTTP: HTTPConfig{BaseURL: "https://lab.metalagman.dev/extra"}}
	_, err := backofficeRuntimeConfig(cfg, state.DatabaseConfig{})
	if err == nil || !strings.Contains(err.Error(), "balda.http.base_url") {
		t.Fatalf("invalid shared origin error = %v", err)
	}
}

func TestSharedBackofficeMCPBrowserOAuthUsesSharedCallback(t *testing.T) {
	issuer := newBackofficeOAuthIssuer(t, "http")
	t.Cleanup(issuer.server.Close)
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	catalog, err := catalogapp.NewRuntime(t.TempDir(), "", "", provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
	if err != nil {
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
	definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(provider.MCP()), mcpfx.NewConfiguredDefinitions(nil, map[string]agentconfig.Config{"hosted": {}}, "hosted", nil), catalog, probe)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(provider.MCP()), mcpfx.NewOAuthProvider(issuer.server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	basePath := sharedTestBasePath
	cfg := BaldaConfig{Backoffice: backoffice.ServerConfig{ListenAddr: "127.0.0.1:19095", PublicURL: "https://legacy.example.test", BasePath: "/old"}, HTTP: HTTPConfig{ListenAddr: testHTTPListenAddr, BaseURL: "https://lab.metalagman.dev", BasePath: &basePath}}
	legacyFlow, err := mcpmanage.NewAuthorizations(grants, "https://legacy.example.test/old/mcp/oauth/callback", definitions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(legacyFlow.Close)
	legacyMCP := mcpbackofficeapp.New(definitions, catalog)
	if err := legacyMCP.ConfigureAuthorizations(legacyFlow); err != nil {
		t.Fatal(err)
	}
	sharedMCP, sharedFlow, err := newSharedBackofficeMCP(cfg, grants, definitions, catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sharedFlow.Close)
	runtimeConfig, err := backofficeRuntimeConfig(cfg, state.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := backoffice.NewRuntime(runtimeConfig, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureMCPOperations(legacyMCP); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureMCPAuthorizations(legacyMCP); err != nil {
		t.Fatal(err)
	}
	handler, err := backofficeSharedHandler(t.Context(), runtime, cfg, sharedMCP)
	if err != nil {
		t.Fatal(err)
	}
	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/balda/backoffice/login", nil))
	csrf := testCookieValue(loginPage.Result().Cookies(), security.CSRFCookieName)
	if csrf == "" {
		t.Fatal("login page omitted CSRF cookie")
	}
	loginForm := url.Values{"username": {"superuser"}, "password": {"correct horse battery staple"}, "csrf_token": {csrf}}
	loginRequest := httptest.NewRequest(http.MethodPost, "/balda/backoffice/login", strings.NewReader(loginForm.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", "https://lab.metalagman.dev")
	loginRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	loginRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("admin login = %d", login.Code)
	}
	access := testCookieValue(login.Result().Cookies(), security.AccessCookieName)
	csrf = testCookieValue(login.Result().Cookies(), security.CSRFCookieName)
	form := url.Values{"csrf_token": {csrf}, "public_id": {"worker"}, "transport": {"http"}, "url": {issuer.server.URL + "/mcp"}, "targets_all": {"yes"}, "client_id": {oauthBrowserClientID}, "client_auth_method": {"none"}, "scopes": {"tools:read"}}
	beginRequest := httptest.NewRequest(http.MethodPost, "/balda/backoffice/mcp/connections/oauth/browser", strings.NewReader(form.Encode()))
	beginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	beginRequest.Header.Set("Origin", "https://lab.metalagman.dev")
	beginRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	beginRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
	beginRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
	begin := httptest.NewRecorder()
	handler.ServeHTTP(begin, beginRequest)
	if begin.Code != http.StatusSeeOther {
		t.Fatalf("shared OAuth begin = %d %q", begin.Code, begin.Body.String())
	}
	authorizationURL, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	const callbackURL = "https://lab.metalagman.dev/balda/backoffice/mcp/oauth/callback"
	if got := authorizationURL.Query().Get("redirect_uri"); got != callbackURL {
		t.Fatalf("issued OAuth redirect_uri = %q, want %q", got, callbackURL)
	}
	callbackCredential := testCookieValue(begin.Result().Cookies(), security.OAuthCallbackCookieName)
	if callbackCredential == "" {
		t.Fatal("OAuth begin omitted callback credential")
	}
	issuerResponse, err := issuer.server.Client().Get(authorizationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, issuerResponse.Body)
	_ = issuerResponse.Body.Close()
	if issuerResponse.StatusCode != http.StatusOK {
		t.Fatalf("issuer authorization page = %d", issuerResponse.StatusCode)
	}
	issuer.mu.Lock()
	var code string
	for issuedCode := range issuer.codes {
		code = issuedCode
	}
	issuer.mu.Unlock()
	if code == "" {
		t.Fatal("issuer did not issue authorization code")
	}
	approve := url.Values{"account": {"service-user"}, "password": {"synthetic-service-password"}, "code": {code}, "decision": {"accept"}}
	issuerRequest, err := http.NewRequestWithContext(t.Context(), http.MethodPost, issuer.server.URL+"/authorize", bytes.NewBufferString(approve.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	issuerRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	issuerClient := issuer.server.Client()
	issuerClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	approved, err := issuerClient.Do(issuerRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = approved.Body.Close()
	if approved.StatusCode != http.StatusSeeOther {
		t.Fatalf("issuer approval = %d", approved.StatusCode)
	}
	callbackLocation, err := url.Parse(approved.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if callbackLocation.Scheme+"://"+callbackLocation.Host+callbackLocation.Path != callbackURL {
		t.Fatalf("issuer callback location = %q", callbackLocation.String())
	}
	callbackRequest := httptest.NewRequest(http.MethodGet, callbackLocation.String(), nil)
	callbackRequest.AddCookie(&http.Cookie{Name: security.OAuthCallbackCookieName, Value: callbackCredential})
	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/balda/backoffice/mcp/oauth/return" {
		t.Fatalf("shared OAuth callback = %d, Location %q", callback.Code, callback.Header().Get("Location"))
	}
}

func testCookieValue(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func TestBackofficeRuntimeConfigUsesSharedDatabaseAndSafeCapabilities(t *testing.T) {
	database := state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: "/tmp/balda-test.db"}}
	cfg := BaldaConfig{}
	cfg.Telegram.Token = "secret-telegram-token"
	cfg.Webhooks.Enabled = true
	cfg.Webhooks.Routes = map[string]WebhookRouteConfig{"example": {PromptTemplate: "secret prompt"}}
	cfg.Mattermost = MattermostConfig{Enabled: true, ServerURL: "https://mattermost.example", Token: "secret-mattermost-token", CommandsEnabled: true, CommandsListenAddr: "127.0.0.1:8094", CommandsPath: "/mattermost/commands", CommandsToken: "secret-command-token"}
	resolved, err := backofficeRuntimeConfig(cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Database.SQLite.Path != database.SQLite.Path {
		t.Fatalf("database path = %q, want %q", resolved.Database.SQLite.Path, database.SQLite.Path)
	}
	if !resolved.Balda.Telegram.Enabled || resolved.Balda.Webhooks.RouteCount != 1 {
		t.Fatalf("capability projection = %+v", resolved.Balda)
	}
	if !resolved.Balda.Mattermost.Enabled || !resolved.Balda.Mattermost.CommandsEnabled || resolved.Balda.Mattermost.ListenAddr != cfg.Mattermost.CommandsListenAddr || resolved.Balda.Mattermost.ServerURL != cfg.Mattermost.ServerURL {
		t.Fatalf("Mattermost capability projection = %+v", resolved.Balda.Mattermost)
	}
}
