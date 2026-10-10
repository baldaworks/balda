package backoffice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestMCPBrowserAuthorizationReturnsWithCallbackCredential(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	flows := &mcpAuthorizationFixture{item: mcpcmd.Item{Connection: mcpcmd.Connection{ID: "worker"}}}
	app.mcp, app.mcpAuthorizations = &mcpHTTPFixture{}, flows
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, "admin")
	principal, err := app.security.ValidateAccess(t.Context(), admin.access)
	if err != nil {
		t.Fatal(err)
	}
	begin := performAccessMutation(t, handler, config, "/mcp/connections/worker/oauth/browser", url.Values{"csrf_token": {admin.csrf}}, admin.access, admin.csrf, false)
	if begin.Code != http.StatusSeeOther {
		t.Fatalf("begin status = %d, want 303", begin.Code)
	}
	credential := cookieValue(begin.Result().Cookies(), "balda_oauth_callback")
	if credential == "" {
		t.Fatal("native begin did not issue a callback-scoped credential for the external issuer return")
	}
	request := httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback?state=private-state&code=private-code", nil)
	request.AddCookie(&http.Cookie{Name: "balda_oauth_callback", Value: credential})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/mcp/oauth/return" {
		t.Fatalf("external callback = %d %q, want native return landing", response.Code, response.Header().Get("Location"))
	}
	if flows.callback.Authority.SessionID != principal.FamilyID || flows.callback.Authority.SessionVersion != principal.Version || flows.callback.Authority.UserID != principal.User.ID || flows.callback.Authority.UserVersion != principal.User.Version || flows.callback.Authority.CredentialVersion != principal.User.Credential.Version || flows.callback.Authority.MFAVersion != principal.MFAVersion {
		t.Fatal("callback did not supply the initiating canonical administrator family and current authority fences")
	}
	cleared := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == security.OAuthCallbackCookieName && cookie.Path == "/mcp/oauth/callback" && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared || response.Header().Get("Cache-Control") != noStoreCacheControl || response.Header().Get("Referrer-Policy") != noReferrerPolicy {
		t.Fatal("callback did not clear its credential or retain private response policy")
	}
	for _, secret := range []string{"private-state", "private-code", admin.access} {
		if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Header().Get("Location"), secret) {
			t.Fatal("callback exposed protocol input or its browser credential")
		}
	}
}

func TestMCPOAuthReturnCommitsNativeMetadataDocument(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	config.Server.BasePath = "/balda"
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, hx := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/balda/mcp/oauth/return?oauth_result=denied&state=private-state&code=private-code&connection_id=private-input", nil)
		if hx {
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Target", "main-content")
			r.Header.Set("HX-History-Restore-Request", "true")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html>") || !strings.Contains(w.Body.String(), `href="/balda/mcp?oauth_result=denied" hx-boost="false">Continue to MCP</a>`) || !strings.Contains(w.Body.String(), `hx-history="false"`) {
			t.Fatalf("native landing did not commit a safe document: status=%d", w.Code)
		}
		if w.Header().Get("Cache-Control") != noStoreCacheControl || w.Header().Get("Referrer-Policy") != noReferrerPolicy || len(w.Result().Cookies()) != 0 {
			t.Fatal("landing modified credentials or lost its private response policy")
		}
		for _, secret := range []string{"private-state", "private-code", "private-input"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("landing reflected arbitrary protocol or recovery input")
			}
		}
	}
}

func TestMCPCallbackCannotRetainProtocolInputInSessionRecovery(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback?state=private-state&code=private-code&iss=https%3A%2F%2Fissuer.example", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d, want303", response.Code)
	}
	for _, secret := range []string{"private-state", "private-code", "issuer.example"} {
		if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Header().Get("Location"), secret) {
			t.Fatalf("unauthenticated callback retained %s", secret)
		}
	}
	if response.Header().Get("Location") != "/mcp/oauth/return?oauth_result=forbidden" {
		t.Fatal("session recovery does not return to safe MCP inventory")
	}
	if response.Header().Get("Cache-Control") != noStoreCacheControl {
		t.Fatal("callback lost no-store policy")
	}
}

