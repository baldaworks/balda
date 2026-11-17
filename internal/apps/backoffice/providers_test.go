package backoffice

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
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
			protector, err := newProviderProtector(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
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
			picker := get("/providers?new=1", admin.access, false)
			if picker.Code != http.StatusOK || !strings.Contains(picker.Body.String(), "Choose provider type") || strings.Contains(picker.Body.String(), `name="api_key"`) || strings.Contains(picker.Body.String(), `name="environment"`) {
				t.Fatalf("provider type picker status=%d body=%s", picker.Code, picker.Body.String())
			}
			openAIEditor := get("/providers?new=1&type="+agentconfig.AgentTypeOpenAI, admin.access, false)
			if openAIEditor.Code != http.StatusOK || !strings.Contains(openAIEditor.Body.String(), `name="api_key"`) || !strings.Contains(openAIEditor.Body.String(), `name="timeout"`) || strings.Contains(openAIEditor.Body.String(), `name="environment"`) || strings.Contains(openAIEditor.Body.String(), `name="pool_members"`) {
				t.Fatalf("OpenAI editor status=%d body=%s", openAIEditor.Code, openAIEditor.Body.String())
			}
			configured := get("/providers/yaml", admin.access, false)
			if configured.Code != http.StatusOK || strings.Contains(configured.Body.String(), "configured-secret") || !strings.Contains(configured.Body.String(), "YAML_SECRET") || !strings.Contains(configured.Body.String(), "Read-only") {
				t.Fatalf("configured detail status=%d body=%s", configured.Code, configured.Body.String())
			}
			create := url.Values{"expected_version": {"0"}, "id": {"managed"}, "type": {agentconfig.AgentTypeCodexACP}, "model": {"gpt-test"}, "inherit_env": {"no"}, "environment_action": {"replace"}, "environment": {"TOKEN=managed-secret"}}
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
			update := url.Values{"expected_version": {"1"}, "type": {agentconfig.AgentTypeCodexACP}, "model": {"gpt-updated"}, "inherit_env": {"no"}, "environment_action": {"keep"}, "csrf_token": {admin.csrf}}
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
			deletedInventory := get("/providers?lifecycle=deleted", admin.access, true)
			if deletedInventory.Code != http.StatusOK || !strings.Contains(deletedInventory.Body.String(), `aria-current="page">Deleted`) ||
				!strings.Contains(deletedInventory.Body.String(), `/providers/managed?lifecycle=deleted`) ||
				strings.Contains(deletedInventory.Body.String(), ">yaml</a>") || strings.Contains(deletedInventory.Body.String(), "<!doctype html>") {
				t.Fatalf("deleted provider inventory status=%d body=%s", deletedInventory.Code, deletedInventory.Body.String())
			}
			deleted = get("/providers/managed?lifecycle=deleted", admin.access, false)
			if !strings.Contains(deleted.Body.String(), `/providers?lifecycle=deleted`) || !strings.Contains(deleted.Body.String(), "Source</dt><dd>managed") {
				t.Fatal("deleted provider detail lost lifecycle or source")
			}
			for _, action := range []string{"Save provider", "Enable provider", "Disable provider", "Delete provider", `name="api_key"`} {
				if strings.Contains(deleted.Body.String(), action) {
					t.Errorf("deleted provider exposes %q", action)
				}
			}
			invalid := url.Values{"expected_version": {"4"}, "id": {"invalid-aistudio"}, "type": {agentconfig.AgentTypeAIStudio}, "model": {"safe-model"}, "timeout": {"2m"}, "api_key_action": {"replace"}, "api_key": {"must-not-render"}, "csrf_token": {admin.csrf}}
			invalidResponse := performAccessMutation(t, handler, config, base+"/providers", invalid, admin.access, admin.csrf, false)
			if invalidResponse.Code != http.StatusBadRequest || !strings.Contains(invalidResponse.Body.String(), `value="invalid-aistudio"`) || !strings.Contains(invalidResponse.Body.String(), `value="safe-model"`) || strings.Contains(invalidResponse.Body.String(), "must-not-render") || strings.Contains(invalidResponse.Body.String(), `name="timeout"`) {
				t.Fatalf("invalid typed draft status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
			}
			managedTypes := []struct {
				id       string
				typeName string
				values   url.Values
			}{
				{id: "managed-openai", typeName: agentconfig.AgentTypeOpenAI, values: url.Values{"model": {"gpt-synthetic"}, "timeout": {"30s"}, "api_key_action": {"replace"}, "api_key": {"synthetic-hosted-secret"}}},
				{id: "managed-aistudio", typeName: agentconfig.AgentTypeAIStudio, values: url.Values{"model": {"gemini-synthetic"}, "api_key_action": {"replace"}, "api_key": {"synthetic-studio-secret"}}},
				{id: "managed-pool", typeName: agentconfig.AgentTypePool, values: url.Values{"pool_members": {"yaml"}}},
				{id: "managed-registry", typeName: agentconfig.AgentTypeRegistryACP, values: url.Values{"registry_id": {"synthetic-agent"}, "inherit_env": {"no"}, "environment_action": {"keep"}}},
				{id: "managed-codex-2", typeName: agentconfig.AgentTypeCodexACP, values: url.Values{"model": {"gpt-synthetic"}, "inherit_env": {"no"}, "environment_action": {"keep"}}},
				{id: "managed-opencode", typeName: agentconfig.AgentTypeOpenCodeACP, values: url.Values{"inherit_env": {"no"}, "environment_action": {"keep"}}},
				{id: "managed-copilot", typeName: agentconfig.AgentTypeCopilotACP, values: url.Values{"inherit_env": {"no"}, "environment_action": {"keep"}}},
				{id: "managed-claude", typeName: agentconfig.AgentTypeClaudeCodeACP, values: url.Values{"inherit_env": {"no"}, "environment_action": {"keep"}}},
				{id: "managed-grok", typeName: agentconfig.AgentTypeGrokACP, values: url.Values{"inherit_env": {"no"}, "environment_action": {"keep"}}},
				{id: "managed-agy", typeName: agentconfig.AgentTypeAgyACP, values: url.Values{"inherit_env": {"no"}, "environment_action": {"keep"}}},
			}
			for index, managedType := range managedTypes {
				managedType.values.Set("expected_version", strconv.Itoa(4+index))
				managedType.values.Set("id", managedType.id)
				managedType.values.Set("type", managedType.typeName)
				managedType.values.Set("csrf_token", admin.csrf)
				response := performAccessMutation(t, handler, config, base+"/providers", managedType.values, admin.access, admin.csrf, false)
				if response.Code != http.StatusSeeOther {
					t.Fatalf("create %s = %d body=%s", managedType.typeName, response.Code, response.Body.String())
				}
				detail := get("/providers/"+managedType.id, admin.access, false)
				if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "synthetic-hosted-secret") || strings.Contains(detail.Body.String(), "synthetic-studio-secret") {
					t.Fatalf("detail %s status=%d body=%s", managedType.typeName, detail.Code, detail.Body.String())
				}
				detailBody := detail.Body.String()
				descriptor, _ := agentconfig.BuiltinDriverRegistry().Lookup(managedType.typeName)
				if got := strings.Contains(detailBody, `name="api_key"`); got != (descriptor.Class == agentconfig.DriverClassHosted) {
					t.Fatalf("edit %s API key applicability = %t", managedType.typeName, got)
				}
				if got := strings.Contains(detailBody, `name="environment"`); got != (descriptor.Class == agentconfig.DriverClassACP) {
					t.Fatalf("edit %s environment applicability = %t", managedType.typeName, got)
				}
				invalidUpdate := url.Values{"expected_version": {strconv.Itoa(5 + index)}, "type": {managedType.typeName}, "csrf_token": {admin.csrf}}
				switch descriptor.Class {
				case agentconfig.DriverClassHosted:
					invalidUpdate.Set("model", "still-valid")
					invalidUpdate.Set("api_key_action", "keep")
					if managedType.typeName == agentconfig.AgentTypeOpenAI {
						invalidUpdate.Set("registry_id", "forbidden")
					} else {
						invalidUpdate.Set("timeout", "2m")
					}
				case agentconfig.DriverClassPool:
					invalidUpdate.Set("pool_members", "yaml")
					invalidUpdate.Set("model", "forbidden")
				case agentconfig.DriverClassACP:
					invalidUpdate.Set("inherit_env", "no")
					invalidUpdate.Set("environment_action", "keep")
					invalidUpdate.Set("api_key_action", "keep")
				}
				invalidEdit := performAccessMutation(t, handler, config, base+"/providers/"+managedType.id, invalidUpdate, admin.access, admin.csrf, false)
				if invalidEdit.Code != http.StatusBadRequest {
					t.Fatalf("invalid edit %s = %d body=%s", managedType.typeName, invalidEdit.Code, invalidEdit.Body.String())
				}
			}
			all := get("/providers?lifecycle=all", admin.access, false)
			if all.Code != http.StatusOK || !strings.Contains(all.Body.String(), "managed") || !strings.Contains(all.Body.String(), "yaml") || !strings.Contains(all.Body.String(), `aria-current="page">All`) {
				t.Fatalf("all provider inventory status=%d", all.Code)
			}
			if got := get("/providers?lifecycle=unknown", admin.access, false).Code; got != http.StatusBadRequest {
				t.Fatalf("invalid provider lifecycle = %d", got)
			}
		})
	}
}

