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
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type schedulesHTTPFixture struct {
	items      []schedulecmd.Item
	updateErr  error
	creates    []schedulecmd.Create
	updates    []schedulecmd.Update
	selections []schedulecmd.ChangeSelection
	deletes    []schedulecmd.Delete
}

func (f *schedulesHTTPFixture) Inventory(context.Context, schedulecmd.Authority) ([]schedulecmd.Item, error) {
	return f.items, nil
}

func (f *schedulesHTTPFixture) Get(_ context.Context, id string, _ schedulecmd.Authority) (schedulecmd.Item, error) {
	for _, item := range f.items {
		if item.Definition.ID == id {
			return item, nil
		}
	}
	return schedulecmd.Item{}, schedulecmd.ErrNotFound
}

func (f *schedulesHTTPFixture) Create(_ context.Context, request schedulecmd.Create) (schedulecmd.Item, error) {
	f.creates = append(f.creates, request)
	return schedulecmd.Item{Definition: request.Definition, Source: "managed", Version: 1, Enabled: true}, nil
}

func (f *schedulesHTTPFixture) Update(_ context.Context, request schedulecmd.Update) (schedulecmd.Item, error) {
	f.updates = append(f.updates, request)
	if f.updateErr != nil {
		return schedulecmd.Item{}, f.updateErr
	}
	return schedulecmd.Item{Definition: request.Definition, Source: "managed", Version: request.ExpectedVersion + 1}, nil
}

func (f *schedulesHTTPFixture) SetEnabled(_ context.Context, request schedulecmd.ChangeSelection) (schedulecmd.Item, error) {
	f.selections = append(f.selections, request)
	return schedulecmd.Item{Definition: schedulecmd.Definition{ID: request.ID}, Source: "managed", Version: request.ExpectedVersion + 1}, nil
}

func (f *schedulesHTTPFixture) Delete(_ context.Context, request schedulecmd.Delete) (schedulecmd.Item, error) {
	f.deletes = append(f.deletes, request)
	return schedulecmd.Item{Definition: schedulecmd.Definition{ID: request.ID}, Source: "managed", Deleted: true}, nil
}

