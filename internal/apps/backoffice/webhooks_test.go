package backoffice

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutefx"
)

type webhooksHTTPFixture struct {
	items              map[string]webhookroutecmd.Item
	creates            []webhookroutecmd.Create
	updates            []webhookroutecmd.Update
	selections         []webhookroutecmd.ChangeSelection
	deletes            []webhookroutecmd.Delete
	rotates            []webhookroutecmd.Rotate
	updateErr          error
	tests              []webhookroutecmd.TestPost
	history            []webhookroutecmd.HistoryItem
	historyLimit       int
	historyBeforeAt    time.Time
	historyBeforeJobID string
	testErr            error
}

const testWebhookPublicOrigin = "https://lab.metalagman.dev"
const testManagedCallbackPath = "/balda/webhooks/orders"

func TestWebhooksCreateWithRealManagedRoutePolicy(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "standalone"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			provider, config := newHTTPAppTestState(t)
			config.Server.BasePath = testBackofficeBasePath
			manager := webhookmanagement.New(webhookroutefx.NewStore(provider))
			if shared {
				config.Server.BasePath += "/backoffice"
				sharedPath := testBackofficeBasePath
				config.Server.WebhookURLBasePath = &sharedPath
				manager = webhookmanagement.New(webhookroutefx.NewStore(provider),
					webhookmanagement.ManagedPaths{Prefix: "/balda/webhooks"})
			}
			now := time.Now().UTC()
			createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin",
				NormalizedUsername: "admin", DisplayName: "admin", Role: usercmd.RoleAdministrator,
				Status: usercmd.StatusActive, Primary: true,
				Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
				Version:    1, CreatedAt: now, UpdatedAt: now})
			app, err := newHTTPApp(provider.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			app.webhooks = webhookbackofficeapp.New(manager, nil, nil)
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "admin")
			form := url.Values{"name": {"orders"}, "prompt_template": {"Handle {{.RawBody}}"},
				"dedupe_source": {"request_id"}, "csrf_token": {admin.csrf}}
			response := performAccessMutation(t, handler, config, config.Server.BasePath+"/webhooks",
				form, admin.access, admin.csrf, false)
			if response.Code != http.StatusOK {
				t.Fatalf("create without editable path = %d", response.Code)
			}
			stored, found, err := provider.WebhookRoutes().Get(t.Context(), "orders")
			if err != nil || !found || stored.Path != testManagedCallbackPath {
				t.Fatalf("stored route = %+v, found %v, error %v", stored, found, err)
			}
			update := url.Values{"expected_version": {"1"}, "prompt_template": {"Updated {{.RawBody}}"},
				"dedupe_source": {"request_id"}, "csrf_token": {admin.csrf}}
			response = performAccessMutation(t, handler, config, config.Server.BasePath+"/webhooks/orders",
				update, admin.access, admin.csrf, false)
			if response.Code != http.StatusSeeOther {
				t.Fatalf("update without editable path = %d", response.Code)
			}
			stored, found, err = provider.WebhookRoutes().Get(t.Context(), "orders")
			if err != nil || !found || stored.Path != testManagedCallbackPath || stored.PromptTemplate != "Updated {{.RawBody}}" {
				t.Fatalf("updated route = %+v, found %v, error %v", stored, found, err)
			}
			update.Set("expected_version", "2")
			update.Set("path", "/forged")
			response = performAccessMutation(t, handler, config, config.Server.BasePath+"/webhooks/orders",
				update, admin.access, admin.csrf, false)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("forged read-only path update = %d", response.Code)
			}
			stored, found, err = provider.WebhookRoutes().Get(t.Context(), "orders")
			if err != nil || !found || stored.Path != testManagedCallbackPath || stored.Version != 2 {
				t.Fatalf("route changed after forged path: %+v, found %v, error %v", stored, found, err)
			}
			if !shared {
				principal, err := app.security.ValidateAccess(t.Context(), admin.access)
				if err != nil {
					t.Fatal(err)
				}
				_, err = manager.Create(t.Context(), webhookroutecmd.Create{
					Definition: webhookroutecmd.Definition{Name: "legacy", Path: "/old/events",
						PromptTemplate: "Legacy {{.RawBody}}", DedupeSource: webhookroutecmd.DedupeSourceRequestID},
					Authority: app.webhookAuthority(principal),
				})
				if err != nil {
					t.Fatal(err)
				}
				legacyUpdate := url.Values{"expected_version": {"1"}, "prompt_template": {"Retained {{.RawBody}}"},
					"dedupe_source": {"request_id"}, "csrf_token": {admin.csrf}}
				response = performAccessMutation(t, handler, config, config.Server.BasePath+"/webhooks/legacy",
					legacyUpdate, admin.access, admin.csrf, false)
				if response.Code != http.StatusSeeOther {
					t.Fatalf("legacy custom-path update = %d", response.Code)
				}
				legacy, found, err := provider.WebhookRoutes().Get(t.Context(), "legacy")
				if err != nil || !found || legacy.Path != "/old/events" || legacy.PromptTemplate != "Retained {{.RawBody}}" {
					t.Fatalf("legacy route after edit = %+v, found %v, error %v", legacy, found, err)
				}
			}
		})
	}
}

