package backoffice

import (
	"context"
	"errors"
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

const marketplaceSecurityIdentity = "new"
const marketplaceSecurityAddName = "safe-name"
const marketplaceSecuritySource = "https://private-user:private-source-canary@example.test/repo?token=private-query-canary"

type pluginMarketplaceSecurityFixture struct {
	PluginsOperations
	err       error
	readErr   error
	refreshes []plugincmd.MarketplaceMutation
	removes   []plugincmd.MarketplaceMutation
	adds      []plugincmd.MarketplaceAdd
}

func (f *pluginMarketplaceSecurityFixture) Inventory(context.Context, plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	return nil, f.readErr
}
func (f *pluginMarketplaceSecurityFixture) Marketplaces(context.Context, plugincmd.Authority) ([]plugincmd.MarketplaceView, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return []plugincmd.MarketplaceView{{Name: marketplaceSecurityIdentity, Source: "local", Kind: "local", ExpectedState: "current-state"}}, nil
}
func (f *pluginMarketplaceSecurityFixture) Marketplace(_ context.Context, name string, _ plugincmd.Authority) (plugincmd.MarketplaceDetail, error) {
	if name != marketplaceSecurityIdentity {
		return plugincmd.MarketplaceDetail{}, plugincmd.ErrNotFound
	}
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: name, Source: "local", Kind: "local", ExpectedState: "current-state", LastAttemptAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), LastAttemptStatus: "failed", FailureCode: "invalid_index", LastSuccessfulAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Candidates: []plugincmd.CandidateView{{Name: "demo", Marketplace: name, Revision: "retained-evidence"}}}, nil
}
func (f *pluginMarketplaceSecurityFixture) AddMarketplace(_ context.Context, r plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error) {
	f.adds = append(f.adds, r)
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: r.Name}}, f.err
}
func (f *pluginMarketplaceSecurityFixture) RefreshMarketplace(_ context.Context, r plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error) {
	f.refreshes = append(f.refreshes, r)
	return plugincmd.MarketplaceDetail{}, f.err
}
func (f *pluginMarketplaceSecurityFixture) RemoveMarketplace(_ context.Context, r plugincmd.MarketplaceMutation) error {
	f.removes = append(f.removes, r)
	return f.err
}