func TestMCPCallbackFailureLeavesOnlyMetadataInBrowserLocation(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback?state=private-state&code=private-code", nil))
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/mcp/oauth/return?") {
		t.Fatalf("failed callback left its protocol URL in browser location: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestMCPAuthorizationNativeGuardsAndConfiguredSideState(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		id := string(role)
		createAccessTestUser(t, provider.Users(), usercmd.User{ID: id, Username: id, NormalizedUsername: id, DisplayName: id, Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now})
	}
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "config:worker", PublicID: "worker", Source: mcpcmd.SourceConfig, Enabled: true, CurrentRevisionID: "revision"}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp", OAuth: true}, Status: mcpcmd.StatusUnavailable}
	app.mcp = &mcpHTTPFixture{items: []mcpcmd.Item{item}}
	flows := &mcpAuthorizationFixture{item: item}
	app.mcpAuthorizations = flows
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, string(usercmd.RoleAdministrator))
	operator := loginHTTPAppSession(t, handler, config, string(usercmd.RoleOperator))
	path := "/mcp/connections/config:worker/oauth/device"
	for _, tc := range []struct {
		name, access, csrf string
		hx                 bool
		want               int
	}{
		{"anonymous", "", "none", false, 401},
		{"operator", operator.access, operator.csrf, false, 403},
		{"bad csrf", admin.access, "invalid", false, 403},
		{"HTMX protocol begin", admin.access, admin.csrf, true, 400},
	} {
		for _, flow := range []string{"browser", "device"} {
			t.Run(tc.name+" "+flow, func(t *testing.T) {
				w := performAccessMutation(t, handler, config, "/mcp/connections/config:worker/oauth/"+flow, url.Values{"csrf_token": {tc.csrf}, "client_secret": {"must-not-echo"}}, tc.access, tc.csrf, tc.hx)
				if w.Code != tc.want {
					t.Fatalf("native begin = %d, want %d", w.Code, tc.want)
				}
				if flows.begins != 0 || strings.Contains(w.Body.String(), "must-not-echo") || cookieValue(w.Result().Cookies(), security.OAuthCallbackCookieName) != "" {
					t.Fatal("rejected native begin reached owner, reflected a secret, or issued a callback credential")
				}
			})
		}
	}
	w := performAccessMutation(t, handler, config, path, url.Values{"csrf_token": {admin.csrf}, "client_id": {"client"}, "client_auth_method": {"none"}}, admin.access, admin.csrf, false)
	if w.Code != 200 || flows.begins != 1 {
		t.Fatalf("configuredsideauthorization=%d/%d", w.Code, flows.begins)
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("native device cancellation would lose its trusted Origin")
	}
	if !strings.Contains(w.Body.String(), "one-time-user-code") || !strings.Contains(w.Body.String(), `hx-history="false"`) {
		t.Fatal("nativebeginlostone-timeinstructions/historypolicy")
	}
	for _, hx := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/mcp/oauth/device/attempt", nil)
		r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
		if hx {
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Target", "main-content")
			r.Header.Set("HX-History-Restore-Request", "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != 200 || strings.Contains(response.Body.String(), "one-time-user-code") || strings.Contains(response.Body.String(), "device-capability") {
			t.Fatal("device status/history repeated private instructions")
		}
	}
	flows.device.Status = mcpcmd.DeviceAuthorized
	r := httptest.NewRequest(http.MethodGet, "/mcp/oauth/device/attempt", nil)
	r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if !strings.Contains(response.Body.String(), "Open the connection to check whether tools are available") {
		t.Fatal("device grant status was mistaken for runtime readiness")
	}
}

type mcpAuthorizationFixture struct {
	item      mcpcmd.Item
	device    mcpcmd.DeviceAuthorization
	callback  mcpcmd.BrowserCallback
	begins    int
	creations int
	creation  mcpcmd.CreateDefinition
	begin     mcpcmd.BeginAuthorization
	beginErr  error
}

func (f *mcpAuthorizationFixture) CreateAndBeginBrowser(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BrowserAuthorization, error) {
	f.creations++
	f.creation = creation
	request.ConnectionID = f.item.Connection.ID
	started, err := f.BeginBrowser(ctx, request)
	return f.item, started, err
}

func (f *mcpAuthorizationFixture) CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error) {
	f.creations++
	f.creation = creation
	request.ConnectionID = f.item.Connection.ID
	started, err := f.BeginDevice(ctx, request)
	return f.item, started, err
}

func (f *mcpAuthorizationFixture) BeginBrowser(_ context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.BrowserAuthorization, error) {
	f.begins++
	f.begin = request
	return mcpcmd.BrowserAuthorization{AuthorizationURL: "https://issuer.example/authorize?state=private", ExpiresAt: time.Now().UTC().Add(time.Minute)}, f.beginErr
}
func (f *mcpAuthorizationFixture) CompleteBrowser(_ context.Context, callback mcpcmd.BrowserCallback) (mcpcmd.Item, error) {
	f.callback = callback
	return f.item, nil
}
func (f *mcpAuthorizationFixture) BeginDevice(_ context.Context, r mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error) {
	f.begins++
	f.begin = r
	f.device = mcpcmd.DeviceAuthorization{ID: "attempt", ConnectionID: r.ConnectionID, UserCode: "one-time-user-code", VerificationURI: "https://issuer.example/device", VerificationURIComplete: "https://issuer.example/device?device=device-capability", Status: mcpcmd.DevicePending, ExpiresAt: time.Now().Add(time.Minute)}
	return f.device, f.beginErr
}
func (f *mcpAuthorizationFixture) Device(context.Context, string, mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error) {
	return f.device, nil
}
func (*mcpAuthorizationFixture) CurrentAttempt(context.Context, string, mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error) {
	return mcpcmd.AuthorizationAttempt{}, false, nil
}
func (*mcpAuthorizationFixture) Cancel(context.Context, string, mcpcmd.Authority) error { return nil }
func (*mcpAuthorizationFixture) Disconnect(context.Context, string, mcpcmd.Authority) error {
	return nil
}
func (f *mcpAuthorizationFixture) RetryAuthorization(context.Context, mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	return f.item, nil
}