func TestWebhooksShowPublicCallbackURL(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	config.Server.PublicURL = testWebhookPublicOrigin
	config.Server.BasePath = "/balda/backoffice"
	config.Server.SecureCookies = true
	sharedPath := testBackofficeBasePath
	config.Server.WebhookURLBasePath = &sharedPath
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin",
		NormalizedUsername: "admin", DisplayName: "admin", Role: usercmd.RoleAdministrator,
		Status: usercmd.StatusActive, Primary: true,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Version:    1, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	app.webhooks = &webhooksHTTPFixture{items: map[string]webhookroutecmd.Item{
		"orders": {Definition: webhookroutecmd.Definition{Name: "orders", Path: "/balda/webhooks/orders"},
			Source: webhookroutecmd.SourceManaged, Enabled: true, Version: 1},
		"legacy": {Definition: webhookroutecmd.Definition{Name: "legacy", Path: "/old/events"},
			Source: webhookroutecmd.SourceManaged, Enabled: true, Version: 1},
	}}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, "admin")
	get := func(path string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, config.Server.BasePath+path, nil)
		request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, response.Code)
		}
		return response.Body.String()
	}
	create := get("/webhooks?new=1")
	if !strings.Contains(create, `data-webhook-url-prefix="https://lab.metalagman.dev/balda/webhooks/"`) ||
		!strings.Contains(create, `id="webhook-url"`) || strings.Contains(create, `name="path"`) {
		t.Fatal("create form must show a derived, read-only callback URL")
	}
	inventory := get("/webhooks")
	if !strings.Contains(inventory, "Webhook URL</th>") ||
		!strings.Contains(inventory, "https://lab.metalagman.dev/balda/webhooks/orders") ||
		!strings.Contains(inventory, "https://lab.metalagman.dev/old/events") {
		t.Fatal("inventory must show exact public callback URLs")
	}
	legacy := get("/webhooks/legacy")
	if !strings.Contains(legacy, "https://lab.metalagman.dev/old/events") || strings.Contains(legacy, `name="path"`) {
		t.Fatal("legacy detail must show its stored path without editing it")
	}
}

