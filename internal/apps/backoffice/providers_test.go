package backoffice

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func TestProvidersBrowserManagementAndSecretBoundary(t *testing.T) {
	for _, base := range []string{"", "/balda"} {
		t.Run(base, func(t *testing.T) {
			provider, config := newHTTPAppTestState(t)
			config.Server.BasePath = base
			now := time.Now().UTC()
			for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
				createAccessTestUser(t, provider.Users(), usercmd.User{ID: string(role), Username: string(role), NormalizedUsername: string(role), DisplayName: string(role), Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
			}
			protector, err := secrets.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
			if err != nil {
				t.Fatal(err)
			}
			service := providers.New(map[string]agentconfig.Config{"yaml": {Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Model: "configured-model", Env: map[string]string{"YAML_SECRET": "configured-secret"}}}}, provider.ProviderManagement(), protector)
			app, err := newHTTPApp(provider.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			app.providers = service
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "administrator")
			operator := loginHTTPAppSession(t, handler, config, "operator")
			get := func(path, access string, fragment bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, base+path, nil)
				if access != "" {
					r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
				}
				if fragment {
					r.Header.Set("HX-Request", "true")
					r.Header.Set("HX-Target", "main-content")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			if got := get("/providers", "", false).Code; got != http.StatusUnauthorized {
				t.Fatalf("anonymous inventory = %d", got)
			}
			if got := get("/providers", operator.access, false).Code; got != http.StatusForbidden {
				t.Fatalf("operator inventory = %d", got)
			}
			configured := get("/providers/yaml", admin.access, false)
			if configured.Code != http.StatusOK || strings.Contains(configured.Body.String(), "configured-secret") || !strings.Contains(configured.Body.String(), "YAML_SECRET") || !strings.Contains(configured.Body.String(), "Read-only") {
				t.Fatalf("configured detail status=%d body=%s", configured.Code, configured.Body.String())
			}
			create := url.Values{"expected_version": {"0"}, "id": {"managed"}, "type": {agentconfig.AgentTypeCodexACP}, "model": {"gpt-test"}, "inherit_env": {"no"}, "environment_action": {"replace"}, "environment": {"TOKEN=managed-secret"}, "api_key_action": {"keep"}}
			create.Set("csrf_token", admin.csrf)
			if got := performAccessMutation(t, handler, config, base+"/providers", create, operator.access, operator.csrf, false).Code; got != http.StatusForbidden {
				t.Fatalf("operator create = %d", got)
			}
			created := performAccessMutation(t, handler, config, base+"/providers", create, admin.access, admin.csrf, true)
			if created.Code != http.StatusNoContent || !strings.Contains(created.Header().Get("HX-Location"), base+"/providers/managed") {
				t.Fatalf("HTMX create = %d, HX-Location=%q", created.Code, created.Header().Get("HX-Location"))
			}
			detail := get("/providers/managed", admin.access, true)
			body := detail.Body.String()
			if detail.Code != http.StatusOK || strings.Contains(body, "managed-secret") || !strings.Contains(body, "TOKEN") || strings.Contains(body, "<html") || strings.Count(body, `id="main-content"`) != 1 {
				t.Fatalf("managed fragment status=%d body=%s", detail.Code, body)
			}
			update := url.Values{"expected_version": {"1"}, "type": {agentconfig.AgentTypeCodexACP}, "model": {"gpt-updated"}, "inherit_env": {"no"}, "environment_action": {"keep"}, "api_key_action": {"keep"}, "csrf_token": {admin.csrf}}
			if got := performAccessMutation(t, handler, config, base+"/providers/managed", update, admin.access, admin.csrf, false); got.Code != http.StatusSeeOther || got.Header().Get("Location") != base+"/providers/managed" {
				t.Fatalf("native update = %d, location=%q", got.Code, got.Header().Get("Location"))
			}
			if got := performAccessMutation(t, handler, config, base+"/providers/managed", update, admin.access, admin.csrf, false).Code; got != http.StatusConflict {
				t.Fatalf("stale update = %d", got)
			}
			disable := url.Values{"expected_version": {"2"}, "enabled": {"no"}, "confirm": {"yes"}, "csrf_token": {admin.csrf}}
			if got := performAccessMutation(t, handler, config, base+"/providers/managed/selection", disable, admin.access, admin.csrf, false).Code; got != http.StatusSeeOther {
				t.Fatalf("disable = %d", got)
			}
			remove := url.Values{"expected_version": {"3"}, "confirm": {"yes"}, "csrf_token": {admin.csrf}}
			if got := performAccessMutation(t, handler, config, base+"/providers/managed/delete", remove, admin.access, admin.csrf, false).Code; got != http.StatusSeeOther {
				t.Fatalf("delete = %d", got)
			}
			deleted := get("/providers/managed", admin.access, false)
			if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), "Deleted provider") || strings.Contains(deleted.Body.String(), "managed-secret") {
				t.Fatalf("deleted detail status=%d", deleted.Code)
			}
		})
	}
}

func TestProviderProtectedEditDistinguishesDeleteFromExplicitEmpty(t *testing.T) {
	current := &webui.ProviderEditor{EnvironmentNames: []string{"OLD", "EMPTY"}}
	edit, err := providerProtectedEdit(url.Values{
		"api_key_action":     {"keep"},
		"environment_action": {"replace"},
		"environment":        {"EMPTY=\nNEW=value"},
	}, current)
	if err != nil {
		t.Fatal(err)
	}
	if edit.Environment["OLD"] != nil {
		t.Fatal("replace did not delete omitted environment name")
	}
	if edit.Environment["EMPTY"] == nil || *edit.Environment["EMPTY"] != "" {
		t.Fatal("replace lost explicit empty environment value")
	}
	if edit.Environment["NEW"] == nil || *edit.Environment["NEW"] != "value" {
		t.Fatal("replace lost new environment value")
	}
}
