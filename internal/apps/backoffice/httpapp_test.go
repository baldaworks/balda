package backoffice

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
)

func TestHTTPAppQAIsOptInAndUsesProductionTemplates(t *testing.T) {
	t.Parallel()
	provider, config := newHTTPAppTestState(t)
	for _, enabled := range []bool{false, true} {
		config.Server.QAUI = enabled
		app, err := newHTTPApp(provider.Users(), config)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := app.handler()
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/qa/ui/overview", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if !enabled && response.Code != http.StatusNotFound {
			t.Fatalf("disabled QA status = %d", response.Code)
		}
		if enabled && (response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<main id="main-content"`) || response.Header().Get("X-Robots-Tag") == "") {
			t.Fatalf("enabled QA response = %d %v %q", response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestHTTPAppQAAccessFixturePrecedesRuntimeBinding(t *testing.T) {
	t.Parallel()
	provider, config := newHTTPAppTestState(t)
	config.Server.QAUI = true
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/qa/ui/access", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Temporary credential") ||
		!strings.Contains(response.Body.String(), "telegram:42") || !strings.Contains(response.Body.String(), "Browser sessions") {
		t.Fatalf("access QA fixture = %d %q", response.Code, response.Body.String())
	}
}

func TestHTTPAppLoginOverviewAndRefreshContinuation(t *testing.T) {
	t.Parallel()
	provider, config := newHTTPAppTestState(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	hash, err := userpassword.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	user := usercmd.User{ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin", Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Primary: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	audit := usercmd.AuditEvent{ID: "create-admin", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "http-test", OccurredAt: now}
	if err := provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, audit); err != nil {
		t.Fatal(err)
	}
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/login", nil))
	cookies := loginPage.Result().Cookies()
	csrf := cookieValue(cookies, security.CSRFCookieName)
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}, "csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", config.Server.PublicURL)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, request)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/overview" {
		t.Fatalf("login = %d %v %q", login.Code, login.Header(), login.Body.String())
	}
	access := cookieValue(login.Result().Cookies(), security.AccessCookieName)
	overviewRequest := httptest.NewRequest(http.MethodGet, "/overview", nil)
	overviewRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
	overview := httptest.NewRecorder()
	handler.ServeHTTP(overview, overviewRequest)
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), "Overview") {
		t.Fatalf("overview = %d %q", overview.Code, overview.Body.String())
	}
	expiredRequest := httptest.NewRequest(http.MethodGet, "/overview", nil)
	expiredRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: "invalid.access"})
	expired := httptest.NewRecorder()
	handler.ServeHTTP(expired, expiredRequest)
	if expired.Code != http.StatusUnauthorized || !strings.Contains(expired.Body.String(), `action="/auth/session/refresh"`) {
		t.Fatalf("refresh continuation = %d %q", expired.Code, expired.Body.String())
	}
}

func TestHTTPAppAccessAdministrationNoJSHTMXAndRoleBoundary(t *testing.T) {
	t.Parallel()
	provider, config := newHTTPAppTestState(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Primary:    true, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "operator", DisplayName: "Operator", Username: "operator", NormalizedUsername: "operator",
		Status: usercmd.StatusActive, Role: usercmd.RoleOperator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Version:    1, CreatedAt: now, UpdatedAt: now,
	})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	adminAccess, adminCSRF := loginHTTPApp(t, handler, config, "admin")

	listRequest := httptest.NewRequest(http.MethodGet, "/access", nil)
	listRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: adminAccess})
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, listRequest)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Create user") || !strings.Contains(list.Body.String(), "Operator") {
		t.Fatalf("access list = %d %q", list.Code, list.Body.String())
	}

	createForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {"Second Operator"}, "username": {"second"},
		"role": {"operator"}, "status": {"active"}, "temporary_password": {"temporary password"},
	}
	created := performAccessMutation(t, handler, config, "/access/users", createForm, adminAccess, adminCSRF, false)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/access" {
		t.Fatalf("native create = %d %v %q", created.Code, created.Header(), created.Body.String())
	}
	createdUser, found, err := provider.Users().GetUserByNormalizedUsername(t.Context(), "second")
	if err != nil || !found || createdUser.Credential.State != usercmd.CredentialStateTemporary {
		t.Fatalf("created user = %+v, found %t, error %v", createdUser, found, err)
	}

	htmxForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {"Third Operator"}, "username": {"third"},
		"role": {"operator"}, "status": {"active"}, "temporary_password": {"temporary password"},
	}
	htmx := performAccessMutation(t, handler, config, "/access/users", htmxForm, adminAccess, adminCSRF, true)
	if htmx.Code != http.StatusNoContent || htmx.Header().Get("HX-Location") != "/access" {
		t.Fatalf("HTMX create = %d %v %q", htmx.Code, htmx.Header(), htmx.Body.String())
	}

	conflictForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {createdUser.DisplayName}, "username": {createdUser.Username},
		"role": {string(createdUser.Role)}, "status": {string(createdUser.Status)}, "expected_version": {"99"},
	}
	conflict := performAccessMutation(t, handler, config, "/access/users/"+createdUser.ID, conflictForm, adminAccess, adminCSRF, true)
	if conflict.Code != http.StatusConflict || strings.Contains(conflict.Body.String(), "<!doctype") || strings.Count(conflict.Body.String(), `id="main-content"`) != 1 {
		t.Fatalf("conflict response = %d %q", conflict.Code, conflict.Body.String())
	}

	operatorAccess, operatorCSRF := loginHTTPApp(t, handler, config, "operator")
	deniedRequest := httptest.NewRequest(http.MethodGet, "/access", nil)
	deniedRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: operatorAccess})
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("operator access status = %d", denied.Code)
	}
	operatorForm := url.Values{
		"csrf_token": {operatorCSRF}, "display_name": {"Denied User"}, "username": {"denied"},
		"role": {"operator"}, "status": {"active"}, "temporary_password": {"temporary password"},
	}
	deniedMutation := performAccessMutation(t, handler, config, "/access/users", operatorForm, operatorAccess, operatorCSRF, false)
	if deniedMutation.Code != http.StatusForbidden {
		t.Fatalf("operator mutation status = %d", deniedMutation.Code)
	}
	if _, found, err := provider.Users().GetUserByNormalizedUsername(t.Context(), "denied"); err != nil || found {
		t.Fatalf("denied user found = %t, error %v", found, err)
	}
}

func loginHTTPApp(t *testing.T, handler http.Handler, config ResolvedConfig, username string) (string, string) {
	t.Helper()
	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/login", nil))
	csrf := cookieValue(loginPage.Result().Cookies(), security.CSRFCookieName)
	form := url.Values{"username": {username}, "password": {"correct horse battery staple"}, "csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", config.Server.PublicURL)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login %q = %d %q", username, response.Code, response.Body.String())
	}
	return cookieValue(response.Result().Cookies(), security.AccessCookieName), cookieValue(response.Result().Cookies(), security.CSRFCookieName)
}

func performAccessMutation(t *testing.T, handler http.Handler, config ResolvedConfig, path string, form url.Values, access, csrf string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", config.Server.PublicURL)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if htmx {
		request.Header.Set("HX-Request", "true")
		request.Header.Set("HX-Target", "main-content")
	}
	request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
	request.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func newHTTPAppTestState(t *testing.T) (state.Provider, ResolvedConfig) {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider, ResolvedConfig{Server: ResolvedServerConfig{PublicURL: "http://backoffice.example", AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour}}
}

func cookieValue(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
