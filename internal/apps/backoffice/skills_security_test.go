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
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type skillsHTTPFixture struct {
	SkillsOperations
	inventory   skillcmd.Inventory
	err         error
	outcome     skillcmd.Outcome
	installs    []SkillsInstall
	mutations   []skillcmd.Mutation
	handles     []string
	authorities []skillcmd.Authority
}

func (f *skillsHTTPFixture) Inventory(_ context.Context, a skillcmd.Authority) (skillcmd.Inventory, error) {
	f.authorities = append(f.authorities, a)
	return f.inventory, nil
}
func (f *skillsHTTPFixture) ReviewInstall(_ context.Context, r SkillsInstall) (skillcmd.Candidate, error) {
	f.installs = append(f.installs, r)
	return skillcmd.Candidate{Handle: "opaque-review", Entry: f.inventory.Entries[2], SkillPath: "selected", ResolvedCommit: strings.Repeat("a", 40), ExpiresAt: time.Now().Add(time.Minute)}, f.err
}
func (f *skillsHTTPFixture) ReviewUpdate(_ context.Context, r skillcmd.Mutation) (skillcmd.Candidate, error) {
	f.mutations = append(f.mutations, r)
	return skillcmd.Candidate{Handle: "opaque-update", Entry: f.inventory.Entries[2]}, f.err
}
func (f *skillsHTTPFixture) Accept(_ context.Context, h string, a skillcmd.Authority) (skillcmd.Outcome, error) {
	f.handles = append(f.handles, h)
	f.authorities = append(f.authorities, a)
	return f.outcome, f.err
}
func (f *skillsHTTPFixture) Change(_ context.Context, _ skillcmd.Operation, r skillcmd.Mutation) (skillcmd.Outcome, error) {
	f.mutations = append(f.mutations, r)
	return f.outcome, f.err
}
func (f *skillsHTTPFixture) Refresh(_ context.Context, r skillcmd.Mutation) (skillcmd.Outcome, error) {
	f.mutations = append(f.mutations, r)
	return f.outcome, f.err
}
func (f *skillsHTTPFixture) RetryPublication(_ context.Context, a skillcmd.Authority) (skillcmd.Outcome, error) {
	f.authorities = append(f.authorities, a)
	return f.outcome, f.err
}

