package backoffice

import (
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

func TestMCPOnboardingNativeGuardsAndSavedFailure(t *testing.T) {
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
	item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "saved", PublicID: "onboarding", Source: mcpcmd.SourceManaged}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP}, Status: mcpcmd.StatusPending}
	flows := &mcpAuthorizationFixture{item: item}
	app.mcp, app.mcpAuthorizations = &mcpHTTPFixture{items: []mcpcmd.Item{item}}, flows
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, string(usercmd.RoleAdministrator))
	operator := loginHTTPAppSession(t, handler, config, string(usercmd.RoleOperator))
	for _, flow := range []string{"browser", "device"} {
		path := "/mcp/connections/oauth/" + flow
		for _, tc := range []struct {
			name, access, csrf string
			hx                 bool
			want               int
		}{
			{"anonymous", "", "none", false, 401}, {"operator", operator.access, operator.csrf, false, 403},
			{"bad csrf", admin.access, "invalid", false, 403}, {"fragment", admin.access, admin.csrf, true, 400},
		} {
			t.Run(flow+"/"+tc.name, func(t *testing.T) {
				w := performAccessMutation(t, handler, config, path, url.Values{"csrf_token": {tc.csrf}, "client_secret": {"private-marker"}}, tc.access, tc.csrf, tc.hx)
				if w.Code != tc.want || flows.creations != 0 || strings.Contains(w.Body.String(), "private-marker") {
					t.Fatalf("native guard status/calls = %d/%d, want %d/0", w.Code, flows.creations, tc.want)
				}
			})
		}
	}
	form := url.Values{"csrf_token": {admin.csrf}, "public_id": {"onboarding"}, "transport": {"http"}, "url": {"https://tools.example/mcp"}, "targets_all": {"yes"}, "enabled": {"yes"}, "client_id": {"registered-client"}, "client_secret": {"private-marker"}, "scopes": {"tools.read\ntools.write"}}
	flows.beginErr = mcpcmd.ErrUnavailable
	w := performAccessMutation(t, handler, config, "/mcp/connections/oauth/browser", form, admin.access, admin.csrf, false)
	if w.Code != http.StatusServiceUnavailable || flows.creations != 1 || flows.creation.PublicID != "onboarding" || len(flows.begin.Scopes) != 2 || flows.begin.Authority.UserID != string(usercmd.RoleAdministrator) {
		t.Fatalf("saved onboarding failure status/calls = %d/%d", w.Code, flows.creations)
	}
	if !strings.Contains(w.Body.String(), `href="/mcp/connections/saved"`) || !strings.Contains(w.Body.String(), "Connection saved") || strings.Contains(w.Body.String(), "private-marker") {
		t.Fatal("failed begin lost saved retry path or exposed secret")
	}
	flows.beginErr = nil
	w = performAccessMutation(t, handler, config, "/mcp/connections/oauth/device", form, admin.access, admin.csrf, false)
	if w.Code != http.StatusOK || flows.creations != 2 || !strings.Contains(w.Body.String(), "one-time-user-code") || w.Header().Get("Cache-Control") != noStoreCacheControl {
		t.Fatal("native device creation did not return private instructions")
	}
	form.Set("padding", strings.Repeat("a", 32<<10))
	w = performAccessMutation(t, handler, config, "/mcp/connections/oauth/browser", form, admin.access, admin.csrf, false)
	if w.Code != http.StatusSeeOther || flows.creations != 3 || cookieValue(w.Result().Cookies(), security.OAuthCallbackCookieName) == "" {
		t.Fatal("native browser onboarding lost MCP body limit or callback credential")
	}
	form.Set("padding", strings.Repeat("a", (1<<20)+1))
	w = performAccessMutation(t, handler, config, "/mcp/connections/oauth/browser", form, admin.access, admin.csrf, false)
	if w.Code != 400 || flows.creations != 3 {
		t.Fatal("oversized onboarding reached owner")
	}
	form.Del("padding")
	r := httptest.NewRequest(http.MethodPost, "/mcp/connections/oauth/browser", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", config.Server.PublicURL)
	r.Header.Set("HX-History-Restore-Request", "true")
	r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	r.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: admin.csrf})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 || flows.creations != 3 {
		t.Fatal("history-restored onboarding reached owner")
	}
}

func TestMCPBeginRequestDistinguishesMissingAndEmptyScopes(t *testing.T) {
	for _, tc := range []struct {
		form    url.Values
		missing bool
		count   int
	}{
		{url.Values{}, true, 0}, {url.Values{"scopes": {""}}, false, 0}, {url.Values{"scopes": {"tools.read\ntools.write"}}, false, 2},
	} {
		request := mcpBeginRequest(tc.form, "remote", mcpcmd.Authority{})
		if (request.Scopes == nil) != tc.missing || len(request.Scopes) != tc.count {
			t.Fatalf("scope intent missing/count = %t/%d, want %t/%d", request.Scopes == nil, len(request.Scopes), tc.missing, tc.count)
		}
	}
}
