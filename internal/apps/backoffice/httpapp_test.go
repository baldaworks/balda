package backoffice

import (
	"errors"
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

func TestHTTPAppQAWorkspaceFixturesPrecedeRuntimeBinding(t *testing.T) {
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
	tests := []struct {
		name string
		want []string
	}{
		{name: "access", want: []string{"Temporary credential", "telegram:42", "Browser sessions"}},
		{name: "account", want: []string{"Rotate password", "telegram:42", "Revoke family"}},
		{name: "audit", want: []string{"session.refresh.succeeded", "session.refresh.replay", "11111111-1111-4111-8111-111111111111"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/qa/ui/"+tt.name, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("QA fixture status = %d", response.Code)
			}
			for _, want := range tt.want {
				if !strings.Contains(response.Body.String(), want) {
					t.Fatalf("QA fixture missing %q: %q", want, response.Body.String())
				}
			}
		})
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

func TestHTTPAppAccountRotationAndCurrentFamilyRevocation(t *testing.T) {
	t.Parallel()
	provider, config := newHTTPAppTestState(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
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
	initial := loginHTTPAppSession(t, handler, config, "operator")
	accountRequest := httptest.NewRequest(http.MethodGet, "/account", nil)
	accountRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: initial.access})
	accountRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: initial.csrf})
	account := httptest.NewRecorder()
	handler.ServeHTTP(account, accountRequest)
	if account.Code != http.StatusOK || !strings.Contains(account.Body.String(), "Rotate password") || strings.Contains(account.Body.String(), "Access</span>") {
		t.Fatalf("account page = %d %q", account.Code, account.Body.String())
	}

	rotateForm := url.Values{
		"csrf_token": {initial.csrf}, "current_password": {"correct horse battery staple"},
		"new_password": {"replacement password"},
	}
	wrongRotateForm := url.Values{
		"csrf_token": {initial.csrf}, "current_password": {"incorrect password"},
		"new_password": {"replacement password"},
	}
	wrongRotation := performAccessMutation(t, handler, config, "/account/password", wrongRotateForm, initial.access, initial.csrf, true)
	if wrongRotation.Code != http.StatusUnauthorized || strings.Contains(wrongRotation.Body.String(), "<!doctype") || strings.Count(wrongRotation.Body.String(), `id="main-content"`) != 1 {
		t.Fatalf("wrong password rotation = %d %q", wrongRotation.Code, wrongRotation.Body.String())
	}
	rotated := performAccessMutation(t, handler, config, "/account/password", rotateForm, initial.access, initial.csrf, true)
	if rotated.Code != http.StatusNoContent || rotated.Header().Get("HX-Location") != "/account" {
		t.Fatalf("password rotation = %d %v %q", rotated.Code, rotated.Header(), rotated.Body.String())
	}
	next := httpLoginCookies{
		access:  cookieValue(rotated.Result().Cookies(), security.AccessCookieName),
		refresh: cookieValue(rotated.Result().Cookies(), security.RefreshCookieName),
		csrf:    cookieValue(rotated.Result().Cookies(), security.CSRFCookieName),
	}
	if next.access == "" || next.refresh == "" || next.csrf == "" {
		t.Fatalf("rotation cookies = %+v", rotated.Result().Cookies())
	}
	if _, err := app.security.ValidateAccess(t.Context(), initial.access); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("old access validation error = %v", err)
	}
	if _, err := app.security.Refresh(t.Context(), initial.refresh, initial.csrf); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("old refresh validation error = %v", err)
	}
	principal, err := app.security.ValidateAccess(t.Context(), next.access)
	if err != nil {
		t.Fatal(err)
	}

	unconfirmedForm := url.Values{"csrf_token": {next.csrf}}
	unconfirmed := performAccessMutation(t, handler, config, "/account/sessions/"+principal.FamilyID+"/revoke", unconfirmedForm, next.access, next.csrf, true)
	if unconfirmed.Code != http.StatusBadRequest || strings.Contains(unconfirmed.Body.String(), "<!doctype") {
		t.Fatalf("unconfirmed current family revocation = %d %q", unconfirmed.Code, unconfirmed.Body.String())
	}
	revokeForm := url.Values{"csrf_token": {next.csrf}, "confirm_current": {"yes"}}
	revoked := performAccessMutation(t, handler, config, "/account/sessions/"+principal.FamilyID+"/revoke", revokeForm, next.access, next.csrf, false)
	if revoked.Code != http.StatusSeeOther || revoked.Header().Get("Location") != "/login" {
		t.Fatalf("current family revocation = %d %v %q", revoked.Code, revoked.Header(), revoked.Body.String())
	}
	if _, err := app.security.ValidateAccess(t.Context(), next.access); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("revoked access validation error = %v", err)
	}
	if _, err := app.security.Refresh(t.Context(), next.refresh, next.csrf); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("revoked refresh validation error = %v", err)
	}
}

func TestHTTPAppAuditFilteringPaginationRedactionAndRoleBoundary(t *testing.T) {
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
	admin := loginHTTPAppSession(t, handler, config, "admin")
	operator := loginHTTPAppSession(t, handler, config, "operator")

	auditRequest := httptest.NewRequest(http.MethodGet, "/audit?action=session.login.succeeded&outcome=succeeded&target=session&limit=1", nil)
	auditRequest.Header.Set("HX-Request", "true")
	auditRequest.Header.Set("HX-Target", "main-content")
	auditRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, auditRequest)
	body := auditResponse.Body.String()
	if auditResponse.Code != http.StatusOK || strings.Contains(body, "<!doctype") || strings.Count(body, `id="main-content"`) != 1 ||
		!strings.Contains(body, "session.login.succeeded") || !strings.Contains(body, "Next page") {
		t.Fatalf("filtered audit = %d %q", auditResponse.Code, body)
	}
	for _, forbidden := range []string{"correct horse battery staple", admin.access, admin.refresh, admin.csrf} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("audit leaked browser secret %q", forbidden)
		}
	}

	invalidRequest := httptest.NewRequest(http.MethodGet, "/audit?action=product.event", nil)
	invalidRequest.Header.Set("HX-Request", "true")
	invalidRequest.Header.Set("HX-Target", "main-content")
	invalidRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest || strings.Contains(invalid.Body.String(), "<!doctype") {
		t.Fatalf("invalid audit filter = %d %q", invalid.Code, invalid.Body.String())
	}

	deniedRequest := httptest.NewRequest(http.MethodGet, "/audit", nil)
	deniedRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: operator.access})
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("operator audit status = %d", denied.Code)
	}
}

type httpLoginCookies struct {
	access  string
	refresh string
	csrf    string
}

func loginHTTPApp(t *testing.T, handler http.Handler, config ResolvedConfig, username string) (string, string) {
	t.Helper()
	session := loginHTTPAppSession(t, handler, config, username)
	return session.access, session.csrf
}

func loginHTTPAppSession(t *testing.T, handler http.Handler, config ResolvedConfig, username string) httpLoginCookies {
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
	return httpLoginCookies{
		access:  cookieValue(response.Result().Cookies(), security.AccessCookieName),
		refresh: cookieValue(response.Result().Cookies(), security.RefreshCookieName),
		csrf:    cookieValue(response.Result().Cookies(), security.CSRFCookieName),
	}
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
