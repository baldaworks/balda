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
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

type webhooksHTTPFixture struct {
	items      map[string]webhookroutecmd.Item
	creates    []webhookroutecmd.Create
	updates    []webhookroutecmd.Update
	selections []webhookroutecmd.ChangeSelection
	deletes    []webhookroutecmd.Delete
	rotates    []webhookroutecmd.Rotate
	updateErr  error
}

func (f *webhooksHTTPFixture) Inventory(context.Context, webhookroutecmd.Authority) ([]webhookroutecmd.Item, error) {
	var items []webhookroutecmd.Item
	for _, item := range f.items {
		if !item.Deleted {
			items = append(items, item)
		}
	}
	return items, nil
}

func (f *webhooksHTTPFixture) Get(_ context.Context, name string, _ webhookroutecmd.Authority) (webhookroutecmd.Item, error) {
	item, ok := f.items[name]
	if !ok {
		return webhookroutecmd.Item{}, webhookroutecmd.ErrNotFound
	}
	return item, nil
}

func (f *webhooksHTTPFixture) Create(_ context.Context, request webhookroutecmd.Create) (webhookroutecmd.SecretResult, error) {
	f.creates = append(f.creates, request)
	if _, exists := f.items[request.Definition.Name]; exists {
		return webhookroutecmd.SecretResult{}, webhookroutecmd.ErrConflict
	}
	item := webhookroutecmd.Item{Definition: request.Definition, Source: webhookroutecmd.SourceManaged,
		Enabled: true, Version: 1}
	f.items[item.Definition.Name] = item
	return webhookroutecmd.SecretResult{Item: item, Secret: "created-secret-example"}, nil
}

func (f *webhooksHTTPFixture) Update(_ context.Context, request webhookroutecmd.Update) (webhookroutecmd.Item, error) {
	f.updates = append(f.updates, request)
	if f.updateErr != nil {
		return webhookroutecmd.Item{}, f.updateErr
	}
	item := f.items[request.Name]
	item.Definition = request.Definition
	item.Version++
	f.items[request.Name] = item
	return item, nil
}

func (f *webhooksHTTPFixture) SetEnabled(_ context.Context, request webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error) {
	f.selections = append(f.selections, request)
	item := f.items[request.Name]
	item.Enabled = request.Enabled
	item.Version++
	f.items[request.Name] = item
	return item, nil
}

func (f *webhooksHTTPFixture) Delete(_ context.Context, request webhookroutecmd.Delete) (webhookroutecmd.Item, error) {
	f.deletes = append(f.deletes, request)
	item := f.items[request.Name]
	item.Deleted, item.Enabled = true, false
	f.items[request.Name] = item
	return item, nil
}

func (f *webhooksHTTPFixture) Rotate(_ context.Context, request webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error) {
	f.rotates = append(f.rotates, request)
	item := f.items[request.Name]
	item.Version++
	f.items[request.Name] = item
	return webhookroutecmd.SecretResult{Item: item, Secret: "rotated-secret-example"}, nil
}

