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
	"github.com/google/uuid"
)

type schedulesHTTPFixture struct {
	items       []schedulecmd.Item
	updateErr   error
	creates     []schedulecmd.Create
	updates     []schedulecmd.Update
	selections  []schedulecmd.ChangeSelection
	deletes     []schedulecmd.Delete
	runRequests []schedulecmd.RunNow
	runs        map[string][]schedulecmd.RunItem
	runErr      error
}

func TestScheduleDefinitionSelectsManagedReportAlias(t *testing.T) {
	definition, err := scheduleDefinition(url.Values{
		"cron": {"0 9 * * *"}, "content": {"review"},
		"report_kind": {"managed_alias"}, "alias": {"main_chat"},
	}, "daily")
	if err != nil || definition.Alias != "main_chat" || definition.Locator != "" {
		t.Fatalf("schedule definition = %+v, err=%v", definition, err)
	}
}

func (f *schedulesHTTPFixture) Inventory(context.Context, schedulecmd.Authority) ([]schedulecmd.Item, error) {
	items := make([]schedulecmd.Item, 0, len(f.items))
	for _, item := range f.items {
		if !item.Deleted {
			items = append(items, item)
		}
	}
	return items, nil
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

func (f *schedulesHTTPFixture) RunNow(_ context.Context, request schedulecmd.RunNow) (schedulecmd.RunItem, error) {
	f.runRequests = append(f.runRequests, request)
	if f.runErr != nil {
		return schedulecmd.RunItem{}, f.runErr
	}
	if request.ID == "disabled" && !request.ConfirmDisabled {
		return schedulecmd.RunItem{}, schedulecmd.ErrConflict
	}
	if request.ID == "archived" {
		return schedulecmd.RunItem{}, schedulecmd.ErrNotFound
	}
	if _, err := uuid.Parse(request.RequestKey); err != nil {
		return schedulecmd.RunItem{}, schedulecmd.ErrInvalid
	}
	return schedulecmd.RunItem{ID: request.RequestKey, Trigger: "manual", State: "queued"}, nil
}

func (f *schedulesHTTPFixture) History(_ context.Context, id string, beforeAt time.Time, beforeID string, limit int, _ schedulecmd.Authority) ([]schedulecmd.RunItem, error) {
	var page []schedulecmd.RunItem
	for _, run := range f.runs[id] {
		if !beforeAt.IsZero() && (run.RequestedAt.After(beforeAt) || run.RequestedAt.Equal(beforeAt) && run.ID >= beforeID) {
			continue
		}
		page = append(page, run)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

func (f *schedulesHTTPFixture) RunDetail(_ context.Context, scheduleID, runID string,
	_ schedulecmd.Authority) (schedulecmd.RunDetail, error) {
	for _, run := range f.runs[scheduleID] {
		if run.ID == runID {
			return schedulecmd.RunDetail{Run: run, Input: "private <script>input</script>",
				Output: "sent <script>output</script>"}, nil
		}
	}
	return schedulecmd.RunDetail{}, schedulecmd.ErrNotFound
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
				{Definition: schedulecmd.Definition{ID: "daily", Cron: "0 9 * * *", Content: "ping <script>alert(1)</script>", Locator: "telegram:9001:0"}, Source: "managed", Enabled: true, Version: 2, Status: "active", NextRunAt: now},
				{Definition: schedulecmd.Definition{ID: "new", Cron: "0 8 * * *", Content: "name collision", Locator: "telegram:9001:0"}, Source: "managed", Enabled: true, Version: 1, Status: "active"},
				{Definition: schedulecmd.Definition{ID: "config:knowl", Cron: "0 10 * * *", Content: "configured", Locator: "telegram:9001:0"}, Source: "config", Enabled: true, Version: 1, Status: "active"},
				{Definition: schedulecmd.Definition{ID: "disabled", Cron: "0 11 * * *", Content: "disabled", Locator: "telegram:9001:0"}, Source: "managed", Enabled: false, Version: 1, Status: "active"},
				{Definition: schedulecmd.Definition{ID: "archived", Cron: "0 12 * * *", Content: "archived", Locator: "telegram:9001:0"}, Source: "managed", Enabled: false, Deleted: true, Version: 2, Status: "active"},
			}}
			fixture.runs = map[string][]schedulecmd.RunItem{
				"config:knowl": {{ID: uuid.NewString(), Trigger: "cron", State: "failed", RequestedAt: now, SafeFailureCode: "private provider error <script>"}},
				"archived":     {{ID: uuid.NewString(), Trigger: "manual", State: "succeeded", RequestedAt: now.Add(-time.Hour), CompletedAt: now}},
				"disabled":     {{ID: uuid.NewString(), Trigger: "manual", State: "dispatched", RequestedAt: now.Add(-time.Minute)}},
			}
			for i := 0; i < scheduleHistoryPageSize+1; i++ {
				fixture.runs["daily"] = append(fixture.runs["daily"], schedulecmd.RunItem{ID: uuid.NewString(), Trigger: "manual", State: "running", RequestedAt: now.Add(-time.Duration(i) * time.Minute)})
			}
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
			if inventory.Code != http.StatusOK || !strings.Contains(inventory.Body.String(), "config:knowl") || !strings.Contains(inventory.Body.String(), "daily") || !strings.Contains(inventory.Body.String(), "telegram:9001:0") || strings.Contains(inventory.Body.String(), "ping &lt;script&gt;") {
				t.Fatalf("unsafe inventory: %d", inventory.Code)
			}
			if strings.Contains(inventory.Body.String(), `href="`+base+`/schedules/archived"`) {
				t.Fatal("archived schedule appeared in inventory")
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
			if !strings.Contains(managed.Body.String(), `name="locator" value="telegram:9001:0"`) ||
				strings.Contains(managed.Body.String(), `name="target_kind"`) ||
				strings.Contains(managed.Body.String(), `name="report_to_key"`) {
				t.Fatal("managed editor must contain one report locator")
			}
			if got := get("/schedules/new", admin.access); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "name collision") || strings.Contains(got.Body.String(), "Add schedule</h1>") {
				t.Fatalf("schedule named new is hidden: %d", got.Code)
			}
			if got := get("/schedules?new=1", admin.access); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Add schedule</h1>") {
				t.Fatalf("creation form = %d", got.Code)
			}
			configuredHistory := get("/schedules/config:knowl", admin.access)
			if configuredHistory.Code != http.StatusOK || !strings.Contains(configuredHistory.Body.String(), "Run now") || !strings.Contains(configuredHistory.Body.String(), "Scheduled") || !strings.Contains(configuredHistory.Body.String(), "Execution could not complete") || strings.Contains(configuredHistory.Body.String(), "private provider error") {
				t.Fatalf("configured run/history view = %d", configuredHistory.Code)
			}
			runPath := "/schedules/config:knowl?run_id=" + url.QueryEscape(fixture.runs["config:knowl"][0].ID)
			if got := get(runPath, "").Code; got != http.StatusUnauthorized {
				t.Fatalf("anonymous run detail = %d", got)
			}
			if got := get(runPath, operator.access).Code; got != http.StatusForbidden {
				t.Fatalf("operator run detail = %d", got)
			}
			detail := get(runPath, admin.access)
			if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "private &lt;script&gt;input") ||
				!strings.Contains(detail.Body.String(), "sent &lt;script&gt;output") ||
				strings.Contains(detail.Body.String(), "<script>output</script>") ||
				strings.Contains(configuredHistory.Body.String(), "private &lt;script&gt;input") {
				t.Fatalf("run detail access or escaping = %d", detail.Code)
			}
			archivedHistory := get("/schedules/archived", admin.access)
			if archivedHistory.Code != http.StatusOK || !strings.Contains(archivedHistory.Body.String(), "Archived schedule") || !strings.Contains(archivedHistory.Body.String(), "Succeeded") || strings.Contains(archivedHistory.Body.String(), "Run now</button>") {
				t.Fatalf("archived history view = %d", archivedHistory.Code)
			}
			if !strings.Contains(managed.Body.String(), "Older runs") {
				t.Fatal("history pagination link missing")
			}
			cursor := url.Values{"before_at": {fixture.runs["daily"][scheduleHistoryPageSize-1].RequestedAt.Format(time.RFC3339Nano)}, "before_id": {fixture.runs["daily"][scheduleHistoryPageSize-1].ID}}
			older := get("/schedules/daily?"+cursor.Encode(), admin.access)
			if older.Code != http.StatusOK || strings.Contains(older.Body.String(), "Older runs") || !strings.Contains(older.Body.String(), "Running") {
				t.Fatalf("older history page = %d", older.Code)
			}
			if got := get("/schedules/daily?before_at=bad&before_id=bad", admin.access); got.Code != http.StatusBadRequest {
				t.Fatalf("bad history cursor = %d", got.Code)
			}
			mutation := func(path string, form url.Values) *httptest.ResponseRecorder {
				t.Helper()
				form.Set("csrf_token", admin.csrf)
				return performAccessMutation(t, handler, config, base+path, form, admin.access, admin.csrf, false)
			}
			if got := mutation("/schedules/config:knowl/delete", url.Values{"expected_version": {"1"}, "confirm": {"yes"}}).Code; got != http.StatusForbidden || len(fixture.deletes) != 0 {
				t.Fatalf("configured deletion = %d", got)
			}
			createForm := url.Values{"id": {"new-daily"}, "cron": {"0 11 * * *"}, "locator": {"telegram:9001:0"}, "content": {"run report"}}
			legacyForm := url.Values{"id": {"new-daily"}, "cron": {"0 11 * * *"}, "locator": {"telegram:9001:0"},
				"content": {"run report"}, "report_to_key": {"telegram:9002:0"}}
			if got := mutation("/schedules", legacyForm).Code; got != http.StatusBadRequest || len(fixture.creates) != 0 {
				t.Fatalf("legacy report input reached schedule owner: %d", got)
			}
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
			withoutReport := mutation("/schedules", url.Values{"id": {"local-review"},
				"cron": {"0 11 * * *"}, "content": {"review locally"}})
			if withoutReport.Code != http.StatusSeeOther || len(fixture.creates) != 2 ||
				fixture.creates[1].Definition.Locator != "" {
				t.Fatalf("no-report schedule create = %d, definitions=%+v", withoutReport.Code, fixture.creates)
			}
			updateForm := url.Values{"expected_version": {"2"}, "cron": {"0 12 * * *"}, "locator": {"telegram:9001:0"}, "content": {"updated"}}
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
			for _, id := range []string{"daily", "config:knowl"} {
				key := uuid.NewString()
				for attempt := 0; attempt < 2; attempt++ {
					result := mutation("/schedules/"+id+"/runs", url.Values{"request_key": {key}})
					if result.Code != http.StatusSeeOther || result.Header().Get("Location") != base+"/schedules/"+id {
						t.Fatalf("run %s attempt %d = %d", id, attempt, result.Code)
					}
				}
				last := fixture.runRequests[len(fixture.runRequests)-1]
				previous := fixture.runRequests[len(fixture.runRequests)-2]
				if last.RequestKey != key || previous.RequestKey != key || last.ID != id || previous.ID != id || last.Authority.SessionID == "" {
					t.Fatalf("manual run idempotency input %s: %+v, %+v", id, previous, last)
				}
			}
			if page := get("/schedules/disabled", admin.access); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Waiting for execution") || strings.Contains(page.Body.String(), ">Running<") {
				t.Fatalf("dispatched state is misrepresented: %d", page.Code)
			}
			if got := mutation("/schedules/disabled/runs", url.Values{"request_key": {uuid.NewString()}}); got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "No runs yet") {
				t.Fatalf("unconfirmed disabled run = %d", got.Code)
			}
			if got := mutation("/schedules/disabled/runs", url.Values{"request_key": {uuid.NewString()}, "confirm_disabled": {"yes"}}).Code; got != http.StatusSeeOther {
				t.Fatalf("confirmed disabled run = %d", got)
			}
			if got := mutation("/schedules/archived/runs", url.Values{"request_key": {uuid.NewString()}}).Code; got != http.StatusNotFound {
				t.Fatalf("archived manual run = %d", got)
			}
			fixture.runErr = schedulecmd.ErrUnavailable
			key := uuid.NewString()
			uncertain := mutation("/schedules/daily/runs", url.Values{"request_key": {key}})
			if uncertain.Code != http.StatusServiceUnavailable || !strings.Contains(uncertain.Body.String(), "may have been queued") || !strings.Contains(uncertain.Body.String(), `name="request_key" value="`+key+`"`) || strings.Contains(uncertain.Body.String(), "No runs yet") {
				t.Fatalf("uncertain manual admission = %d", uncertain.Code)
			}
		})
	}
}
