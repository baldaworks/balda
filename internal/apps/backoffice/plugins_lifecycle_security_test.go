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
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const pluginLifecycleMarketplace = "team"
const pluginLifecycleCurrent = "r1"
const pluginLifecycleExpected = "observed-plugin-state"
const pluginLifecycleCandidateToken = "reviewed-content"

type pluginLifecycleSecurityFixture struct {
	PluginsOperations
	mutations          []plugincmd.PluginMutation
	err                error
	outcome            plugincmd.MutationOutcome
	removed            bool
	unavailable        bool
	installedDeclared  bool
	discoveredDeclared bool
}

func (f *pluginLifecycleSecurityFixture) Inventory(context.Context, plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	return []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: "install", Version: "1", Marketplace: pluginLifecycleMarketplace, Enabled: true}, DeclaresBaldaExtension: f.installedDeclared}}, nil
}
func (f *pluginLifecycleSecurityFixture) Detail(_ context.Context, name string, _ plugincmd.Authority) (plugincmd.PluginDetail, error) {
	return plugincmd.PluginDetail{InstalledView: plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: name, Version: "1", Marketplace: pluginLifecycleMarketplace, Revision: pluginLifecycleCurrent, Enabled: !f.removed, Runtime: plugincmd.RuntimeStatus{RevisionCurrent: true, SnapshotID: "published-snapshot"}}, ExpectedState: pluginLifecycleExpected, OriginAvailable: !f.removed, RecoveryPending: f.removed, Removed: f.removed, DeclaresBaldaExtension: f.installedDeclared}, Commands: []plugincmd.Contribution{{Name: "ship", Description: "<script>window.pluginCanary=true</script>"}}, Revisions: []plugincmd.RetainedRevision{{Version: "1", Revision: pluginLifecycleCurrent, Available: true, Current: !f.removed}, {Version: "0", Revision: "r0", Available: true}}}, nil
}
func (f *pluginLifecycleSecurityFixture) Marketplaces(context.Context, plugincmd.Authority) ([]plugincmd.MarketplaceView, error) {
	return []plugincmd.MarketplaceView{{Name: pluginLifecycleMarketplace, Source: "local", LastAttemptStatus: "succeeded"}}, nil
}
func (f *pluginLifecycleSecurityFixture) Marketplace(_ context.Context, name string, _ plugincmd.Authority) (plugincmd.MarketplaceDetail, error) {
	token := pluginLifecycleCandidateToken
	if f.unavailable {
		token = ""
	}
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: name, LastAttemptStatus: "succeeded"}, Candidates: []plugincmd.CandidateView{{Name: "demo", Marketplace: name, Version: "2", Revision: "r2", CandidateToken: token, ExpectedState: pluginLifecycleExpected, DeclaresBaldaExtension: f.discoveredDeclared}}}, nil
}
func (f *pluginLifecycleSecurityFixture) Candidate(_ context.Context, name, marketplace string, _ plugincmd.Authority) (plugincmd.CandidateView, error) {
	if f.unavailable {
		return plugincmd.CandidateView{}, plugincmd.ErrUnavailable
	}
	if marketplace == "" {
		marketplace = pluginLifecycleMarketplace
	}
	return plugincmd.CandidateView{Name: name, Marketplace: marketplace, Version: "2", Revision: "r2", CandidateToken: pluginLifecycleCandidateToken, ExpectedState: pluginLifecycleExpected}, nil
}
func (f *pluginLifecycleSecurityFixture) MutatePlugin(_ context.Context, r plugincmd.PluginMutation) (plugincmd.MutationOutcome, error) {
	f.mutations = append(f.mutations, r)
	if r.Operation == plugincmd.Remove && f.outcome.Accepted && f.outcome.RecoveryPending {
		f.removed = true
	}
	return f.outcome, f.err
}