func TestWebhooksTestPostAndHistory(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin",
		NormalizedUsername: "admin", DisplayName: "admin", Role: usercmd.RoleAdministrator,
		Status: usercmd.StatusActive, Primary: true,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Version:    1, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &webhooksHTTPFixture{items: map[string]webhookroutecmd.Item{
		"configured": {Definition: webhookroutecmd.Definition{Name: "configured", Path: "/hooks/configured"},
			Source: webhookroutecmd.SourceConfig, Enabled: true, Version: 1},
		"disabled": {Definition: webhookroutecmd.Definition{Name: "disabled", Path: "/hooks/disabled"},
			Source: webhookroutecmd.SourceManaged, Version: 2},
		"archived": {Definition: webhookroutecmd.Definition{Name: "archived", Path: "/hooks/archived"},
			Source: webhookroutecmd.SourceManaged, Deleted: true, Version: 3},
	}}
	for i := 0; i < 21; i++ {
		fixture.history = append(fixture.history, webhookroutecmd.HistoryItem{
			JobID: fmt.Sprintf("job-%02d", i), Source: "external", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			Input: "<unsafe>", InputAvailable: true, Output: "<output>", JobStatus: "succeeded",
		})
	}
	app.webhooks = fixture
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, "admin")
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	post := func(path string, form url.Values, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf_token", csrf)
		return performAccessMutation(t, handler, config, path, form, admin.access, csrf, false)
	}
	page := get("/webhooks/configured")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Send test POST") ||
		!strings.Contains(page.Body.String(), "Older requests") || fixture.historyLimit != 21 ||
		strings.Contains(page.Body.String(), "job-20") {
		t.Fatalf("configured history page = %d, limit = %d", page.Code, fixture.historyLimit)
	}
	if got := get("/webhooks/configured?before_at=bad&before_job_id=job-01").Code; got != http.StatusBadRequest {
		t.Fatalf("invalid history cursor = %d", got)
	}
	detail := get("/webhooks/configured?job_id=job-00")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "&lt;unsafe&gt;") ||
		!strings.Contains(detail.Body.String(), "&lt;output&gt;") || strings.Contains(detail.Body.String(), "<unsafe>") {
		t.Fatalf("history detail unsafe or missing: %d", detail.Code)
	}
	fixture.history[0].InputAvailable = false
	if got := get("/webhooks/configured?job_id=job-00"); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), "Input unavailable for this older request.") {
		t.Fatalf("pre-upgrade input = %d", got.Code)
	}
	key := uuid.NewString()
	form := url.Values{"request_key": {key}, "expected_version": {"1"}, "body": {"test body"}}
	if got := post("/webhooks/configured/test", form, "invalid").Code; got != http.StatusForbidden || len(fixture.tests) != 0 {
		t.Fatalf("invalid CSRF reached Test POST: %d", got)
	}
	if got := post("/webhooks/configured/test", form, admin.csrf); got.Code != http.StatusSeeOther ||
		len(fixture.tests) != 1 || fixture.tests[0].Body != "test body" || fixture.tests[0].RequestKey != key ||
		fixture.tests[0].Authority.SessionID == "" {
		t.Fatalf("configured Test POST = %d", got.Code)
	}
	fixture.testErr = webhookroutecmd.ErrUnavailable
	failed := post("/webhooks/configured/test", form, admin.csrf)
	if failed.Code != http.StatusServiceUnavailable ||
		!strings.Contains(failed.Body.String(), `name="request_key" value="`+key+`"`) ||
		!strings.Contains(failed.Body.String(), `>test body</textarea>`) {
		t.Fatalf("uncertain Test POST response lost retry input: %d", failed.Code)
	}
	fixture.testErr = nil
	if got := post("/webhooks/disabled/test", url.Values{"request_key": {key}, "expected_version": {"2"}}, admin.csrf); got.Code != http.StatusBadRequest || len(fixture.tests) != 2 {
		t.Fatalf("unconfirmed disabled Test POST = %d", got.Code)
	}
	if got := post("/webhooks/disabled/test", url.Values{"request_key": {key}, "expected_version": {"2"}, "confirm_disabled": {"yes"}}, admin.csrf); got.Code != http.StatusSeeOther || len(fixture.tests) != 3 || !fixture.tests[2].ConfirmDisabled {
		t.Fatalf("confirmed disabled Test POST = %d", got.Code)
	}
	if got := post("/webhooks/archived/test", url.Values{"request_key": {key}, "expected_version": {"3"}}, admin.csrf).Code; got != http.StatusNotFound || len(fixture.tests) != 3 {
		t.Fatalf("archived route test = %d", got)
	}
	if got := get("/webhooks/archived"); got.Code != http.StatusOK ||
		strings.Contains(got.Body.String(), "Send test POST") || !strings.Contains(got.Body.String(), "Request history") {
		t.Fatalf("archived route detail = %d", got.Code)
	}
	encodedBody := strings.Repeat("{", 600_000)
	validForm := url.Values{"request_key": {uuid.NewString()}, "expected_version": {"1"}, "body": {encodedBody}}
	if encodedSize := len(validForm.Encode()); encodedSize <= webhookcmd.MaxBodyBytes {
		t.Fatalf("test body did not exercise form encoding growth: %d", encodedSize)
	}
	if got := post("/webhooks/configured/test", validForm, admin.csrf); got.Code != http.StatusSeeOther ||
		len(fixture.tests) != 4 || fixture.tests[3].Body != encodedBody {
		t.Fatalf("valid encoded Test POST body = %d, admissions = %d", got.Code, len(fixture.tests))
	}
	tooLargeBody := strings.Repeat("a", webhookcmd.MaxBodyBytes+1)
	invalidForm := url.Values{"request_key": {uuid.NewString()}, "expected_version": {"1"}, "body": {tooLargeBody}}
	if got := post("/webhooks/configured/test", invalidForm, admin.csrf); got.Code != http.StatusBadRequest ||
		len(fixture.tests) != 4 || !strings.Contains(got.Body.String(), "Test POST body is too large") {
		t.Fatalf("oversized decoded Test POST body = %d, admissions = %d", got.Code, len(fixture.tests))
	}
}

func (f *webhooksHTTPFixture) TestPost(_ context.Context, request webhookroutecmd.TestPost) (webhookroutecmd.TestResult, error) {
	f.tests = append(f.tests, request)
	if f.testErr != nil {
		return webhookroutecmd.TestResult{}, f.testErr
	}
	return webhookroutecmd.TestResult{JobID: "test-job"}, nil
}

func (f *webhooksHTTPFixture) History(_ context.Context, _ string, beforeAt time.Time, beforeJobID string, limit int,
	_ webhookroutecmd.Authority) ([]webhookroutecmd.HistoryItem, error) {
	f.historyLimit, f.historyBeforeAt, f.historyBeforeJobID = limit, beforeAt, beforeJobID
	return f.history, nil
}

