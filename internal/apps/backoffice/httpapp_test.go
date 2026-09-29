package backoffice

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
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

func TestHTTPAppRefreshRecoveryChoices(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		status int
		want   string
		reject string
	}{
		{http.StatusUnauthorized, "Sign in again", "Restore session"},
		{http.StatusConflict, "Reopen page", "Restore session"},
	} {
		request := httptest.NewRequest(http.MethodPost, security.RefreshPath, strings.NewReader("return_to=%2Faccount"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		app.renderSecurityError(response, request, check.status)
		if response.Code != check.status || !strings.Contains(response.Body.String(), check.want) || strings.Contains(response.Body.String(), check.reject) {
			t.Fatalf("refresh recovery status %d = %d %q", check.status, response.Code, response.Body.String())
		}
	}
}

func TestQAHandlerSessionAndErrorStates(t *testing.T) {
	t.Parallel()
	handler, err := QAHandler("")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path   string
		status int
		want   string
	}{
		{"/qa/ui/account-many", http.StatusOK, "Older active sessions"},
		{"/qa/ui/account-many-next", http.StatusOK, "End session"},
		{"/qa/ui/account-session-states", http.StatusOK, "Expired"},
		{"/qa/ui/access-primary", http.StatusOK, "Primary administrator"},
		{"/qa/ui/access-long", http.StatusOK, "Long synthetic"},
		{"/qa/ui/form-bad-request", http.StatusBadRequest, "Review the input"},
		{"/qa/ui/form-forbidden", http.StatusForbidden, "Permission denied"},
		{"/qa/ui/form-conflict", http.StatusConflict, "changed while you were editing"},
		{"/qa/ui/form-server-error", http.StatusInternalServerError, "Try again later"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, check.path, nil))
		if response.Code != check.status || !strings.Contains(response.Body.String(), check.want) {
			t.Errorf("GET %s = %d, missing %q", check.path, response.Code, check.want)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/qa/ui/account-session-states", nil))
	if !strings.Contains(response.Body.String(), "Synthetic active, ended, and expired examples") {
		t.Fatalf("session state preview lacks fixture context: %q", response.Body.String())
	}
	if strings.Count(response.Body.String(), "End session</button>") != 2 {
		t.Fatalf("active-only revoke controls in session state preview = %d", strings.Count(response.Body.String(), "End session</button>"))
	}
	fragmentRequest := httptest.NewRequest(http.MethodGet, "/qa/ui/form-bad-request", nil)
	fragmentRequest.Header.Set("HX-Request", "true")
	fragmentRequest.Header.Set("HX-Target", "main-content")
	fragment := httptest.NewRecorder()
	handler.ServeHTTP(fragment, fragmentRequest)
	if fragment.Code != http.StatusBadRequest || strings.Contains(fragment.Body.String(), "<!doctype") || strings.Count(fragment.Body.String(), `id="main-content"`) != 1 {
		t.Fatalf("QA error fragment = %d %q", fragment.Code, fragment.Body.String())
	}
}

func TestQAHandlerGalleryAndReadOnlyRoutes(t *testing.T) {
	t.Parallel()
	handler, err := QAHandler("/balda")
	if err != nil {
		t.Fatal(err)
	}
	gallery := httptest.NewRecorder()
	handler.ServeHTTP(gallery, httptest.NewRequest(http.MethodGet, "/balda/qa/ui/", nil))
	if gallery.Code != http.StatusOK || !strings.Contains(gallery.Body.String(), "Backoffice UI previews") {
		t.Fatalf("gallery response = %d %q", gallery.Code, gallery.Body.String())
	}
	if got := gallery.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("gallery Cache-Control = %q", got)
	}
	if got := gallery.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("gallery X-Robots-Tag = %q", got)
	}
	for _, entry := range qaEntries {
		path := "/balda/qa/ui/" + entry.name
		wantStatus := entry.status
		if wantStatus == 0 {
			wantStatus = http.StatusOK
		}
		if entry.gallery && !strings.Contains(gallery.Body.String(), `href="`+path+`"`) {
			t.Errorf("gallery has no link to %s", path)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != wantStatus || strings.Count(response.Body.String(), `id="main-content"`) != 1 {
			t.Errorf("GET %s = %d, main count = %d", path, response.Code, strings.Count(response.Body.String(), `id="main-content"`))
		}
		if strings.Contains(response.Body.String(), `action="/balda/access`) || strings.Contains(response.Body.String(), `action="/balda/account`) {
			t.Errorf("GET %s has an operational form action", path)
		}
		head := httptest.NewRecorder()
		handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))
		if head.Code != wantStatus || head.Body.Len() != 0 {
			t.Errorf("HEAD %s = %d with %d body bytes", path, head.Code, head.Body.Len())
		}
		post := httptest.NewRecorder()
		handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, path, nil))
		if post.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, post.Code)
		}
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, appAssetPath(t, gallery.Body.String(), "css"), nil))
	if asset.Code != http.StatusOK {
		t.Errorf("asset status = %d", asset.Code)
	}
	runtime := httptest.NewRecorder()
	handler.ServeHTTP(runtime, httptest.NewRequest(http.MethodGet, "/balda/access", nil))
	if runtime.Code != http.StatusNotFound {
		t.Errorf("runtime route status = %d, want 404", runtime.Code)
	}
}

