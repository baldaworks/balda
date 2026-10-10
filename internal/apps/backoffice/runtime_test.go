package backoffice

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

const noReferrerPolicy = "no-referrer"

func TestRuntimeSharedHandlerUsesIndependentBrowserMount(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	config.Server.BasePath = "/legacy"
	runtime, err := NewRuntime(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	shared := config.Server
	shared.PublicURL = "https://lab.metalagman.dev"
	shared.BasePath = "/balda/backoffice"
	shared.SecureCookies = true
	if _, err := runtime.Handler(t.Context(), shared, HandlerServices{}); err == nil {
		t.Fatal("shared handler constructed before administrator bootstrap")
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureWebhooksOperations(&webhooksHTTPFixture{items: map[string]webhookroutecmd.Item{}}); err != nil {
		t.Fatal(err)
	}
	legacyAuthorizations := &mcpAuthorizationFixture{item: mcpcmd.Item{Connection: mcpcmd.Connection{ID: "worker"}}}
	if err := runtime.ConfigureMCPOperations(&mcpHTTPFixture{}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureMCPAuthorizations(legacyAuthorizations); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Handler(t.Context(), shared, HandlerServices{}); err == nil {
		t.Fatal("shared handler reused legacy MCP operations")
	}
	sharedAuthorizations := &mcpAuthorizationFixture{item: mcpcmd.Item{Connection: mcpcmd.Connection{ID: "worker"}}}
	handler, err := runtime.Handler(t.Context(), shared, HandlerServices{MCP: &mcpHTTPFixture{}, MCPAuthorizations: sharedAuthorizations})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/legacy/login", "/balda/webhooks/orders", "/balda/gateway/slack/events"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, response.Code)
		}
	}
	root := httptest.NewRecorder()
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/balda/backoffice/", nil))
	if root.Code != http.StatusSeeOther || root.Header().Get("Location") != "/balda/backoffice/overview" {
		t.Fatalf("shared root = %d, Location %q", root.Code, root.Header().Get("Location"))
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/balda/backoffice/login", nil))
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `action="/balda/backoffice/login"`) {
		t.Fatalf("shared login = %d %q", login.Code, login.Body.String())
	}
	if cookie := login.Result().Cookies()[0]; cookie.Path != "/balda/backoffice/" || !cookie.Secure {
		t.Errorf("browser cookie path/security = %q/%t", cookie.Path, cookie.Secure)
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, appAssetPath(t, login.Body.String(), "js"), nil))
	if asset.Code != http.StatusOK {
		t.Errorf("shared asset = %d", asset.Code)
	}
	sharedConfig := config
	sharedConfig.Server = shared
	admin := loginHTTPAppSession(t, handler, sharedConfig, "superuser")
	overviewRequest := httptest.NewRequest(http.MethodGet, "/balda/backoffice/overview", nil)
	overviewRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	overview := httptest.NewRecorder()
	handler.ServeHTTP(overview, overviewRequest)
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), `href="/balda/backoffice/account"`) {
		t.Errorf("authenticated overview = %d", overview.Code)
	}
	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/balda/backoffice/webhooks", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Errorf("anonymous webhook management = %d", protected.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/balda/backoffice/webhooks", nil)
	request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	managed := httptest.NewRecorder()
	handler.ServeHTTP(managed, request)
	if managed.Code != http.StatusOK {
		t.Errorf("authenticated webhook management = %d", managed.Code)
	}
	badCSRF := performAccessMutation(t, handler, sharedConfig, "/balda/backoffice/webhooks", url.Values{"name": {"orders"}}, admin.access, "bad-csrf", false)
	if badCSRF.Code != http.StatusForbidden {
		t.Errorf("invalid browser CSRF = %d", badCSRF.Code)
	}
	begin := performAccessMutation(t, handler, sharedConfig, "/balda/backoffice/mcp/connections/worker/oauth/browser", url.Values{"csrf_token": {admin.csrf}}, admin.access, admin.csrf, false)
	if begin.Code != http.StatusSeeOther || sharedAuthorizations.begins != 1 || legacyAuthorizations.begins != 0 {
		t.Fatalf("shared OAuth begin = %d, shared=%d legacy=%d", begin.Code, sharedAuthorizations.begins, legacyAuthorizations.begins)
	}
	callbackCredential := cookieValue(begin.Result().Cookies(), security.OAuthCallbackCookieName)
	if callbackCredential == "" {
		t.Fatal("shared OAuth begin omitted callback credential")
	}
	callbackRequest := httptest.NewRequest(http.MethodGet, "/balda/backoffice/mcp/oauth/callback?state=private-state&code=private-code", nil)
	callbackRequest.AddCookie(&http.Cookie{Name: security.OAuthCallbackCookieName, Value: callbackCredential})
	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/balda/backoffice/mcp/oauth/return" || sharedAuthorizations.callback.State != "private-state" || legacyAuthorizations.callback.State != "" {
		t.Errorf("shared OAuth callback = %d, Location %q, shared state %q, legacy state %q", callback.Code, callback.Header().Get("Location"), sharedAuthorizations.callback.State, legacyAuthorizations.callback.State)
	}
	oauth := httptest.NewRecorder()
	handler.ServeHTTP(oauth, httptest.NewRequest(http.MethodGet, "/balda/backoffice/mcp/oauth/callback?state=s&code=c", nil))
	if oauth.Code != http.StatusSeeOther || oauth.Header().Get("Location") != "/balda/backoffice/mcp/oauth/return?oauth_result=forbidden" || oauth.Header().Get("Referrer-Policy") != noReferrerPolicy {
		t.Errorf("anonymous OAuth callback = %d, headers %v", oauth.Code, oauth.Header())
	}
}

func TestRuntimeSharedProviderLifecycle(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.Server.ListenAddr = listener.Addr().String()
	config.Server.BasePath = "/legacy"
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err == nil {
		t.Fatal("Start() succeeded before administrator bootstrap")
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+config.Server.ListenAddr+"/legacy/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	for _, check := range []struct {
		path string
		want int
	}{
		{path: "/legacy/login", want: http.StatusOK},
		{path: "/balda/backoffice/login", want: http.StatusNotFound},
	} {
		response, err := http.Get("http://" + config.Server.ListenAddr + check.path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != check.want {
			t.Errorf("GET %s = %d, want %d", check.path, response.StatusCode, check.want)
		}
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: 1}); err != nil {
		t.Fatalf("provider closed by Backoffice Stop: %v", err)
	}
}

func TestRuntimeStartReturnsBindFailure(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	config.Server.ListenAddr = listener.Addr().String()
	runtime, err := NewRuntime(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), BootstrapInput{Username: "superuser", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err == nil {
		t.Fatal("Start() succeeded with occupied listener")
	}
	if runtime.Done() != nil {
		t.Fatal("serving loop started despite bind failure")
	}
}

func TestHealthHandlerIsNonSensitiveAndNoStore(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	healthHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
}