func TestProviderProtectedEditDistinguishesDeleteFromExplicitEmpty(t *testing.T) {
	current := &webui.ProviderEditor{EnvironmentNames: []string{"OLD", "EMPTY"}}
	edit, err := providerProtectedEdit(url.Values{
		"environment_action": {"replace"},
		"environment":        {"EMPTY=\nNEW=value"},
	}, current, agentconfig.AgentTypeCodexACP)
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

func TestProviderTypeSpecificFormsRejectInapplicableFields(t *testing.T) {
	renderer, err := webui.NewQARenderer("")
	if err != nil {
		t.Fatal(err)
	}
	allSpecificFields := []string{
		"model", "reasoning_effort", "mode", "timeout", "bridge_version", "registry_id",
		"model_config_id", "reasoning_effort_config_id", "extra_args", "inherit_env",
		"pool_members", "api_key_action", "api_key", "environment_action", "environment",
	}
	for _, descriptor := range agentconfig.BuiltinDriverRegistry().Descriptors() {
		if descriptor.Type == agentconfig.AgentTypeGenericACP || descriptor.Deprecated {
			continue
		}
		t.Run("render_"+descriptor.Type, func(t *testing.T) {
			page := qaProviderCreateEditor(descriptor.Type)
			request := httptest.NewRequest(http.MethodGet, "/qa/ui/providers/new/"+descriptor.Type, nil)
			response := httptest.NewRecorder()
			if err := renderer.Render(response, request, http.StatusOK, webui.TemplateProviders, page); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			want := map[string]bool{"model": descriptor.Class != agentconfig.DriverClassPool}
			for _, field := range []string{"reasoning_effort", "mode", "model_config_id", "reasoning_effort_config_id", "extra_args", "inherit_env", "environment_action", "environment"} {
				want[field] = descriptor.Class == agentconfig.DriverClassACP
			}
			want["reasoning_effort"] = descriptor.Class == agentconfig.DriverClassACP || descriptor.Type == agentconfig.AgentTypeOpenAI
			want["timeout"] = descriptor.Type == agentconfig.AgentTypeOpenAI
			want["bridge_version"] = descriptor.Type == agentconfig.AgentTypeCodexACP || descriptor.Type == agentconfig.AgentTypeRegistryACP || descriptor.Type == agentconfig.AgentTypeAgyACP
			want["registry_id"] = descriptor.Type == agentconfig.AgentTypeRegistryACP
			want["pool_members"] = descriptor.Class == agentconfig.DriverClassPool
			want["api_key_action"] = descriptor.Class == agentconfig.DriverClassHosted
			want["api_key"] = descriptor.Class == agentconfig.DriverClassHosted
			for _, field := range allSpecificFields {
				present := strings.Contains(body, `name="`+field+`"`)
				if present != want[field] {
					t.Errorf("field %q present = %t, want %t", field, present, want[field])
				}
			}
			for _, common := range []string{"id", "type", "mcp_servers", "system_instructions"} {
				if !strings.Contains(body, `name="`+common+`"`) {
					t.Errorf("missing common field %q", common)
				}
			}
			if descriptor.Class == agentconfig.DriverClassHosted && !strings.Contains(body, `name="model" value="" maxlength="512" required`) {
				t.Error("hosted model is not required")
			}
			if descriptor.Type == agentconfig.AgentTypeOpenAI && !strings.Contains(body, `name="api_key" value="" maxlength="16384" autocomplete="new-password" required`) {
				t.Error("new OpenAI API key is not required")
			}
		})
	}
	for _, hasAPIKey := range []bool{false, true} {
		t.Run("render_OpenAI_edit_key_"+strconv.FormatBool(hasAPIKey), func(t *testing.T) {
			item := providers.Item{Source: providers.SourceManaged,
				Record: providers.Record{ID: "openai", Enabled: true},
				Revision: providers.Revision{DriverType: agentconfig.AgentTypeOpenAI, Definition: providers.Definition{
					Config: agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: "gpt"}}, HasAPIKey: hasAPIKey,
				}}}
			page := qaProviders()
			page.Providers.Editor = projectProviderEditor(item, 3, false)
			response := httptest.NewRecorder()
			if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/qa/ui/providers/openai", nil), http.StatusOK, webui.TemplateProviders, page); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if strings.Contains(body, `value="clear">Clear value`) {
				t.Error("OpenAI edit offers an unusable clear action")
			}
			if hasAPIKey {
				if !strings.Contains(body, `aria-label="API key action"`) || strings.Contains(body, `name="api_key" value="" maxlength="16384" autocomplete="new-password" required`) {
					t.Error("OpenAI edit with retained key has wrong keep/required controls")
				}
			} else if strings.Contains(body, `aria-label="API key action"`) || !strings.Contains(body, `name="api_key_action" value="replace"`) || !strings.Contains(body, `name="api_key" value="" maxlength="16384" autocomplete="new-password" required`) {
				t.Error("OpenAI edit without retained key must require replacement")
			}
		})
	}

	for _, test := range []struct {
		name string
		form url.Values
	}{
		{name: "AI Studio timeout", form: url.Values{"type": {agentconfig.AgentTypeAIStudio}, "timeout": {"2m"}}},
		{name: "pool model", form: url.Values{"type": {agentconfig.AgentTypePool}, "pool_members": {"one"}, "model": {"ignored"}}},
		{name: "OpenCode bridge", form: url.Values{"type": {agentconfig.AgentTypeOpenCodeACP}, "bridge_version": {"latest"}}},
		{name: "Codex registry ID", form: url.Values{"type": {agentconfig.AgentTypeCodexACP}, "registry_id": {"ignored"}}},
		{name: "AI Studio missing model", form: url.Values{"type": {agentconfig.AgentTypeAIStudio}}},
		{name: "OpenAI missing model", form: url.Values{"type": {agentconfig.AgentTypeOpenAI}}},
	} {
		t.Run("reject_"+test.name, func(t *testing.T) {
			if _, err := providerConfig(test.form); err == nil {
				t.Fatal("inapplicable field accepted")
			}
		})
	}
	for _, test := range []struct {
		name     string
		typeName string
		form     url.Values
	}{
		{name: "ACP API key", typeName: agentconfig.AgentTypeCodexACP, form: url.Values{"api_key_action": {"replace"}, "api_key": {"secret"}}},
		{name: "ACP API keep action", typeName: agentconfig.AgentTypeCodexACP, form: url.Values{"api_key_action": {"keep"}}},
		{name: "hosted environment", typeName: agentconfig.AgentTypeOpenAI, form: url.Values{"environment_action": {"replace"}, "environment": {"TOKEN=secret"}}},
		{name: "hosted environment keep action", typeName: agentconfig.AgentTypeOpenAI, form: url.Values{"environment_action": {"keep"}}},
		{name: "pool API key", typeName: agentconfig.AgentTypePool, form: url.Values{"api_key_action": {"replace"}, "api_key": {"secret"}}},
		{name: "hosted API keep with replacement", typeName: agentconfig.AgentTypeAIStudio, form: url.Values{"api_key_action": {"keep"}, "api_key": {"secret"}}},
		{name: "hosted API clear with replacement", typeName: agentconfig.AgentTypeAIStudio, form: url.Values{"api_key_action": {"clear"}, "api_key": {"secret"}}},
		{name: "ACP environment keep with replacement", typeName: agentconfig.AgentTypeCodexACP, form: url.Values{"environment_action": {"keep"}, "environment": {"TOKEN=secret"}}},
		{name: "ACP environment clear with replacement", typeName: agentconfig.AgentTypeCodexACP, form: url.Values{"environment_action": {"clear"}, "environment": {"TOKEN=secret"}}},
	} {
		t.Run("reject_"+test.name, func(t *testing.T) {
			if _, err := providerProtectedEdit(test.form, nil, test.typeName); err == nil {
				t.Fatal("inapplicable protected field accepted")
			}
		})
	}
	if _, err := providerChange(url.Values{
		"expected_version": {"0"}, "id": {"openai"}, "type": {agentconfig.AgentTypeOpenAI},
		"model": {"gpt"}, "api_key_action": {"replace"}, "api_key": {""},
	}, nil, usercmd.AuditEvent{}); err == nil {
		t.Fatal("new OpenAI provider without API key accepted")
	}
	current := &webui.ProviderEditor{Type: agentconfig.AgentTypeOpenAI, Row: webui.ProviderRow{ID: "openai"}, HasAPIKey: true}
	if _, err := providerChange(url.Values{
		"expected_version": {"1"}, "type": {agentconfig.AgentTypeOpenAI}, "model": {"gpt"}, "api_key_action": {"keep"},
	}, current, usercmd.AuditEvent{}); err != nil {
		t.Fatalf("OpenAI edit with retained API key: %v", err)
	}
}