func TestWebhooksBrowserManagement(t *testing.T) {
	for _, base := range []string{"", "/balda"} {
		t.Run(base, func(t *testing.T) {
			provider, config := newHTTPAppTestState(t)
			config.Server.BasePath = base
			now := time.Now().UTC()
			for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
				createAccessTestUser(t, provider.Users(), usercmd.User{ID: string(role), Username: string(role),
					NormalizedUsername: string(role), DisplayName: string(role), Role: role,
					Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator,
					Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
					Version:    1, CreatedAt: now, UpdatedAt: now})
			}
			app, err := newHTTPApp(provider.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			fixture := &webhooksHTTPFixture{items: map[string]webhookroutecmd.Item{
				"configured": {Definition: webhookroutecmd.Definition{Name: "configured", Path: "/hooks/configured",
					PromptTemplate: "config <script>ignored</script>", ReportTo: "telegram:123456:0"},
					Source: webhookroutecmd.SourceConfig, Enabled: true, Version: 1},
				"managed": {Definition: webhookroutecmd.Definition{Name: "managed", Path: "/hooks/managed",
					PromptTemplate: "handle {{.Body}}", ReportTo: "main_chat"},
					Source: webhookroutecmd.SourceManaged, Enabled: true, Version: 2},
			}}
			app.webhooks = fixture
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "administrator")
			operator := loginHTTPAppSession(t, handler, config, "operator")
			get := func(path, access string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(http.MethodGet, base+path, nil)
				if access != "" {
					r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for _, path := range []string{"/webhooks", "/webhooks?new=1", "/webhooks/managed"} {
				if got := get(path, "").Code; got != http.StatusUnauthorized {
					t.Fatalf("anonymous %s = %d", path, got)
				}
				if got := get(path, operator.access).Code; got != http.StatusForbidden {
					t.Fatalf("operator %s = %d", path, got)
				}
			}
			inventory := get("/webhooks", admin.access)
			if inventory.Code != http.StatusOK || !strings.Contains(inventory.Body.String(), "Report to</th>") ||
				!strings.Contains(inventory.Body.String(), "main_chat") ||
				strings.Contains(inventory.Body.String(), "config &lt;script&gt;") {
				t.Fatalf("inventory leaked content or omitted report recipient: %d", inventory.Code)
			}
			configured := get("/webhooks/configured", admin.access)
			if configured.Code != http.StatusOK || !strings.Contains(configured.Body.String(), "Configuration webhook") ||
				strings.Contains(configured.Body.String(), `name="expected_version"`) ||
				strings.Contains(configured.Body.String(), "<script>ignored</script>") {
				t.Fatalf("configuration route was editable or unescaped: %d", configured.Code)
			}
			managed := get("/webhooks/managed", admin.access)
			if managed.Code != http.StatusOK || !strings.Contains(managed.Body.String(), `name="report_to" value="main_chat"`) ||
				strings.Contains(managed.Body.String(), `name="destination"`) ||
				!strings.Contains(managed.Body.String(), `name="expected_version"`) {
				t.Fatalf("managed form does not match route fields: %d", managed.Code)
			}
			mutation := func(path string, form url.Values) *httptest.ResponseRecorder {
				t.Helper()
				form.Set("csrf_token", admin.csrf)
				return performAccessMutation(t, handler, config, base+path, form, admin.access, admin.csrf, false)
			}
			if got := mutation("/webhooks/configured/delete", url.Values{"expected_version": {"1"}, "confirm": {"yes"}}).Code; got != http.StatusForbidden || len(fixture.deletes) != 0 {
				t.Fatalf("configuration deletion = %d", got)
			}
			form := url.Values{"name": {"new_route"}, "path": {"/hooks/new"}, "report_to": {"telegram:123456:0"},
				"prompt_template": {"handle {{.Body}}"}, "dedupe_source": {"request_id"}}
			if got := performAccessMutation(t, handler, config, base+"/webhooks", form, admin.access, "invalid", false).Code; got != http.StatusForbidden || len(fixture.creates) != 0 {
				t.Fatalf("invalid CSRF reached policy: %d", got)
			}
			created := mutation("/webhooks", form)
			if created.Code != http.StatusOK || created.Header().Get("Cache-Control") != "no-store" ||
				!strings.Contains(created.Body.String(), "created-secret-example") ||
				!strings.Contains(created.Body.String(), `data-webhook-once="`+base+`/webhooks/new_route"`) ||
				len(fixture.creates) != 1 || fixture.creates[0].Authority.SessionID == "" {
				t.Fatalf("create did not return one-time secret safely: %d", created.Code)
			}
			if got := get("/webhooks/new_route", admin.access); got.Code != http.StatusOK || strings.Contains(got.Body.String(), "created-secret-example") {
				t.Fatalf("secret persisted in GET: %d", got.Code)
			}
			if got := mutation("/webhooks", form); got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "created-secret-example") {
				t.Fatalf("repeat create exposed secret or hid conflict: %d", got.Code)
			}
			update := url.Values{"expected_version": {"2"}, "path": {"/hooks/managed-v2"},
				"report_to": {"telegram:123456:0"}, "prompt_template": {"updated"}, "dedupe_source": {"body_sha256"}}
			if got := mutation("/webhooks/managed", update); got.Code != http.StatusSeeOther ||
				fixture.updates[0].ExpectedVersion != 2 || fixture.updates[0].Definition.ReportTo != "telegram:123456:0" {
				t.Fatalf("update failed: %d", got.Code)
			}
			fixture.updateErr = webhookroutecmd.ErrConflict
			if got := mutation("/webhooks/managed", update); got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "/hooks/managed-v2") == false {
				t.Fatalf("conflicting update = %d", got.Code)
			}
			if got := mutation("/webhooks/managed/selection", url.Values{"expected_version": {"3"}}).Code; got != http.StatusBadRequest || len(fixture.selections) != 0 {
				t.Fatalf("unconfirmed selection = %d", got)
			}
			if got := mutation("/webhooks/managed/selection", url.Values{"expected_version": {"3"}, "confirm": {"yes"}, "enabled": {"no"}}).Code; got != http.StatusSeeOther || len(fixture.selections) != 1 {
				t.Fatalf("confirmed selection = %d", got)
			}
			rotated := mutation("/webhooks/managed/rotate", url.Values{"expected_version": {"4"}, "confirm": {"yes"}})
			if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), "rotated-secret-example") ||
				strings.Contains(get("/webhooks/managed", admin.access).Body.String(), "rotated-secret-example") {
				t.Fatalf("secret rotation exposed later: %d", rotated.Code)
			}
			if got := mutation("/webhooks/managed/delete", url.Values{"expected_version": {"5"}, "confirm": {"yes"}}).Code; got != http.StatusSeeOther || len(fixture.deletes) != 1 {
				t.Fatalf("confirmed delete = %d", got)
			}
		})
	}
}
