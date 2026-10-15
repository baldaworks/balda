package backoffice

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/hookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

func TestHooksHTTPManagement(t *testing.T) {
	for _, base := range []string{"", "/balda/backoffice"} {
		t.Run(base, func(t *testing.T) {
			provider, config := newHTTPAppTestState(t)
			config.Server.BasePath = base
			now := time.Now().UTC()
			for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
				createAccessTestUser(t, provider.Users(), usercmd.User{ID: string(role), Username: string(role), NormalizedUsername: string(role), DisplayName: string(role), Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
			}
			app, err := newHTTPApp(provider.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			management := hooks.NewManagement(provider.HookManagement())
			native, err := hookprovider.New(hooks.NewRunner(provider.HookManagement()), provider.HookManagement(), "provider", map[string]agentconfig.Config{"provider": {Type: agentconfig.AgentTypeClaudeCodeACP, ClaudeCodeACP: &agentconfig.ACPConfig{Cmd: []string{"fixture-acp"}}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := native.Close(); err != nil {
					t.Error(err)
				}
			})
			management.ConfigureValidator(native)
			app.hooks = hookbackofficeapp.New(management)
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin, operator := loginHTTPAppSession(t, handler, config, "administrator"), loginHTTPAppSession(t, handler, config, "operator")
			get := func(path, access string, fragment bool) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, base+path, nil)
				if access != "" {
					request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
				}
				if fragment {
					request.Header.Set("HX-Request", "true")
					request.Header.Set("HX-Target", "main-content")
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			form := url.Values{"name": {"guard"}, "event": {"UserPromptSubmit"}, "command": {"/usr/bin/true"}, "args": {`["command-canary"]`}, "timeout_seconds": {"3600"}, "expected_version": {"0"}, "enabled": {"yes"}, "csrf_token": {admin.csrf}}
			mutate := func(path string, values url.Values, fragment bool) *httptest.ResponseRecorder {
				return performAccessMutation(t, handler, config, base+path, values, admin.access, admin.csrf, fragment)
			}
			for _, tc := range []struct {
				access string
				want   int
			}{{"", 401}, {operator.access, 403}, {admin.access, 200}} {
				if response := get("/hooks", tc.access, false); response.Code != tc.want {
					t.Fatalf("inventory status = %d, want %d", response.Code, tc.want)
				}
			}
			if response := get("/overview", operator.access, false); strings.Contains(response.Body.String(), `href="`+base+`/hooks"`) {
				t.Fatal("operator sees hook navigation")
			}
			if response := mutate("/hooks", form, false); response.Code != 303 || response.Header().Get("Location") != base+"/hooks" {
				t.Fatalf("create = %d, %s", response.Code, response.Body.String())
			}
			inventory := get("/hooks", admin.access, true)
			if inventory.Code != 200 || strings.Contains(inventory.Body.String(), "command-canary") || strings.Contains(inventory.Body.String(), "<!DOCTYPE") || !strings.Contains(inventory.Body.String(), "UserPromptSubmit") {
				t.Fatal("inventory projection or fragment contract differs")
			}
			if response := get("/hooks/guard", admin.access, false); response.Code != 200 || !strings.Contains(response.Body.String(), "command-canary") {
				t.Fatal("saved editor is unavailable")
			}
			form.Set("expected_version", "0")
			form.Set("command", "submitted-canary")
			if response := mutate("/hooks/guard", form, true); response.Code != 409 || !strings.Contains(response.Body.String(), "submitted-canary") {
				t.Fatalf("stale edit = %d", response.Code)
			}
			form.Set("expected_version", "1")
			form.Set("enabled", "")
			form.Set("timeout_seconds", "0")
			if response := mutate("/hooks/guard", form, true); response.Code != 204 || !strings.Contains(response.Header().Get("HX-Location"), base+"/hooks") {
				t.Fatalf("update = %d", response.Code)
			}
			deletion := url.Values{"expected_version": {"2"}, "csrf_token": {admin.csrf}}
			if response := mutate("/hooks/guard/delete", deletion, false); response.Code != 400 {
				t.Fatalf("unconfirmed delete = %d", response.Code)
			}
			deletion.Set("confirm", "yes")
			if response := mutate("/hooks/guard/delete", deletion, false); response.Code != 303 {
				t.Fatalf("delete = %d, %s", response.Code, response.Body.String())
			}
			if response := get("/hooks/guard", admin.access, false); response.Code != 404 {
				t.Fatalf("missing hook = %d", response.Code)
			}
			toolForm := url.Values{"name": {"tool-guard"}, "event": {string(hookcmd.PreToolUse)}, "command": {"/usr/bin/true"}, "args": {`[]`}, "timeout_seconds": {"10"}, "expected_version": {"3"}, "enabled": {"yes"}, "provider": {"missing"}, "csrf_token": {admin.csrf}}
			if response := mutate("/hooks", toolForm, true); response.Code != 400 || !strings.Contains(response.Body.String(), "Review the configured provider") || !strings.Contains(response.Body.String(), `value="missing"`) {
				t.Fatalf("unsupported native draft = %d, %s", response.Code, response.Body.String())
			}
			toolForm.Set("provider", "provider")
			if response := mutate("/hooks", toolForm, false); response.Code != 303 {
				t.Fatalf("PreToolUse creation = %d: %s", response.Code, response.Body.String())
			}
			toolForm.Set("event", string(hookcmd.PostToolUse))
			toolForm.Set("expected_version", "4")
			if response := mutate("/hooks/tool-guard", toolForm, true); response.Code != 204 {
				t.Fatalf("PostToolUse edit = %d", response.Code)
			}
			if response := get("/hooks/tool-guard", admin.access, false); response.Code != 200 || !strings.Contains(response.Body.String(), `value="PostToolUse" selected`) || !strings.Contains(response.Body.String(), "use /reset") {
				t.Fatal("native editor lost event or activation guidance")
			}
			for _, failure := range []string{"csrf", "foreign origin"} {
				request := httptest.NewRequest(http.MethodPost, base+"/hooks", strings.NewReader(form.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.Header.Set("Origin", config.Server.PublicURL)
				if failure == "foreign origin" {
					request.Header.Set("Origin", "https://foreign.invalid")
				}
				request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
				csrf := "wrong"
				if failure == "foreign origin" {
					csrf = admin.csrf
				}
				request.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != 403 {
					t.Fatalf("%s rejection = %d", failure, response.Code)
				}
			}
		})
	}
}
