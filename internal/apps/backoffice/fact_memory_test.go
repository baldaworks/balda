package backoffice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/memorybackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestFactMemoryHTTPManagement(t *testing.T) {
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
			store := memory.NewStore(provider.AppKV(), "", true)
			app.factMemory = memorybackofficeapp.New(memory.NewManagement(store, provider.MemoryManagement()))
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
			if got := get("/memory", admin.access, false); got.Code != 200 {
				t.Fatalf("inventory=%d body=%s", got.Code, got.Body.String())
			}
			if got := get("/memory", operator.access, false); got.Code != 403 {
				t.Fatalf("operator inventory=%d", got.Code)
			}
			if got := get("/overview", operator.access, false); strings.Contains(got.Body.String(), `href="`+base+`/memory"`) {
				t.Fatal("operator received Fact memory navigation")
			}
			if got := get("/memory", "", false); got.Code != 401 {
				t.Fatalf("anonymous=%d", got.Code)
			}
			mutation := func(path string, form url.Values, access, csrf string, fragment bool) *httptest.ResponseRecorder {
				form.Set("csrf_token", csrf)
				return performAccessMutation(t, handler, config, base+path, form, access, csrf, fragment)
			}
			if got := mutation("/memory", url.Values{"fact": {"alpha\n<script>data</script>"}}, operator.access, operator.csrf, false); got.Code != 403 {
				t.Fatalf("operator create=%d", got.Code)
			}
			if got := mutation("/memory", url.Values{"fact": {"alpha\n<script>data</script>"}}, admin.access, admin.csrf, false); got.Code != 303 || got.Header().Get("Location") != base+"/memory" {
				t.Fatalf("create=%d location=%s", got.Code, got.Header().Get("Location"))
			}
			if _, err := store.Remember(t.Context(), "beta"); err != nil {
				t.Fatal(err)
			}
			list := get("/memory?q=ALPHA", admin.access, true)
			if list.Code != 200 || strings.Contains(list.Body.String(), "<script>data") || !strings.Contains(list.Body.String(), "&lt;script&gt;data") || strings.Contains(list.Body.String(), ">beta<") || strings.Contains(list.Body.String(), "<!DOCTYPE") {
				t.Fatal("filtered fragment escaped/current content mismatch")
			}
			if got := get("/memory/1", admin.access, false); got.Code != 200 || !strings.Contains(got.Body.String(), `name="expected_version" value="2"`) {
				t.Fatalf("editor=%d", got.Code)
			}
			if _, err := store.Remember(t.Context(), "gamma"); err != nil {
				t.Fatal(err)
			}
			stale := mutation("/memory/1", url.Values{"fact": {"unsaved textarea"}, "expected_version": {"2"}}, admin.access, admin.csrf, true)
			if stale.Code != 409 || !strings.Contains(stale.Body.String(), "unsaved textarea") || !strings.Contains(stale.Body.String(), `value="2"`) {
				t.Fatalf("staleedit=%d", stale.Code)
			}
			if got := mutation("/memory/1", url.Values{"fact": {"alpha2"}, "expected_version": {"3"}}, admin.access, admin.csrf, true); got.Code != 204 {
				t.Fatalf("edit=%d body=%s", got.Code, got.Body.String())
			}
			if got := mutation("/memory/1/delete", url.Values{"expected_version": {"4"}}, admin.access, admin.csrf, false); got.Code != 400 {
				t.Fatalf("unconfirmeddelete=%d", got.Code)
			}
			if got := mutation("/memory/1/delete", url.Values{"expected_version": {"4"}, "confirm": {"yes"}}, admin.access, admin.csrf, false); got.Code != 303 {
				t.Fatalf("delete=%d", got.Code)
			}
			if got := get("/memory/1", admin.access, false); got.Code != 404 {
				t.Fatalf("missing=%d", got.Code)
			}
			snapshot, err := store.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Content != "beta\n\ngamma" || snapshot.Version != 5 {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			for i, form := range []url.Values{{"fact": {" \n"}}, {"fact": {"x"}, "expected_version": {"no"}}} {
				path := "/memory"
				if i == 1 {
					path += "/2"
				}
				if got := mutation(path, form, admin.access, admin.csrf, false); got.Code != 400 {
					t.Fatalf("validation%d=%d", i, got.Code)
				}
			}
			for _, id := range []int{2, 3} {
				version := 5
				if id == 3 {
					version = 6
				}
				if got := mutation("/memory/"+strconv.Itoa(id)+"/delete", url.Values{"expected_version": {strconv.Itoa(version)}, "confirm": {"yes"}}, admin.access, admin.csrf, false); got.Code != 303 {
					t.Fatalf("finaldelete=%d", got.Code)
				}
			}
			if got := get("/memory?q=none", admin.access, false); !strings.Contains(got.Body.String(), "No facts yet.") {
				t.Fatal("initialized empty search did not stay empty")
			}
			for _, failure := range []string{"origin", "csrf"} {
				form := url.Values{"fact": {"denied"}, "csrf_token": {admin.csrf}}
				if failure == "csrf" {
					form.Set("csrf_token", "wrong")
				}
				r := httptest.NewRequest(http.MethodPost, base+"/memory", strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", config.Server.PublicURL)
				if failure == "origin" {
					r.Header.Set("Origin", "https://foreign.example.test")
				}
				r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
				r.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: admin.csrf})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 403 {
					t.Fatalf("%s denial=%d", failure, w.Code)
				}
			}
			oversize := mutation("/memory", url.Values{"fact": {strings.Repeat("x", 1<<20)}}, admin.access, admin.csrf, false)
			if oversize.Code != 400 || !strings.Contains(oversize.Body.String(), "Shorten the fact") {
				t.Fatalf("oversize=%d", oversize.Code)
			}
			app.factMemory = unavailableFactMemory{app.factMemory}
			failed := mutation("/memory", url.Values{"fact": {"safe submitted fact"}}, admin.access, admin.csrf, true)
			if failed.Code != 503 || !strings.Contains(failed.Body.String(), "safe submitted fact") || !strings.Contains(failed.Body.String(), "check its current state") {
				t.Fatalf("backend failure=%d", failed.Code)
			}
			app.factMemory = memorybackofficeapp.New(memory.NewManagement(memory.NewStore(provider.AppKV(), "", false), provider.MemoryManagement()))
			if got := get("/memory?new=1", admin.access, false); got.Code != 200 || !strings.Contains(got.Body.String(), "disabled") || strings.Contains(got.Body.String(), `name="fact"`) {
				t.Fatal("disabled page mismatch")
			}
			if got := mutation("/memory", url.Values{"fact": {"disabled write"}}, admin.access, admin.csrf, false); got.Code != 403 {
				t.Fatalf("disabledwrite=%d", got.Code)
			}
		})
	}
}

type unavailableFactMemory struct{ FactMemoryOperations }

func (unavailableFactMemory) Create(context.Context, memorycmd.Create) (memorycmd.Fact, error) {
	return memorycmd.Fact{}, memorycmd.ErrUnavailable
}