func TestSkillsHTTPAuthorityFragmentsAndLifecycle(t *testing.T) {
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
			fixture := &skillsHTTPFixture{inventory: skillcmd.Inventory{ExpectedState: "current"}}
			for _, row := range qaSkills().Skills.Rows {
				fixture.inventory.Entries = append(fixture.inventory.Entries, row.Entry)
			}
			app.skills = fixture
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, string(usercmd.RoleAdministrator))
			operator := loginHTTPAppSession(t, handler, config, string(usercmd.RoleOperator))
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
			paths := []string{"/skills", "/skills/install", "/skills/managed/managed-synthetic", qaSkills().Skills.Rows[0].DetailPath, qaSkills().Skills.Rows[1].DetailPath}
			for _, path := range paths {
				if got := get(path, "", false); got.Code != 401 {
					t.Fatalf("anonymous %s=%d", path, got.Code)
				}
				if got := get(path, operator.access, false); got.Code != 403 {
					t.Fatalf("operator %s=%d", path, got.Code)
				}
				for _, fragment := range []bool{false, true} {
					got := get(path, admin.access, fragment)
					body := got.Body.String()
					if got.Code != 200 || strings.Count(body, `id="main-content"`) != 1 || !strings.Contains(got.Header().Get("Cache-Control"), "no-store") {
						t.Fatalf("page %s fragment=%v status=%d body=%s", path, fragment, got.Code, body)
					}
					if strings.Contains(body, "<!doctype html>") == fragment {
						t.Fatalf("incorrect shell %s", path)
					}
				}
			}
			if strings.Contains(get("/overview", operator.access, false).Body.String(), `href="`+base+`/skills"`) {
				t.Fatal("operator has Skills navigation")
			}
			unmanaged := get(paths[4], admin.access, false).Body.String()
			if !strings.Contains(unmanaged, "read-only here") || strings.Contains(unmanaged, "Review update") {
				t.Fatal("unmanaged ownership incorrectly editable")
			}
			plugin := get(paths[3], admin.access, false).Body.String()
			if !strings.Contains(plugin, base+"/plugins/installed/demo") {
				t.Fatal("owning plugin link missing")
			}
			post := func(path string, form url.Values, fragment bool) *httptest.ResponseRecorder {
				form.Set("csrf_token", admin.csrf)
				return performAccessMutation(t, handler, config, base+path, form, admin.access, admin.csrf, fragment)
			}
			source := "https://private-user:private-canary@example.test/repo?secret=query-canary"
			form := url.Values{"source": {source}, "path": {"selected"}, "expected_state": {"current"}}
			denied := performAccessMutation(t, handler, config, base+"/skills/review", form, admin.access, "wrong", false)
			if denied.Code != 403 || len(fixture.installs) != 0 {
				t.Fatal("CSRF accepted")
			}
			denied = performAccessMutation(t, handler, config, base+"/skills/review", form, operator.access, operator.csrf, false)
			if denied.Code != 403 || len(fixture.installs) != 0 {
				t.Fatal("operator mutation accepted")
			}
			originRequest := httptest.NewRequest(http.MethodPost, base+"/skills/review", strings.NewReader(url.Values{"csrf_token": {admin.csrf}, "source": {source}, "expected_state": {"current"}}.Encode()))
			originRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			originRequest.Header.Set("Origin", "https://other.example")
			originRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
			originRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: admin.csrf})
			originResponse := httptest.NewRecorder()
			handler.ServeHTTP(originResponse, originRequest)
			if originResponse.Code != http.StatusForbidden || len(fixture.installs) != 0 {
				t.Fatal("cross-origin mutation accepted")
			}
			reviewed := post("/skills/review", form, true)
			if reviewed.Code != 200 || !strings.Contains(reviewed.Body.String(), "opaque-review") || strings.Contains(reviewed.Body.String(), "private-canary") || len(fixture.installs) != 1 || fixture.installs[0].Source != source || !fixture.installs[0].Authority.Valid() {
				t.Fatalf("review status=%d body=%s", reviewed.Code, reviewed.Body.String())
			}
			fixture.err = skillcmd.ErrInvalid
			invalid := post("/skills/review", form, true)
			if invalid.Code != 400 || strings.Contains(invalid.Body.String(), "private-canary") || !strings.Contains(invalid.Body.String(), `id="skill-source"`) || strings.Contains(invalid.Body.String(), "<!doctype") {
				t.Fatal("validation reflects source or wrong fragment")
			}
			for _, failure := range []struct {
				err    error
				status int
			}{{skillcmd.ErrForbidden, 403}, {skillcmd.ErrNotFound, 404}, {skillcmd.ErrUnavailable, 503}} {
				fixture.err = failure.err
				got := post("/skills/review", form, true)
				if got.Code != failure.status || strings.Contains(got.Body.String(), "private-canary") || strings.Count(got.Body.String(), `id="main-content"`) != 1 {
					t.Fatalf("error contract status=%d want=%d", got.Code, failure.status)
				}
			}
			fixture.err = nil
			fixture.outcome = skillcmd.Outcome{Accepted: true, Entry: &fixture.inventory.Entries[2]}
			accepted := post("/skills/accept", url.Values{"candidate_handle": {"opaque-review"}}, true)
			if accepted.Code != 204 || !strings.Contains(accepted.Header().Get("HX-Location"), base+"/skills/managed/managed-synthetic") || fixture.handles[0] != "opaque-review" {
				t.Fatal("accept redirect/handle wrong")
			}
			accepted = post("/skills/accept", url.Values{"candidate_handle": {"opaque-review"}}, false)
			if accepted.Code != 303 {
				t.Fatal("native acceptance contract")
			}
			if got := post("/skills/managed/managed-synthetic/disable", url.Values{"expected_state": {"current"}}, false); got.Code != 400 || len(fixture.mutations) != 0 {
				t.Fatal("disable missing confirmation accepted")
			}
			if got := post("/skills/managed/managed-synthetic/remove", url.Values{"expected_state": {"current"}}, false); got.Code != 400 {
				t.Fatal("remove missing confirmation accepted")
			}
			if got := post("/skills/managed/managed-synthetic/disable", url.Values{"expected_state": {"current"}, "confirm": {"yes"}}, false); got.Code != 303 || len(fixture.mutations) != 1 || fixture.mutations[0].ID.Name != "managed-synthetic" {
				t.Fatal("confirmed disable failed")
			}
			fixture.err = skillcmd.ErrConflict
			fixture.outcome = skillcmd.Outcome{}
			conflict := post("/skills/managed/managed-synthetic/review-update", url.Values{"expected_state": {"old"}}, true)
			if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), base+"/skills/managed/managed-synthetic") || strings.Count(conflict.Body.String(), `id="main-content"`) != 1 {
				t.Fatal("conflict lacks exact reopen fragment")
			}
			expired := post("/skills/accept", url.Values{"candidate_handle": {"expired"}, "skill_id": {"managed-synthetic"}}, true)
			if expired.Code != 409 || !strings.Contains(expired.Body.String(), `href="`+base+`/skills/managed/managed-synthetic"`) || strings.Contains(expired.Body.String(), ">Install skill</a>") {
				t.Fatal("expired update lacks exact contextual reopen or permits replay")
			}
			fixture.err = nil
			fixture.outcome = skillcmd.Outcome{Accepted: true, Pending: true}
			fixture.inventory.Pending = true
			pending := post("/skills/accept", url.Values{"candidate_handle": {"opaque-review"}}, true)
			if pending.Code != 503 || !strings.Contains(pending.Body.String(), "Accepted; recovery pending") || !strings.Contains(pending.Body.String(), "Recover publication") || strings.Contains(pending.Body.String(), `name="candidate_handle"`) || strings.Contains(pending.Body.String(), ">Install skill</a>") {
				t.Fatal("accepted pending permits replay or hides recovery")
			}
			for _, a := range fixture.authorities {
				if !a.Valid() {
					t.Fatal("missing current authority")
				}
			}
		})
	}
}