func TestSchedulesBrowserManagement(t *testing.T) {
	for _, base := range []string{"", "/balda"} {
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
			fixture := &schedulesHTTPFixture{items: []schedulecmd.Item{
				{Definition: schedulecmd.Definition{ID: "daily", Cron: "0 9 * * *", Content: "ping <script>alert(1)</script>", Target: schedulecmd.Target{Kind: "alias", Key: "owner"}}, Source: "managed", Enabled: true, Version: 2, Status: "active", NextRunAt: now},
				{Definition: schedulecmd.Definition{ID: "new", Cron: "0 8 * * *", Content: "name collision", Target: schedulecmd.Target{Kind: "alias", Key: "owner"}}, Source: "managed", Enabled: true, Version: 1, Status: "active"},
				{Definition: schedulecmd.Definition{ID: "config:knowl", Cron: "0 10 * * *", Content: "configured", Target: schedulecmd.Target{Kind: "alias", Key: "owner"}, ReportTo: &schedulecmd.Target{Kind: "session", Key: "report-session"}}, Source: "config", Enabled: true, Version: 1, Status: "active"},
			}}
			app.schedules = fixture
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "administrator")
			operator := loginHTTPAppSession(t, handler, config, "operator")
			get := func(path, access string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequest(http.MethodGet, base+path, nil)
				if access != "" {
					request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			for _, path := range []string{"/schedules", "/schedules?new=1", "/schedules/daily"} {
				if got := get(path, "").Code; got != http.StatusUnauthorized {
					t.Fatalf("anonymous %s = %d", path, got)
				}
				if got := get(path, operator.access).Code; got != http.StatusForbidden {
					t.Fatalf("operator %s = %d", path, got)
				}
			}
			inventory := get("/schedules", admin.access)
			if inventory.Code != http.StatusOK || !strings.Contains(inventory.Body.String(), "config:knowl") || !strings.Contains(inventory.Body.String(), "daily") || !strings.Contains(inventory.Body.String(), "Report to: session: report-session") || strings.Contains(inventory.Body.String(), "ping &lt;script&gt;") {
				t.Fatalf("unsafe inventory: %d", inventory.Code)
			}
			if !strings.Contains(inventory.Body.String(), `href="`+base+`/schedules/daily"`) || !strings.Contains(inventory.Body.String(), "Schedules") {
				t.Fatal("schedule navigation or base-path link missing")
			}
			configured := get("/schedules/config:knowl", admin.access)
			if configured.Code != http.StatusOK || !strings.Contains(configured.Body.String(), "Configuration schedule") || strings.Contains(configured.Body.String(), `name="expected_version"`) {
				t.Fatalf("configured schedule is editable: %d", configured.Code)
			}
			managed := get("/schedules/daily", admin.access)
			if managed.Code != http.StatusOK || !strings.Contains(managed.Body.String(), `name="expected_version"`) || !strings.Contains(managed.Body.String(), "ping &lt;script&gt;") || strings.Contains(managed.Body.String(), "<script>alert(1)</script>") {
				t.Fatalf("managed editor is missing or unsafe: %d", managed.Code)
			}
			if got := get("/schedules/new", admin.access); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "name collision") || strings.Contains(got.Body.String(), "Add schedule</h1>") {
				t.Fatalf("schedule named new is hidden: %d", got.Code)
			}
			if got := get("/schedules?new=1", admin.access); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Add schedule</h1>") {
				t.Fatalf("creation form = %d", got.Code)
			}
			mutation := func(path string, form url.Values) *httptest.ResponseRecorder {
				t.Helper()
				form.Set("csrf_token", admin.csrf)
				return performAccessMutation(t, handler, config, base+path, form, admin.access, admin.csrf, false)
			}
			if got := mutation("/schedules/config:knowl/delete", url.Values{"expected_version": {"1"}, "confirm": {"yes"}}).Code; got != http.StatusForbidden || len(fixture.deletes) != 0 {
				t.Fatalf("configured deletion = %d", got)
			}
			createForm := url.Values{"id": {"new-daily"}, "cron": {"0 11 * * *"}, "target_kind": {"alias"}, "target_key": {"owner"}, "content": {"run report"}}
			invalidCSRF := url.Values{}
			for key, values := range createForm {
				invalidCSRF[key] = append([]string(nil), values...)
			}
			invalidCSRF.Set("csrf_token", "wrong")
			if got := performAccessMutation(t, handler, config, base+"/schedules", invalidCSRF, admin.access, admin.csrf, false).Code; got != http.StatusForbidden || len(fixture.creates) != 0 {
				t.Fatalf("invalid CSRF reached schedule owner: %d", got)
			}
			wrongOrigin := httptest.NewRequest(http.MethodPost, base+"/schedules", strings.NewReader(createForm.Encode()))
			wrongOrigin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			wrongOrigin.Header.Set("Origin", "https://foreign.example")
			wrongOrigin.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
			wrongOrigin.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: admin.csrf})
			wrongOriginResponse := httptest.NewRecorder()
			handler.ServeHTTP(wrongOriginResponse, wrongOrigin)
			if wrongOriginResponse.Code != http.StatusForbidden || len(fixture.creates) != 0 {
				t.Fatalf("foreign origin reached schedule owner: %d", wrongOriginResponse.Code)
			}
			created := mutation("/schedules", createForm)
			if created.Code != http.StatusSeeOther || created.Header().Get("Location") != base+"/schedules/new-daily" || len(fixture.creates) != 1 || fixture.creates[0].Authority.SessionID == "" {
				t.Fatalf("schedule create = %d, location %q", created.Code, created.Header().Get("Location"))
			}
			updateForm := url.Values{"expected_version": {"2"}, "cron": {"0 12 * * *"}, "target_kind": {"alias"}, "target_key": {"owner"}, "content": {"updated"}}
			if got := mutation("/schedules/daily", updateForm).Code; got != http.StatusSeeOther || len(fixture.updates) != 1 || fixture.updates[0].ID != "daily" || fixture.updates[0].ExpectedVersion != 2 {
				t.Fatalf("schedule update = %d", got)
			}
			fixture.updateErr = schedulecmd.ErrConflict
			conflict := mutation("/schedules/daily", updateForm)
			if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "Reopen it before trying again") {
				t.Fatalf("stale schedule response = %d", conflict.Code)
			}
			fixture.updateErr = nil
			if got := mutation("/schedules/daily/selection", url.Values{"expected_version": {"2"}, "confirm": {"yes"}, "enabled": {"no"}}).Code; got != http.StatusSeeOther || len(fixture.selections) != 1 || fixture.selections[0].Enabled {
				t.Fatalf("schedule disable = %d", got)
			}
			if got := mutation("/schedules/daily/selection", url.Values{"expected_version": {"2"}, "enabled": {"no"}}).Code; got != http.StatusBadRequest || len(fixture.selections) != 1 {
				t.Fatalf("unconfirmed schedule disable = %d", got)
			}
			if got := mutation("/schedules/daily/delete", url.Values{"expected_version": {"2"}, "confirm": {"yes"}}).Code; got != http.StatusSeeOther || len(fixture.deletes) != 1 {
				t.Fatalf("schedule delete = %d", got)
			}
		})
	}
}