func TestPluginLifecycleHTTPRoutesActionsAndRecovery(t *testing.T) {
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
			fixture := &pluginLifecycleSecurityFixture{outcome: plugincmd.MutationOutcome{Accepted: true}}
			app.plugins = fixture
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, string(usercmd.RoleAdministrator))
			operator := loginHTTPAppSession(t, handler, config, string(usercmd.RoleOperator))
			get := func(path, access string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, base+path, nil)
				if access != "" {
					r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for _, state := range []struct {
				installed, discovered bool
			}{
				{installed: true},
				{discovered: true},
			} {
				fixture.installedDeclared = state.installed
				fixture.discoveredDeclared = state.discovered
				for _, page := range []struct {
					path string
					want bool
				}{
					{path: "/plugins", want: state.installed},
					{path: "/plugins/installed/install", want: state.installed},
					{path: "/plugins/marketplaces/definitions/team", want: state.discovered},
				} {
					got := get(page.path, admin.access)
					wantCount := 0
					if page.want {
						wantCount = 1
					}
					if got.Code != http.StatusOK || strings.Count(got.Body.String(), ">Balda extension</span>") != wantCount {
						t.Fatalf("declaration GET %s: status=%d, marker count=%d, want=%d", page.path, got.Code, strings.Count(got.Body.String(), ">Balda extension</span>"), wantCount)
					}
				}
			}
			if len(fixture.mutations) != 0 {
				t.Fatal("declaration GET mutated plugin state")
			}
			fixture.installedDeclared = false
			fixture.discoveredDeclared = false
			mutation := func(path string, form url.Values, access, csrf string, fragment bool) *httptest.ResponseRecorder {
				form.Set("csrf_token", csrf)
				return performAccessMutation(t, handler, config, base+path, form, access, csrf, fragment)
			}
			for _, name := range []string{"install", "marketplaces"} {
				path := "/plugins/installed/" + name
				if got := get(path, ""); got.Code != http.StatusUnauthorized {
					t.Fatalf("anonymous %s=%d", name, got.Code)
				}
				if got := get(path, operator.access); got.Code != http.StatusForbidden {
					t.Fatalf("operator %s=%d", name, got.Code)
				}
				got := get(path, admin.access)
				body := got.Body.String()
				if got.Code != http.StatusOK || strings.Contains(body, "<script>window.pluginCanary") || !strings.Contains(body, "&lt;script&gt;window.pluginCanary") {
					t.Fatalf("safe valid plugin detail %s=%d", name, got.Code)
				}
				if !strings.Contains(got.Header().Get("Cache-Control"), "no-store") {
					t.Fatal("plugin detail not private")
				}
				before := len(fixture.mutations)
				if got := mutation(path+"/remove", url.Values{"expected_state": {pluginLifecycleExpected}}, admin.access, admin.csrf, false); got.Code != http.StatusBadRequest || len(fixture.mutations) != before {
					t.Fatal("unconfirmed remove reached lifecycle")
				}
				if got := mutation(path+"/disable", url.Values{"expected_state": {pluginLifecycleExpected}, "confirm": {checkedFormValue}}, admin.access, admin.csrf, false); got.Code != http.StatusSeeOther || got.Header().Get("Location") != base+path {
					t.Fatalf("valid name disable %s=%d", name, got.Code)
				}
				request := fixture.mutations[len(fixture.mutations)-1]
				if request.PluginName != name || request.Operation != plugincmd.Disable || !request.Confirm || request.ExpectedState != pluginLifecycleExpected || request.Authority.SessionID == "" {
					t.Fatalf("route selector=%+v", request)
				}
			}
			if got := get("/plugins/install?marketplace=team&plugin=demo", admin.access); got.Code != http.StatusOK {
				t.Fatalf("install review=%d", got.Code)
			}
			if got := mutation("/plugins/install", url.Values{"plugin_name": {"demo"}, "marketplace_name": {pluginLifecycleMarketplace}, "candidate_token": {pluginLifecycleCandidateToken}, "expected_state": {pluginLifecycleExpected}}, admin.access, admin.csrf, false); got.Code != http.StatusSeeOther {
				t.Fatalf("install=%d", got.Code)
			}
			installed := fixture.mutations[len(fixture.mutations)-1]
			if installed.Operation != plugincmd.Install || installed.MarketplaceName != pluginLifecycleMarketplace || installed.CandidateToken != pluginLifecycleCandidateToken {
				t.Fatalf("review binding=%+v", installed)
			}
			for _, operation := range []plugincmd.Operation{plugincmd.Enable, plugincmd.Upgrade, plugincmd.Rollback} {
				form := url.Values{"expected_state": {pluginLifecycleExpected}}
				if operation == plugincmd.Upgrade {
					form.Set("candidate_token", pluginLifecycleCandidateToken)
				}
				if operation == plugincmd.Rollback {
					form.Set("revision", "r0")
					before := len(fixture.mutations)
					if got := mutation("/plugins/installed/install/rollback", form, admin.access, admin.csrf, false); got.Code != http.StatusBadRequest || len(fixture.mutations) != before {
						t.Fatal("unconfirmed rollback reached lifecycle")
					}
					form.Set("confirm", checkedFormValue)
				}
				path := "/plugins/installed/install/" + string(operation)
				got := mutation(path, form, admin.access, admin.csrf, false)
				if got.Code != http.StatusSeeOther || got.Header().Get("Location") != base+"/plugins/installed/install" {
					t.Fatalf("%s=%d", operation, got.Code)
				}
				request := fixture.mutations[len(fixture.mutations)-1]
				if request.Operation != operation || request.MarketplaceName != "" || request.ExpectedState != pluginLifecycleExpected || request.Authority.SessionID == "" {
					t.Fatalf("operation binding=%+v", request)
				}
				if operation == plugincmd.Upgrade && request.CandidateToken != pluginLifecycleCandidateToken {
					t.Fatal("upgrade lost reviewed content token")
				}
				if operation == plugincmd.Rollback && (request.Revision != "r0" || !request.Confirm) {
					t.Fatal("rollback lost exact retained revision")
				}
			}
			fixture.unavailable = true
			unavailable := get("/plugins/install?marketplace=team&plugin=demo", admin.access)
			if unavailable.Code != http.StatusServiceUnavailable || strings.Contains(unavailable.Body.String(), `hx-post="`+base+`/plugins/install"`) {
				t.Fatalf("unavailable candidate offers submission: %d", unavailable.Code)
			}
			fixture.unavailable = false
			fixture.err = plugincmd.ErrConflict
			for _, fragment := range []bool{false, true} {
				got := mutation("/plugins/installed/install/remove", url.Values{"expected_state": {pluginLifecycleExpected}, "confirm": {checkedFormValue}}, admin.access, admin.csrf, fragment)
				if got.Code != http.StatusConflict || strings.Count(got.Body.String(), "<main") != 1 || !strings.Contains(got.Body.String(), base+"/plugins/installed/install") {
					t.Fatalf("stale action=%d", got.Code)
				}
			}
			fixture.err = plugincmd.ErrRecoveryPending
			fixture.outcome = plugincmd.MutationOutcome{Accepted: true, RecoveryPending: true}
			pending := mutation("/plugins/installed/install/remove", url.Values{"expected_state": {pluginLifecycleExpected}, "confirm": {checkedFormValue}}, admin.access, admin.csrf, true)
			if pending.Code != http.StatusServiceUnavailable || !strings.Contains(strings.ToLower(pending.Body.String()), "accepted") || !strings.Contains(pending.Body.String(), base+"/plugins/installed/install") {
				t.Fatalf("accepted pending=%d", pending.Code)
			}
			fixture.removed = true
			removed := get("/plugins/installed/install", admin.access)
			if removed.Code != http.StatusOK {
				t.Fatalf("pending removed detail=%d", removed.Code)
			}
			for _, operation := range []string{"enable", "disable", "upgrade", "rollback", "remove"} {
				if strings.Contains(removed.Body.String(), `action="`+base+"/plugins/installed/install/"+operation+`"`) {
					t.Errorf("removed detail exposes %s action", operation)
				}
			}
		})
	}
}