func TestHTTPAppServesConfiguredBasePath(t *testing.T) {
	t.Parallel()
	provider, config := newHTTPAppTestState(t)
	config.Server.BasePath = "/balda"
	config.Server.PublicURL = "https://lab.metalagman.dev"
	config.Server.SecureCookies = true
	config.Server.QAUI = true
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/", "/login", "/overview", "/balda/unknown"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", route, response.Code)
		}
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/balda/login", nil))
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `action="/balda/login"`) {
		t.Fatalf("prefixed login = %d %q", login.Code, login.Body.String())
	}
	assetPath := appAssetPath(t, login.Body.String(), "js")
	if got := login.Result().Cookies()[0].Path; got != "/balda/" {
		t.Errorf("CSRF cookie path = %q", got)
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, assetPath, nil))
	if asset.Code != http.StatusOK {
		t.Errorf("prefixed asset = %d", asset.Code)
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/balda/healthz", nil))
	if health.Code != http.StatusOK {
		t.Errorf("prefixed health = %d", health.Code)
	}
	qa := httptest.NewRecorder()
	handler.ServeHTTP(qa, httptest.NewRequest(http.MethodGet, "/balda/qa/ui/access", nil))
	if qa.Code != http.StatusOK || !strings.Contains(qa.Body.String(), `action="/balda/qa/ui/access/users`) || !strings.Contains(qa.Body.String(), `href="/balda/qa/ui/overview"`) {
		t.Fatalf("prefixed QA = %d %q", qa.Code, qa.Body.String())
	}
}