func (f *webhooksHTTPFixture) HistoryDetail(_ context.Context, _, jobID string,
	_ webhookroutecmd.Authority) (webhookroutecmd.HistoryItem, error) {
	for _, item := range f.history {
		if item.JobID == jobID {
			return item, nil
		}
	}
	return webhookroutecmd.HistoryItem{}, webhookroutecmd.ErrNotFound
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
				!strings.Contains(inventory.Body.String(), "Webhook URL</th>") ||
				!strings.Contains(inventory.Body.String(), "main_chat") ||
				strings.Contains(inventory.Body.String(), "config &lt;script&gt;") {
				t.Fatalf("inventory leaked content or omitted report recipient: %d", inventory.Code)
			}
			configured := get("/webhooks/configured", admin.access)
			if configured.Code != http.StatusOK || !strings.Contains(configured.Body.String(), "Configuration webhook") ||
				strings.Contains(configured.Body.String(), "Save webhook") ||
				!strings.Contains(configured.Body.String(), "Send test POST") ||
				strings.Contains(configured.Body.String(), "<script>ignored</script>") {
				t.Fatalf("configuration route was editable or unescaped: %d", configured.Code)
			}
			managed := get("/webhooks/managed", admin.access)
			if managed.Code != http.StatusOK || !strings.Contains(managed.Body.String(), `name="report_to" value="main_chat"`) ||
				strings.Contains(managed.Body.String(), `name="destination"`) ||
				!strings.Contains(managed.Body.String(), `name="expected_version"`) {
				t.Fatalf("managed form does not match route fields: %d", managed.Code)
			}
			createPage := get("/webhooks?new=1", admin.access)
			if createPage.Code != http.StatusOK || !strings.Contains(createPage.Body.String(),
				`data-webhook-url-prefix="http://backoffice.example`+base+`/webhooks/"`) {
				t.Fatalf("standalone callback URL fallback = %d", createPage.Code)
			}
			mutation := func(path string, form url.Values) *httptest.ResponseRecorder {
				t.Helper()
				form.Set("csrf_token", admin.csrf)
				return performAccessMutation(t, handler, config, base+path, form, admin.access, admin.csrf, false)
			}
			if got := mutation("/webhooks/configured/delete", url.Values{"expected_version": {"1"}, "confirm": {"yes"}}).Code; got != http.StatusForbidden || len(fixture.deletes) != 0 {
				t.Fatalf("configuration deletion = %d", got)
			}
			form := url.Values{"name": {"new_route"}, "report_to": {"telegram:123456:0"},
				"prompt_template": {"handle {{.Body}}"}, "dedupe_source": {"request_id"}}
			if got := performAccessMutation(t, handler, config, base+"/webhooks", form, admin.access, "invalid", false).Code; got != http.StatusForbidden || len(fixture.creates) != 0 {
				t.Fatalf("invalid CSRF reached policy: %d", got)
			}
			created := mutation("/webhooks", form)
			if created.Code != http.StatusOK || created.Header().Get("Cache-Control") != "no-store" ||
				!strings.Contains(created.Body.String(), "created-secret-example") ||
				!strings.Contains(created.Body.String(), `data-webhook-once="`+base+`/webhooks/new_route"`) ||
				len(fixture.creates) != 1 || fixture.creates[0].Authority.SessionID == "" ||
				fixture.creates[0].Definition.Path != base+"/webhooks/new_route" {
				t.Fatalf("create did not return one-time secret safely: %d", created.Code)
			}
			if got := get("/webhooks/new_route", admin.access); got.Code != http.StatusOK || strings.Contains(got.Body.String(), "created-secret-example") {
				t.Fatalf("secret persisted in GET: %d", got.Code)
			}
			if got := mutation("/webhooks", form); got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "created-secret-example") {
				t.Fatalf("repeat create exposed secret or hid conflict: %d", got.Code)
			}
			update := url.Values{"expected_version": {"2"},
				"report_to": {"telegram:123456:0"}, "prompt_template": {"updated"}, "dedupe_source": {"body_sha256"}}
			if got := mutation("/webhooks/managed", update); got.Code != http.StatusSeeOther ||
				fixture.updates[0].ExpectedVersion != 2 || fixture.updates[0].Definition.ReportTo != "telegram:123456:0" ||
				fixture.updates[0].Definition.Path != "/hooks/managed" {
				t.Fatalf("update failed: %d", got.Code)
			}
			fixture.updateErr = webhookroutecmd.ErrConflict
			if got := mutation("/webhooks/managed", update); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "Webhook operation could not complete") {
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