func TestPluginMarketplaceHTTPAuthorityAndCollisions(t *testing.T) {
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
			fixture := &pluginMarketplaceSecurityFixture{}
			app.plugins = fixture
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
			for _, path := range []string{"/plugins", "/plugins/marketplaces", "/plugins/marketplaces/new", "/plugins/marketplaces/definitions/new"} {
				if got := get(path, "", false); got.Code != http.StatusUnauthorized {
					t.Fatalf("anonymous %s=%d", path, got.Code)
				}
				if got := get(path, operator.access, false); got.Code != http.StatusForbidden {
					t.Fatalf("operator %s=%d", path, got.Code)
				}
				got := get(path, admin.access, false)
				if got.Code != http.StatusOK {
					t.Fatalf("admin %s=%d", path, got.Code)
				}
				if !strings.Contains(got.Header().Get("Cache-Control"), "no-store") {
					t.Fatalf("%s missing no-store", path)
				}
			}
			if got := get("/overview", operator.access, false); strings.Contains(got.Body.String(), `href="`+base+`/plugins"`) {
				t.Fatal("operator received Plugins navigation")
			}

			fixture.readErr = errors.New("private-read-error-canary")
			for _, path := range []string{"/plugins", "/plugins/marketplaces", "/plugins/marketplaces/definitions/new"} {
				for _, fragment := range []bool{false, true} {
					got := get(path, admin.access, fragment)
					body := got.Body.String()
					if got.Code != http.StatusServiceUnavailable || strings.Contains(body, "No installed plugins.") || strings.Contains(body, "No marketplaces yet.") || strings.Contains(body, "private-read-error-canary") {
						t.Fatalf("read failure %s status=%d false empty/private error", path, got.Code)
					}
				}
			}
			fixture.readErr = nil
			definition := get("/plugins/marketplaces/definitions/new", admin.access, true)
			if definition.Code != http.StatusOK || strings.Contains(definition.Body.String(), "<!DOCTYPE") || strings.Count(definition.Body.String(), "<main") != 1 {
				t.Fatal("definition new did not render one intended fragment")
			}
			mutation := func(path string, form url.Values, access, csrf string, fragment bool) *httptest.ResponseRecorder {
				form.Set("csrf_token", csrf)
				return performAccessMutation(t, handler, config, base+path, form, access, csrf, fragment)
			}
			got := mutation("/plugins/marketplaces/definitions/new/refresh", url.Values{"expected_state": {"current-state"}}, admin.access, admin.csrf, false)
			if got.Code != http.StatusSeeOther || len(fixture.refreshes) != 1 || fixture.refreshes[0].Name != marketplaceSecurityIdentity || fixture.refreshes[0].Authority.SessionID == "" {
				t.Fatalf("valid definition refresh=%d calls=%+v", got.Code, fixture.refreshes)
			}

			sparseResponse := mutation("/plugins/marketplaces", url.Values{"name": {"2.Team"}, "source": {"https://example.test/plugins.git"}, "sparse": {"plugins/team tools\r\n\r\n .agents/plugins \n"}}, admin.access, admin.csrf, false)
			if sparseResponse.Code != http.StatusSeeOther || sparseResponse.Header().Get("Location") != base+"/plugins/marketplaces/definitions/2.Team" || len(fixture.adds) != 1 || len(fixture.adds[0].Sparse) != 2 || fixture.adds[0].Sparse[0] != "plugins/team tools" || fixture.adds[0].Sparse[1] != ".agents/plugins" {
				t.Fatalf("newline sparse parsing status=%d adds=%+v", sparseResponse.Code, fixture.adds)
			}
			for _, failure := range []struct {
				err    error
				status int
			}{{plugincmd.ErrInvalid, 400}, {plugincmd.ErrForbidden, 403}, {plugincmd.ErrNotFound, 404}, {plugincmd.ErrConflict, 409}, {errors.New("private-error-canary " + marketplaceSecuritySource), 503}} {
				fixture.err = failure.err
				for _, fragment := range []bool{false, true} {
					got := mutation("/plugins/marketplaces", url.Values{"name": {marketplaceSecurityAddName}, "source": {marketplaceSecuritySource}, "ref": {"main"}, "sparse": {"plugins/demo"}}, admin.access, admin.csrf, fragment)
					body := got.Body.String()
					if got.Code != failure.status || strings.Contains(body, marketplaceSecuritySource) || strings.Contains(body, "private-source-canary") || strings.Contains(body, "private-query-canary") || strings.Contains(body, "private-error-canary") {
						t.Fatalf("safe error status=%d want=%d", got.Code, failure.status)
					}
					if strings.Count(body, "<main") != 1 {
						t.Fatalf("error status%d fragment%v main count=%d", got.Code, fragment, strings.Count(body, "<main"))
					}
				}
			}
			calls := len(fixture.adds)
			if got := mutation("/plugins/marketplaces", url.Values{"name": {marketplaceSecurityAddName}, "source": {marketplaceSecuritySource}}, operator.access, operator.csrf, false); got.Code != http.StatusForbidden || len(fixture.adds) != calls {
				t.Fatal("operator reached marketplace owner")
			}
			if got := mutation("/plugins/marketplaces", url.Values{"name": {marketplaceSecurityAddName}, "source": {marketplaceSecuritySource}}, admin.access, "invalid-csrf", false); got.Code != http.StatusForbidden || len(fixture.adds) != calls {
				t.Fatal("invalid CSRF reached marketplace owner")
			}
			fixture.err = nil
			if got := mutation("/plugins/marketplaces/definitions/new/remove", url.Values{"expected_state": {"current-state"}, "confirm": {"yes"}}, admin.access, admin.csrf, false); got.Code != http.StatusSeeOther || len(fixture.removes) != 1 || !fixture.removes[0].Confirm || fixture.removes[0].Name != marketplaceSecurityIdentity {
				t.Fatalf("definition remove=%d calls=%+v", got.Code, fixture.removes)
			}
		})
	}
}