func appAssetPath(t *testing.T, document, extension string) string {
	t.Helper()
	expression := regexp.MustCompile(`(?:href|src)="([^"]*/assets/app\.[0-9a-f]{16}\.` + extension + `)"`)
	match := expression.FindStringSubmatch(document)
	if len(match) != 2 {
		t.Fatalf("versioned app.%s URL missing from document", extension)
	}
	return match[1]
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
		{name: "access", want: []string{"Credential", "Telegram", "42", "Active browser sessions"}},
		{name: "account", want: []string{"Change password", "telegram:42", "End session"}},
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
	if expired.Code != http.StatusUnauthorized || !strings.Contains(expired.Body.String(), `action="/auth/session/refresh"`) || !strings.Contains(expired.Body.String(), `data-auto-refresh="true"`) {
		t.Fatalf("refresh continuation = %d %q", expired.Code, expired.Body.String())
	}
	refreshCSRF := cookieValue(expired.Result().Cookies(), security.CSRFCookieName)
	if refreshCSRF == "" || !strings.Contains(expired.Body.String(), `name="csrf_token" value="`+refreshCSRF+`"`) {
		t.Fatalf("refresh continuation lacks usable CSRF token: %q", expired.Body.String())
	}
	refreshForm := url.Values{"csrf_token": {refreshCSRF}, "return_to": {"/overview"}}
	refreshRequest := httptest.NewRequest(http.MethodPost, security.RefreshPath, strings.NewReader(refreshForm.Encode()))
	refreshRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	refreshRequest.Header.Set("Origin", config.Server.PublicURL)
	refreshRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: refreshCSRF})
	terminal := httptest.NewRecorder()
	handler.ServeHTTP(terminal, refreshRequest)
	if terminal.Code != http.StatusUnauthorized || !strings.Contains(terminal.Body.String(), "This session cannot be restored. Sign in again to continue.") || strings.Contains(terminal.Body.String(), `data-auto-refresh="true"`) {
		t.Fatalf("missing-refresh recovery = %d %q", terminal.Code, terminal.Body.String())
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
	filteredRequest := httptest.NewRequest(http.MethodGet, "/access?q=opera&status=active", nil)
	filteredRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: adminAccess})
	filtered := httptest.NewRecorder()
	handler.ServeHTTP(filtered, filteredRequest)
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), ">Operator</a>") || strings.Contains(filtered.Body.String(), ">Admin</a>") {
		t.Fatalf("filtered access users = %d", filtered.Code)
	}
	createRequest := httptest.NewRequest(http.MethodGet, "/access/new", nil)
	createRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: adminAccess})
	createPage := httptest.NewRecorder()
	handler.ServeHTTP(createPage, createRequest)
	if createPage.Code != http.StatusOK || !strings.Contains(createPage.Body.String(), `name="temporary_password"`) || strings.Contains(createPage.Body.String(), `id="users-heading"`) {
		t.Fatalf("separate create page = %d", createPage.Code)
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
	deniedCreate := httptest.NewRecorder()
	deniedCreateRequest := httptest.NewRequest(http.MethodGet, "/access/new", nil)
	deniedCreateRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: operatorAccess})
	handler.ServeHTTP(deniedCreate, deniedCreateRequest)
	if deniedCreate.Code != http.StatusForbidden {
		t.Fatalf("operator create page status = %d", deniedCreate.Code)
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

func TestHTTPAppConfiguredBindingAdministration(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	config.Balda.Telegram.Enabled = true
	config.Balda.Slack.Agent.Enabled = true
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	for _, user := range []usercmd.User{
		{ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin", Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Primary: true, Version: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "operator", DisplayName: "Operator", Username: "operator", NormalizedUsername: "operator", Status: usercmd.StatusActive, Role: usercmd.RoleOperator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now},
	} {
		createAccessTestUser(t, provider.Users(), user)
	}
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	adminAccess, adminCSRF := loginHTTPApp(t, handler, config, "admin")
	detailRequest := httptest.NewRequest(http.MethodGet, "/access/users/operator", nil)
	detailRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: adminAccess})
	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, detailRequest)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `value="slackagent"`) || strings.Contains(detail.Body.String(), `value="zulip"`) {
		t.Fatalf("configured binding choices = %d %q", detail.Code, detail.Body.String())
	}
	add := func(channel, principal, version string, htmx bool) *httptest.ResponseRecorder {
		return performAccessMutation(t, handler, config, "/access/users/operator/bindings", url.Values{
			"csrf_token": {adminCSRF}, "channel_type": {channel}, "principal": {principal}, "expected_version": {version},
		}, adminAccess, adminCSRF, htmx)
	}
	if got := add("zulip", "202", "1", false); got.Code != http.StatusBadRequest {
		t.Fatalf("disabled channel status = %d", got.Code)
	}
	if got := add("telegram", "202", "1", false); got.Code != http.StatusSeeOther || got.Header().Get("Location") != "/access/users/operator" {
		t.Fatalf("native binding add = %d %v", got.Code, got.Header())
	}
	if got := add("slackagent", "T1:U1", "2", true); got.Code != http.StatusNoContent || got.Header().Get("HX-Location") != "/access/users/operator" {
		t.Fatalf("HTMX binding add = %d %v", got.Code, got.Header())
	}
	if got := add("telegram", "202", "3", false); got.Code != http.StatusConflict {
		t.Fatalf("duplicate principal status = %d", got.Code)
	}
	badCSRF := performAccessMutation(t, handler, config, "/access/users/operator/bindings", url.Values{
		"csrf_token": {"invalid"}, "channel_type": {"telegram"}, "principal": {"303"}, "expected_version": {"3"},
	}, adminAccess, adminCSRF, false)
	if badCSRF.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF status = %d", badCSRF.Code)
	}
	user, found, err := provider.Users().GetUser(t.Context(), "operator")
	if err != nil || !found || len(user.Bindings) != 2 {
		t.Fatalf("operator bindings = %+v, found=%t, err=%v", user.Bindings, found, err)
	}
	if got := add("telegram", "303", "2", false); got.Code != http.StatusConflict {
		t.Fatalf("stale add status = %d", got.Code)
	}
	removePath := "/access/users/operator/bindings/" + user.Bindings[0].ID + "/delete"
	removeForm := url.Values{"csrf_token": {adminCSRF}, "expected_version": {"3"}, "confirm_bot_impact": {"yes"}}
	if got := performAccessMutation(t, handler, config, removePath, url.Values{
		"csrf_token": {adminCSRF}, "expected_version": {"3"},
	}, adminAccess, adminCSRF, false); got.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed removal status = %d", got.Code)
	}
	if got := performAccessMutation(t, handler, config, removePath, removeForm, adminAccess, adminCSRF, false); got.Code != http.StatusSeeOther {
		t.Fatalf("remove binding status = %d", got.Code)
	}
	user, _, err = provider.Users().GetUser(t.Context(), "operator")
	if err != nil || len(user.Bindings) != 1 {
		t.Fatalf("remaining bindings = %+v, err=%v", user.Bindings, err)
	}
	operatorAccess, operatorCSRF := loginHTTPApp(t, handler, config, "operator")
	denied := performAccessMutation(t, handler, config, "/access/users/operator/bindings", url.Values{
		"csrf_token": {operatorCSRF}, "channel_type": {"telegram"}, "principal": {"303"}, "expected_version": {"4"},
	}, operatorAccess, operatorCSRF, false)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("operator binding add status = %d", denied.Code)
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
	if account.Code != http.StatusOK || !strings.Contains(account.Body.String(), "Change password") || strings.Contains(account.Body.String(), "Access</span>") {
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
	nativeChange := performAccessMutation(t, handler, config, "/account/password", url.Values{
		"csrf_token": {next.csrf}, "current_password": {"replacement password"},
		"new_password": {"final replacement password"},
	}, next.access, next.csrf, false)
	if nativeChange.Code != http.StatusSeeOther || nativeChange.Header().Get("Location") != "/account" {
		t.Fatalf("native password change = %d %v", nativeChange.Code, nativeChange.Header())
	}
	if _, err := app.security.ValidateAccess(t.Context(), next.access); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("previous access after native change = %v", err)
	}
	next = httpLoginCookies{
		access:  cookieValue(nativeChange.Result().Cookies(), security.AccessCookieName),
		refresh: cookieValue(nativeChange.Result().Cookies(), security.RefreshCookieName),
		csrf:    cookieValue(nativeChange.Result().Cookies(), security.CSRFCookieName),
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
	revokedPageRequest := httptest.NewRequest(http.MethodGet, "/overview", nil)
	revokedPageRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: next.access})
	revokedPageRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: next.csrf})
	revokedPage := httptest.NewRecorder()
	handler.ServeHTTP(revokedPage, revokedPageRequest)
	if revokedPage.Code != http.StatusUnauthorized || !strings.Contains(revokedPage.Body.String(), `data-auto-refresh="true"`) {
		t.Fatalf("revoked session continuation = %d %q", revokedPage.Code, revokedPage.Body.String())
	}
	recoveryForm := url.Values{"csrf_token": {next.csrf}, "return_to": {"/overview"}}
	recoveryRequest := httptest.NewRequest(http.MethodPost, security.RefreshPath, strings.NewReader(recoveryForm.Encode()))
	recoveryRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recoveryRequest.Header.Set("Origin", config.Server.PublicURL)
	recoveryRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: next.csrf})
	recoveryRequest.AddCookie(&http.Cookie{Name: security.RefreshCookieName, Value: next.refresh})
	recovery := httptest.NewRecorder()
	handler.ServeHTTP(recovery, recoveryRequest)
	if recovery.Code != http.StatusUnauthorized || !strings.Contains(recovery.Body.String(), "This session cannot be restored. Sign in again to continue.") || strings.Contains(recovery.Body.String(), `data-auto-refresh="true"`) {
		t.Fatalf("revoked session recovery = %d %q", recovery.Code, recovery.Body.String())
	}
}

