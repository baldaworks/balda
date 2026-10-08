package backoffice

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/aliasbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/aliases"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestAliasesBrowserManagement(t *testing.T) {
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
			app.aliases = aliasbackofficeapp.New(aliases.New(provider.Aliases()))
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "administrator")
			operator := loginHTTPAppSession(t, handler, config, "operator")
			get := func(path, access string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, base+path, nil)
				if access != "" {
					r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			if got := get("/aliases", "").Code; got != http.StatusUnauthorized {
				t.Fatalf("anonymous inventory = %d", got)
			}
			if got := get("/aliases", operator.access).Code; got != http.StatusForbidden {
				t.Fatalf("operator inventory = %d", got)
			}
			if got := get("/aliases?new=1", admin.access); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `name="locator_ref"`) {
				t.Fatalf("new editor = %d", got.Code)
			}
			mutation := func(path string, form url.Values, access, csrf string) *httptest.ResponseRecorder {
				form.Set("csrf_token", csrf)
				return performAccessMutation(t, handler, config, base+path, form, access, csrf, false)
			}
			create := url.Values{"name": {"main_chat"}, "locator_ref": {"telegram:-1003953132277:0"}}
			if got := mutation("/aliases", create, operator.access, operator.csrf).Code; got != http.StatusForbidden {
				t.Fatalf("operator create = %d", got)
			}
			if got := mutation("/aliases", create, admin.access, admin.csrf).Code; got != http.StatusSeeOther {
				t.Fatalf("create = %d", got)
			}
			detail := get("/aliases/main_chat", admin.access)
			if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `name="expected_version" value="1"`) || !strings.Contains(detail.Body.String(), "telegram:-1003953132277:0") {
				t.Fatalf("created detail = %d", detail.Code)
			}
			if got := mutation("/aliases/main_chat", url.Values{"expected_version": {"1"}, "locator_ref": {"telegram:-1003953132278:0"}}, admin.access, admin.csrf).Code; got != http.StatusSeeOther {
				t.Fatalf("retarget = %d", got)
			}
			if got := mutation("/aliases/main_chat", url.Values{"expected_version": {"1"}, "locator_ref": {"telegram:-1003953132279:0"}}, admin.access, admin.csrf).Code; got != http.StatusConflict {
				t.Fatalf("stale retarget = %d", got)
			}
			inventory := get("/aliases", admin.access)
			if inventory.Code != http.StatusOK || !strings.Contains(inventory.Body.String(), "telegram:-1003953132278:0") || !strings.Contains(inventory.Body.String(), `href="`+base+`/aliases/main_chat"`) {
				t.Fatalf("inventory = %d", inventory.Code)
			}
			if got := mutation("/aliases/main_chat/delete", url.Values{"expected_version": {"2"}, "confirm": {"yes"}}, admin.access, admin.csrf).Code; got != http.StatusSeeOther {
				t.Fatalf("delete = %d", got)
			}
			if got := get("/aliases/main_chat", admin.access).Code; got != http.StatusNotFound {
				t.Fatalf("deleted detail = %d", got)
			}
			unsafeLocator := url.Values{"name": {"log_chat"}, "locator_ref": {"slackagent:c:<script>:C"}}
			if got := mutation("/aliases", unsafeLocator, admin.access, admin.csrf).Code; got != http.StatusSeeOther {
				t.Fatalf("create display-sensitive locator = %d", got)
			}
			inventory = get("/aliases", admin.access)
			if strings.Contains(inventory.Body.String(), "<script>") || !strings.Contains(inventory.Body.String(), "&lt;script&gt;") {
				t.Fatal("alias inventory did not escape locator")
			}
		})
	}
}