func TestHTTPAppAccountActiveSessionPagination(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC().Add(-time.Hour)
	createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Primary:    true, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	oldest := loginHTTPAppSession(t, handler, config, "admin")
	var otherAccess string
	for range 21 {
		credentials, err := app.security.Login(t.Context(), "admin", []byte("correct horse battery staple"))
		if err != nil {
			t.Fatal(err)
		}
		otherAccess = credentials.AccessToken
	}
	account := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: oldest.access})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	first := account("/account")
	if first.Code != http.StatusOK || strings.Count(first.Body.String(), ">End session</button>") != sessionPageSize {
		t.Fatalf("first session page = %d, end actions = %d", first.Code, strings.Count(first.Body.String(), ">End session</button>"))
	}
	firstSession := strings.SplitN(first.Body.String(), `<h3 class="h5">`, 2)
	if len(firstSession) != 2 || !strings.Contains(strings.SplitN(firstSession[1], "</h3>", 2)[0], "Current") {
		t.Fatal("current session is not first")
	}
	match := regexp.MustCompile(`href="(/account\?after_session=[^"]+)"`).FindStringSubmatch(first.Body.String())
	if len(match) != 2 {
		t.Fatal("first page has no continuation")
	}
	second := account(match[1])
	if second.Code != http.StatusOK || strings.Count(second.Body.String(), ">End session</button>") != 2 || strings.Contains(second.Body.String(), "Older active sessions") {
		t.Fatalf("second session page = %d, end actions = %d", second.Code, strings.Count(second.Body.String(), ">End session</button>"))
	}
	other, err := app.security.ValidateAccess(t.Context(), otherAccess)
	if err != nil {
		t.Fatal(err)
	}
	revoked := performAccessMutation(t, handler, config, "/access/users/admin/sessions/"+other.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}}, oldest.access, oldest.csrf, false)
	if revoked.Code != http.StatusSeeOther || revoked.Header().Get("Location") != "/access/users/admin" {
		t.Fatalf("access session revocation = %d %q", revoked.Code, revoked.Header().Get("Location"))
	}
	if _, err := app.security.ValidateAccess(t.Context(), otherAccess); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("revoked access validation = %v", err)
	}
	self, err := app.security.ValidateAccess(t.Context(), oldest.access)
	if err != nil {
		t.Fatal(err)
	}
	endedCurrent := performAccessMutation(t, handler, config, "/access/users/admin/sessions/"+self.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}, "confirm_current": {"yes"}}, oldest.access, oldest.csrf, false)
	if endedCurrent.Code != http.StatusSeeOther || endedCurrent.Header().Get("Location") != "/login" {
		t.Fatalf("current access session revocation = %d %q", endedCurrent.Code, endedCurrent.Header().Get("Location"))
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
		!strings.Contains(body, "session.login.succeeded") || !strings.Contains(body, "Older events") {
		t.Fatalf("filtered audit = %d %q", auditResponse.Code, body)
	}
	for _, forbidden := range []string{"correct horse battery staple", admin.access, admin.refresh, admin.csrf} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("audit leaked browser secret %q", forbidden)
		}
	}
	if _, err := app.security.Login(t.Context(), "operator", []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	actorPath := "/audit?action=session.login.succeeded&actor=operator&from=" + day + "&to=" + day + "&limit=1"
	actorRequest := httptest.NewRequest(http.MethodGet, actorPath, nil)
	actorRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	actorResponse := httptest.NewRecorder()
	handler.ServeHTTP(actorResponse, actorRequest)
	if actorResponse.Code != http.StatusOK || !strings.Contains(actorResponse.Body.String(), "<td class=\"event-actor\">Operator</td>") ||
		!strings.Contains(actorResponse.Body.String(), "actor=operator") || !strings.Contains(actorResponse.Body.String(), "from="+day) || !strings.Contains(actorResponse.Body.String(), "to="+day) {
		t.Fatalf("actor/date audit pagination = %d %q", actorResponse.Code, actorResponse.Body.String())
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
