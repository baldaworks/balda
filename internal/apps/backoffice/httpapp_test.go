package backoffice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/access"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	testAccessPath  = "/access"
	testAccountPath = "/account"
	testLoginPath   = "/login"
)

const noStoreCacheControl = "no-store"

type readTrackingBody struct {
	read bool
}

func (b *readTrackingBody) Read([]byte) (int, error) {
	b.read = true
	return 0, io.EOF
}

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
	if got := gallery.Header().Get("Cache-Control"); got != noStoreCacheControl {
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
	config.Server.PublicURL = "https://balda.example.com"
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
	for _, route := range []string{"/", testLoginPath, "/overview", "/balda/unknown"} {
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
	config.Agent = AgentConfig{ProviderID: "primary", ProviderType: "codex_acp", Profile: "default", Model: "gpt-6-sol", ReasoningEffort: "high", GlobalInstruction: "operate safely"}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	hash := validPasswordHash(t)
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
	for _, route := range []string{testLoginPath, security.RefreshPath} {
		body := &readTrackingBody{}
		request := httptest.NewRequest(http.MethodPost, route, body)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", "https://attacker.example")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || body.read {
			t.Fatalf("early rejection for %s = status %d, body read %t", route, response.Code, body.read)
		}
	}
	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, testLoginPath, nil))
	cookies := loginPage.Result().Cookies()
	csrf := cookieValue(cookies, security.CSRFCookieName)
	failedForm := url.Values{"username": {"safe<operator>"}, "password": {"secret-never-render-4987"}, "csrf_token": {csrf}}
	failedRequest := httptest.NewRequest(http.MethodPost, testLoginPath, strings.NewReader(failedForm.Encode()))
	failedRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	failedRequest.Header.Set("Origin", config.Server.PublicURL)
	failedRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	failedRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: csrf})
	failed := httptest.NewRecorder()
	handler.ServeHTTP(failed, failedRequest)
	failedBody := failed.Body.String()
	if failed.Code != http.StatusUnauthorized ||
		!strings.Contains(failedBody, `value="safe&lt;operator&gt;"`) ||
		!strings.Contains(failedBody, `id="login-error"`) ||
		!strings.Contains(failedBody, `aria-describedby="login-error"`) ||
		!strings.Contains(failedBody, `aria-invalid="true"`) {
		t.Fatalf("failed login form state = %d %q", failed.Code, failedBody)
	}
	if strings.Contains(failedBody, "secret-never-render-4987") {
		t.Fatalf("failed login retained the submitted password: %q", failedBody)
	}
	form := url.Values{"username": {"admin"}, "password": {validPassword}, "csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, testLoginPath, strings.NewReader(form.Encode()))
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
	agentRequest := httptest.NewRequest(http.MethodGet, "/agent", nil)
	agentRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: access})
	agent := httptest.NewRecorder()
	handler.ServeHTTP(agent, agentRequest)
	if agent.Code != http.StatusOK || !strings.Contains(agent.Body.String(), "operate safely") || !strings.Contains(agent.Body.String(), "gpt-6-sol") {
		t.Fatalf("agent = %d %q", agent.Code, agent.Body.String())
	}
	expiredRequest := httptest.NewRequest(http.MethodGet, "/overview", nil)
	expiredRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: "invalid.access"})
	expired := httptest.NewRecorder()
	handler.ServeHTTP(expired, expiredRequest)
	if expired.Code != http.StatusUnauthorized || !strings.Contains(expired.Body.String(), `action="/auth/session/refresh"`) || !strings.Contains(expired.Body.String(), `data-auto-refresh="true"`) || !strings.Contains(expired.Body.String(), `aria-describedby="refresh-error"`) {
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

	listRequest := httptest.NewRequest(http.MethodGet, testAccessPath, nil)
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
	invalidCreateForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {"Safe <Draft>"}, "username": {"draft-user"},
		"role": {"administrator"}, "status": {"disabled"}, "temporary_password": {"secret-5"},
	}
	invalidCreate := performAccessMutation(t, handler, config, "/access/users", invalidCreateForm, adminAccess, adminCSRF, true)
	invalidCreateBody := invalidCreate.Body.String()
	if invalidCreate.Code != http.StatusBadRequest || !strings.Contains(invalidCreateBody, `value="Safe &lt;Draft&gt;"`) ||
		!strings.Contains(invalidCreateBody, `value="draft-user"`) || !strings.Contains(invalidCreateBody, `value="administrator" selected`) ||
		!strings.Contains(invalidCreateBody, `value="disabled" selected`) || !strings.Contains(invalidCreateBody, `aria-describedby="access-form-error"`) {
		t.Fatalf("invalid create state = %d %q", invalidCreate.Code, invalidCreateBody)
	}
	if strings.Contains(invalidCreateBody, "secret-5") {
		t.Fatalf("invalid create retained temporary password: %q", invalidCreateBody)
	}

	createForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {"Second Operator"}, "username": {"second"},
		"role": {"operator"}, "status": {"active"}, "temporary_password": {"temporary password"},
	}
	created := performAccessMutation(t, handler, config, "/access/users", createForm, adminAccess, adminCSRF, false)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != testAccessPath {
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
	if htmx.Code != http.StatusNoContent || testHTMXLocation(t, htmx.Header()) != testAccessPath {
		t.Fatalf("HTMX create = %d %v %q", htmx.Code, htmx.Header(), htmx.Body.String())
	}
	adminPrincipal, err := app.security.ValidateAccess(t.Context(), adminAccess)
	if err != nil {
		t.Fatal(err)
	}
	thirdUser, found, err := provider.Users().GetUserByNormalizedUsername(t.Context(), "third")
	if err != nil || !found {
		t.Fatalf("load concurrent-edit fixture: found=%t err=%v", found, err)
	}
	if _, err := app.access.UpdateUser(t.Context(), access.Actor{User: adminPrincipal.User, SessionID: adminPrincipal.FamilyID}, access.UpdateInput{
		UserID: thirdUser.ID, DisplayName: "Concurrent value", Username: thirdUser.Username,
		Role: thirdUser.Role, Status: thirdUser.Status, ExpectedVersion: thirdUser.Version,
	}); err != nil {
		t.Fatal(err)
	}
	staleDraftRequest := httptest.NewRequest(http.MethodPost, "/access/users/"+thirdUser.ID, nil)
	staleDraftRequest.SetPathValue("user_id", thirdUser.ID)
	staleDraftRequest.PostForm = url.Values{
		"display_name": {"Safe stale draft"}, "username": {thirdUser.Username}, "role": {string(thirdUser.Role)},
		"status": {string(thirdUser.Status)}, "expected_version": {"1"},
	}
	staleDraft := httptest.NewRecorder()
	app.renderAccessMutationError(staleDraft, staleDraftRequest, adminPrincipal, "access-user-update", usercmd.ErrInvalid)
	if staleDraft.Code != http.StatusBadRequest || !strings.Contains(staleDraft.Body.String(), `name="expected_version" value="1"`) || strings.Contains(staleDraft.Body.String(), `name="expected_version" value="2"`) {
		t.Fatalf("stale draft lost its optimistic version: %d %q", staleDraft.Code, staleDraft.Body.String())
	}

	conflictForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {createdUser.DisplayName}, "username": {createdUser.Username},
		"role": {string(createdUser.Role)}, "status": {string(createdUser.Status)}, "expected_version": {"99"},
	}
	conflict := performAccessMutation(t, handler, config, "/access/users/"+createdUser.ID, conflictForm, adminAccess, adminCSRF, true)
	if conflict.Code != http.StatusConflict || strings.Contains(conflict.Body.String(), "<!doctype") || strings.Count(conflict.Body.String(), `id="main-content"`) != 1 {
		t.Fatalf("conflict response = %d %q", conflict.Code, conflict.Body.String())
	}
	invalidUpdateForm := url.Values{
		"csrf_token": {adminCSRF}, "display_name": {"Retained <Name>"}, "username": {"retained-user"},
		"role": {"invalid"}, "status": {string(createdUser.Status)}, "expected_version": {"1"},
	}
	invalidUpdate := performAccessMutation(t, handler, config, "/access/users/"+createdUser.ID, invalidUpdateForm, adminAccess, adminCSRF, true)
	if invalidUpdate.Code != http.StatusBadRequest || !strings.Contains(invalidUpdate.Body.String(), `value="Retained &lt;Name&gt;"`) || !strings.Contains(invalidUpdate.Body.String(), `value="retained-user"`) || !strings.Contains(invalidUpdate.Body.String(), `aria-describedby="access-form-error"`) {
		t.Fatalf("invalid update state = %d %q", invalidUpdate.Code, invalidUpdate.Body.String())
	}
	detailRequest := httptest.NewRequest(http.MethodGet, "/access/users/"+createdUser.ID, nil)
	detailRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: adminAccess})
	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, detailRequest)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `data-confirm-title="Reset credential?"`) || !strings.Contains(detail.Body.String(), `name="confirm_current" value="yes" data-confirm-consent disabled`) {
		t.Fatalf("other-user credential confirmation = %d %q", detail.Code, detail.Body.String())
	}
	resetForm := url.Values{
		"csrf_token": {adminCSRF}, "expected_user_version": {"1"}, "expected_credential_version": {"1"},
		"temporary_password": {"reset temporary password"},
	}
	unconfirmedReset := performAccessMutation(t, handler, config, "/access/users/"+createdUser.ID+"/credential", resetForm, adminAccess, adminCSRF, true)
	if unconfirmedReset.Code != http.StatusBadRequest || strings.Contains(unconfirmedReset.Body.String(), "reset temporary password") || !strings.Contains(unconfirmedReset.Body.String(), `aria-describedby="access-form-error"`) {
		t.Fatalf("unconfirmed credential reset = %d %q", unconfirmedReset.Code, unconfirmedReset.Body.String())
	}
	unchangedUser, found, err := provider.Users().GetUser(t.Context(), createdUser.ID)
	if err != nil || !found || unchangedUser.Credential.Version != 1 {
		t.Fatalf("unconfirmed reset changed credential: %+v found=%t err=%v", unchangedUser, found, err)
	}
	resetForm.Set("confirm_current", "yes")
	confirmedReset := performAccessMutation(t, handler, config, "/access/users/"+createdUser.ID+"/credential", resetForm, adminAccess, adminCSRF, false)
	if confirmedReset.Code != http.StatusSeeOther || confirmedReset.Header().Get("Location") != testAccessPath {
		t.Fatalf("confirmed credential reset = %d %q", confirmedReset.Code, confirmedReset.Header().Get("Location"))
	}

	operatorAccess, operatorCSRF := loginHTTPApp(t, handler, config, "operator")
	deniedRequest := httptest.NewRequest(http.MethodGet, testAccessPath, nil)
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
	selfReset := performAccessMutation(t, handler, config, "/access/users/admin/credential", url.Values{
		"csrf_token": {adminCSRF}, "expected_user_version": {"1"}, "expected_credential_version": {"1"},
		"temporary_password": {"self temporary password"}, "confirm_current": {"yes"},
	}, adminAccess, adminCSRF, false)
	if selfReset.Code != http.StatusSeeOther || selfReset.Header().Get("Location") != testLoginPath {
		t.Fatalf("self credential reset = %d %q", selfReset.Code, selfReset.Header().Get("Location"))
	}
	if _, err := app.security.ValidateAccess(t.Context(), adminAccess); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("self credential reset retained access: %v", err)
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
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `data-binding-channel="slackagent"`) || strings.Contains(detail.Body.String(), `data-binding-channel="zulip"`) {
		t.Fatalf("configured binding forms = %d", detail.Code)
	}
	invitations, err := auth.NewBindingInvitations(provider.Users().(usercmd.InvitationStore))
	if err != nil {
		t.Fatal(err)
	}
	app.invitations = invitations
	channels := auth.NewBindingChannels([]string{"telegram", "slackagent"})
	integration := usercmd.BindingIntegration{ChannelType: "telegram", Key: "123"}
	if err := channels.Register(usercmd.BindingChannel{Integration: integration, BotUsername: "balda_test_bot"}); err != nil {
		t.Fatal(err)
	}
	app.bindingChannels = channels
	identityPath := "/access/users/operator/invitations/slackagent/identity"
	identityForm := url.Values{"csrf_token": {adminCSRF}}
	unavailableIdentity := performAccessMutation(t, handler, config, identityPath, identityForm, adminAccess, adminCSRF, true)
	if unavailableIdentity.Code != http.StatusServiceUnavailable || !strings.Contains(unavailableIdentity.Body.String(), `data-binding-channel="slackagent"`) || !strings.Contains(unavailableIdentity.Body.String(), "Retry connection") || !strings.Contains(unavailableIdentity.Body.String(), `aria-describedby="binding-error-slackagent"`) {
		t.Fatalf("unavailable identity refresh = %d %q", unavailableIdentity.Code, unavailableIdentity.Body.String())
	}
	slackIdentityUnavailable := false
	if err := channels.RegisterResolver("slackagent", func(context.Context) (usercmd.BindingChannel, error) {
		if slackIdentityUnavailable {
			return usercmd.BindingChannel{}, errors.New("synthetic identity unavailable")
		}
		return usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: "slackagent", Key: "TQA:UBOT"}, Name: "Recovered workspace"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	recoveredIdentity := performAccessMutation(t, handler, config, identityPath, identityForm, adminAccess, adminCSRF, false)
	if recoveredIdentity.Code != http.StatusSeeOther || recoveredIdentity.Header().Get("Location") != "/access/users/operator" {
		t.Fatalf("recovered identity refresh = %d %q", recoveredIdentity.Code, recoveredIdentity.Header().Get("Location"))
	}
	slackIdentityUnavailable = true
	failedIdentity := performAccessMutation(t, handler, config, identityPath, identityForm, adminAccess, adminCSRF, false)
	if failedIdentity.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed identity refresh = %d %q", failedIdentity.Code, failedIdentity.Body.String())
	}
	issuePath := "/access/users/operator/invitations/telegram"
	issue := func(version string, replace, htmx bool) *httptest.ResponseRecorder {
		form := url.Values{"csrf_token": {adminCSRF}, "expected_version": {version}, "principal": {"attacker"}, "channel_type": {"zulip"}, "integration_key": {"fake"}}
		if replace {
			form.Set("replace", "yes")
		}
		return performAccessMutation(t, handler, config, issuePath, form, adminAccess, adminCSRF, htmx)
	}
	issued := issue("1", false, false)
	if issued.Code != http.StatusOK || issued.Header().Get("Cache-Control") != noStoreCacheControl || issued.Header().Get("Location") != "" || !strings.Contains(issued.Body.String(), "<!doctype") {
		t.Fatalf("native issuance = %d %v", issued.Code, issued.Header())
	}
	payload := regexp.MustCompile(`bind_[A-Za-z0-9_-]{32}`).FindString(issued.Body.String())
	if payload == "" || !strings.Contains(issued.Body.String(), "/start "+payload) || !strings.Contains(issued.Body.String(), "https://t.me/balda_test_bot?start="+payload) {
		t.Fatal("missing one-time Telegram actions")
	}
	refreshed := httptest.NewRecorder()
	handler.ServeHTTP(refreshed, detailRequest)
	if refreshed.Code != http.StatusOK || strings.Contains(refreshed.Body.String(), payload) || !strings.Contains(refreshed.Body.String(), "Waiting for confirmation") {
		t.Fatal("refresh must contain pending metadata only")
	}
	if got := issue("1", false, true); got.Code != http.StatusConflict {
		t.Fatalf("unconfirmed replacement = %d", got.Code)
	}
	replacement := issue("1", true, true)
	nextPayload := regexp.MustCompile(`bind_[A-Za-z0-9_-]{32}`).FindString(replacement.Body.String())
	if replacement.Code != http.StatusOK || strings.Contains(replacement.Body.String(), "<!doctype") || nextPayload == "" || nextPayload == payload || strings.Contains(replacement.Body.String(), payload) {
		t.Fatal("HTMX replacement must reveal only new secret in fragment")
	}
	proof := usercmd.BindingProof{Payload: payload, Integration: integration, Principal: "202", Direct: true, Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "202", SessionID: "202"}}
	if _, err := invitations.Consume(t.Context(), proof); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
		t.Fatalf("replaced invitation consumed: %v", err)
	}
	proof.Payload = nextPayload
	if id, err := invitations.Consume(t.Context(), proof); err != nil || id != "operator" {
		t.Fatalf("selected target = %q: %v", id, err)
	}
	user, found, err := provider.Users().GetUser(t.Context(), "operator")
	if err != nil || !found || len(user.Bindings) != 1 || user.Bindings[0].Principal != "202" || user.Role != usercmd.RoleOperator || user.Primary {
		t.Fatalf("bound user = %+v: %v", user, err)
	}
	if got := issue("1", false, false); got.Code != http.StatusConflict {
		t.Fatalf("stale user = %d", got.Code)
	}
	version := "2"
	previousConfig, err := invitations.Issue(t.Context(), usercmd.InvitationActor{UserID: "admin", SessionID: "synthetic-admin-session"}, "operator", 2, usercmd.BindingIntegration{ChannelType: "telegram", Key: "previous-bot-configuration"}, false)
	if err != nil {
		t.Fatal(err)
	}
	newInvitation := issue(version, false, false)
	if newInvitation.Code != http.StatusOK {
		t.Fatalf("new invitation = %d", newInvitation.Code)
	}
	pending, err := invitations.Pending(t.Context(), "operator")
	if err != nil || len(pending) != 2 || pending[0].TokenDigest != nil || pending[1].TokenDigest != nil {
		t.Fatalf("pending = %+v: %v", pending, err)
	}
	if !pending[0].ExpiresAt.Truncate(time.Minute).Equal(pending[1].ExpiresAt.Truncate(time.Minute)) {
		t.Fatalf("synthetic pending invitations do not share an expiry minute: %+v", pending)
	}
	for _, invitation := range pending {
		if !strings.Contains(newInvitation.Body.String(), "Reference <code>"+invitation.ID+"</code>") || !strings.Contains(newInvitation.Body.String(), "reference "+invitation.ID+" expiring") {
			t.Fatalf("invitation %s lacks an exact visible and confirmation target: %q", invitation.ID, newInvitation.Body.String())
		}
	}
	if !strings.Contains(newInvitation.Body.String(), previousConfig.Invitation.ID) {
		t.Fatal("previous-configuration invitation is not distinguishable")
	}
	cancelPath := issuePath + "/cancel"
	cancelForm := url.Values{"csrf_token": {adminCSRF}, "invitation_id": {pending[0].ID}, "invitation_version": {"1"}}
	if got := performAccessMutation(t, handler, config, cancelPath, cancelForm, adminAccess, adminCSRF, false); got.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed cancel = %d", got.Code)
	}
	cancelForm.Set("confirm_cancel", "yes")
	if got := performAccessMutation(t, handler, config, cancelPath, cancelForm, adminAccess, adminCSRF, true); got.Code != http.StatusNoContent || testHTMXLocation(t, got.Header()) != "/access/users/operator" {
		t.Fatalf("cancel = %d %v", got.Code, got.Header())
	}
	badCSRF := performAccessMutation(t, handler, config, issuePath, url.Values{"csrf_token": {"invalid"}, "expected_version": {version}}, adminAccess, adminCSRF, false)
	if badCSRF.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF = %d", badCSRF.Code)
	}
	unavailable := performAccessMutation(t, handler, config, "/access/users/operator/invitations/slackagent", url.Values{"csrf_token": {adminCSRF}, "expected_version": {version}}, adminAccess, adminCSRF, false)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable identity = %d", unavailable.Code)
	}
	operatorAccess, operatorCSRF := loginHTTPApp(t, handler, config, "operator")
	denied := performAccessMutation(t, handler, config, issuePath, url.Values{"csrf_token": {operatorCSRF}, "expected_version": {version}}, operatorAccess, operatorCSRF, false)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("operator issuance = %d", denied.Code)
	}
	unconfigured := performAccessMutation(t, handler, config, "/access/users/operator/invitations/zulip", url.Values{"csrf_token": {adminCSRF}, "expected_version": {version}}, adminAccess, adminCSRF, false)
	if unconfigured.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unconfigured invitation = %d", unconfigured.Code)
	}
	originRequest := httptest.NewRequest(http.MethodPost, issuePath, strings.NewReader(url.Values{"csrf_token": {adminCSRF}, "expected_version": {version}}.Encode()))
	originRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	originRequest.Header.Set("Origin", "https://other.example.test")
	originRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: adminAccess})
	originRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: adminCSRF})
	originResponse := httptest.NewRecorder()
	handler.ServeHTTP(originResponse, originRequest)
	if originResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin invitation = %d", originResponse.Code)
	}
	config.Balda.Slack.Agent.Enabled = false
	singleApp, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	singleApp.invitations, singleApp.bindingChannels = invitations, channels
	singleHandler, err := singleApp.handler()
	if err != nil {
		t.Fatal(err)
	}
	singleDetail := httptest.NewRecorder()
	singleHandler.ServeHTTP(singleDetail, detailRequest)
	if singleDetail.Code != http.StatusOK || strings.Count(singleDetail.Body.String(), `data-binding-channel=`) != 1 {
		t.Fatal("Telegram-only configuration must render exactly its form")
	}
	removePath := "/access/users/operator/bindings/" + user.Bindings[0].ID + "/delete"
	removeForm := url.Values{"csrf_token": {adminCSRF}, "expected_version": {version}}
	if got := performAccessMutation(t, handler, config, removePath, removeForm, adminAccess, adminCSRF, false); got.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed removal = %d", got.Code)
	}
	removeForm.Set("confirm_bot_impact", "yes")
	if got := performAccessMutation(t, handler, config, removePath, removeForm, adminAccess, adminCSRF, false); got.Code != http.StatusSeeOther {
		t.Fatalf("confirmed removal = %d", got.Code)
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
	accountRequest := httptest.NewRequest(http.MethodGet, testAccountPath, nil)
	accountRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: initial.access})
	accountRequest.AddCookie(&http.Cookie{Name: security.CSRFCookieName, Value: initial.csrf})
	account := httptest.NewRecorder()
	handler.ServeHTTP(account, accountRequest)
	if account.Code != http.StatusOK || !strings.Contains(account.Body.String(), "Change password") || strings.Contains(account.Body.String(), "Access</span>") {
		t.Fatalf("account page = %d %q", account.Code, account.Body.String())
	}

	rotateForm := url.Values{
		"csrf_token": {initial.csrf}, "current_password": {validPassword},
		"new_password": {"replacement password"},
	}
	wrongRotateForm := url.Values{
		"csrf_token": {initial.csrf}, "current_password": {invalidPassword},
		"new_password": {"replacement password"},
	}
	wrongRotation := performAccessMutation(t, handler, config, "/account/password", wrongRotateForm, initial.access, initial.csrf, true)
	if wrongRotation.Code != http.StatusUnauthorized || strings.Contains(wrongRotation.Body.String(), "<!doctype") || strings.Count(wrongRotation.Body.String(), `id="main-content"`) != 1 ||
		!strings.Contains(wrongRotation.Body.String(), `aria-describedby="password-error"`) || strings.Contains(wrongRotation.Body.String(), invalidPassword) || strings.Contains(wrongRotation.Body.String(), "replacement password") {
		t.Fatalf("wrong password rotation = %d %q", wrongRotation.Code, wrongRotation.Body.String())
	}
	rotated := performAccessMutation(t, handler, config, "/account/password", rotateForm, initial.access, initial.csrf, true)
	if rotated.Code != http.StatusNoContent || testHTMXLocation(t, rotated.Header()) != testAccountPath {
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
	if nativeChange.Code != http.StatusSeeOther || nativeChange.Header().Get("Location") != testAccountPath {
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
	if revoked.Code != http.StatusSeeOther || revoked.Header().Get("Location") != testLoginPath {
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
		credentials, err := app.security.Login(t.Context(), "admin", []byte(validPassword))
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
	first := account(testAccountPath)
	if first.Code != http.StatusOK || strings.Count(first.Body.String(), ">End session</button>") != sessionPageSize {
		t.Fatalf("first session page = %d, end actions = %d", first.Code, strings.Count(first.Body.String(), ">End session</button>"))
	}
	if strings.Count(first.Body.String(), "data-confirm-consent") != sessionPageSize || !strings.Contains(first.Body.String(), "This will sign you out.") || !strings.Contains(first.Body.String(), "That browser will lose access.") {
		t.Fatalf("first session page does not confirm every destructive action: %q", first.Body.String())
	}
	firstSession := regexp.MustCompile(`<h3[^>]*>(.*?)</h3>`).FindStringSubmatch(first.Body.String())
	if len(firstSession) != 2 || !strings.Contains(firstSession[1], "Current") {
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
	unconfirmedOwn := performAccessMutation(t, handler, config, "/account/sessions/"+other.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}}, oldest.access, oldest.csrf, false)
	if unconfirmedOwn.Code == http.StatusSeeOther {
		t.Fatalf("unconfirmed owned session revocation redirected with %d", unconfirmedOwn.Code)
	}
	if _, err := app.security.ValidateAccess(t.Context(), otherAccess); err != nil {
		t.Fatalf("unconfirmed owned session revocation changed the target: %v", err)
	}
	revokedOwn := performAccessMutation(t, handler, config, "/account/sessions/"+other.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}, "confirm_current": {"yes"}}, oldest.access, oldest.csrf, false)
	if revokedOwn.Code != http.StatusSeeOther || revokedOwn.Header().Get("Location") != testAccountPath {
		t.Fatalf("owned session revocation = %d %q", revokedOwn.Code, revokedOwn.Header().Get("Location"))
	}
	if _, err := app.security.ValidateAccess(t.Context(), otherAccess); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("revoked owned access validation = %v", err)
	}
	administrativeCredentials, err := app.security.Login(t.Context(), "admin", []byte(validPassword))
	if err != nil {
		t.Fatal(err)
	}
	administrative, err := app.security.ValidateAccess(t.Context(), administrativeCredentials.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	unconfirmed := performAccessMutation(t, handler, config, "/access/users/admin/sessions/"+administrative.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}}, oldest.access, oldest.csrf, false)
	if unconfirmed.Code == http.StatusSeeOther {
		t.Fatalf("unconfirmed access session revocation redirected with %d", unconfirmed.Code)
	}
	if _, err := app.security.ValidateAccess(t.Context(), administrativeCredentials.AccessToken); err != nil {
		t.Fatalf("unconfirmed access session revocation changed the target: %v", err)
	}
	revoked := performAccessMutation(t, handler, config, "/access/users/admin/sessions/"+administrative.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}, "confirm_current": {"yes"}}, oldest.access, oldest.csrf, false)
	if revoked.Code != http.StatusSeeOther || revoked.Header().Get("Location") != "/access/users/admin" {
		t.Fatalf("access session revocation = %d %q", revoked.Code, revoked.Header().Get("Location"))
	}
	if _, err := app.security.ValidateAccess(t.Context(), administrativeCredentials.AccessToken); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("revoked access validation = %v", err)
	}
	self, err := app.security.ValidateAccess(t.Context(), oldest.access)
	if err != nil {
		t.Fatal(err)
	}
	endedCurrent := performAccessMutation(t, handler, config, "/access/users/admin/sessions/"+self.FamilyID+"/revoke",
		url.Values{"csrf_token": {oldest.csrf}, "confirm_current": {"yes"}}, oldest.access, oldest.csrf, false)
	if endedCurrent.Code != http.StatusSeeOther || endedCurrent.Header().Get("Location") != testLoginPath {
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
	for _, option := range []string{"mcp.definition.changed", "schedule.run.requested", "provider.definition.changed", "webhook.test.requested", "plugin.marketplace.refresh_failed", "skill.management.changed", "hook.definition.changed", "memory.fact.edited", "alias.retargeted", `value="plugin_marketplace"`, `value="webhook_route"`} {
		if !strings.Contains(body, option) {
			t.Fatalf("audit filter omitted supported option %q", option)
		}
	}
	for _, forbidden := range []string{validPassword, admin.access, admin.refresh, admin.csrf} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("audit leaked browser secret %q", forbidden)
		}
	}
	if _, err := app.security.Login(t.Context(), "operator", []byte(validPassword)); err != nil {
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

	invalidPath := "/audit?action=product.event&outcome=succeeded&target=session&actor=operator&from=" + day
	invalidRequest := httptest.NewRequest(http.MethodGet, invalidPath, nil)
	invalidRequest.Header.Set("HX-Request", "true")
	invalidRequest.Header.Set("HX-Target", "main-content")
	invalidRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest || strings.Contains(invalid.Body.String(), "<!doctype") || !strings.Contains(invalid.Body.String(), `id="audit-filter-error"`) || !strings.Contains(invalid.Body.String(), `aria-describedby="audit-filter-error"`) || !strings.Contains(invalid.Body.String(), `value="succeeded" selected`) || !strings.Contains(invalid.Body.String(), `value="session" selected`) || !strings.Contains(invalid.Body.String(), `value="operator"`) || !strings.Contains(invalid.Body.String(), `value="`+day+`"`) || strings.Contains(invalid.Body.String(), `value="product.event"`) {
		t.Fatalf("invalid audit filter = %d %q", invalid.Code, invalid.Body.String())
	}
	invalidNativeRequest := httptest.NewRequest(http.MethodGet, invalidPath, nil)
	invalidNativeRequest.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
	invalidNative := httptest.NewRecorder()
	handler.ServeHTTP(invalidNative, invalidNativeRequest)
	if invalidNative.Code != http.StatusBadRequest || !strings.Contains(invalidNative.Body.String(), "<!doctype html>") || !strings.Contains(invalidNative.Body.String(), `aria-describedby="audit-filter-error"`) {
		t.Fatalf("native invalid audit filter = %d", invalidNative.Code)
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
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, config.Server.BasePath+testLoginPath, nil))
	csrf := cookieValue(loginPage.Result().Cookies(), security.CSRFCookieName)
	form := url.Values{"username": {username}, "password": {validPassword}, "csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, config.Server.BasePath+testLoginPath, strings.NewReader(form.Encode()))
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

func testHTMXLocation(t *testing.T, header http.Header) string {
	t.Helper()
	var location struct{ Path, Target, Swap string }
	if err := json.Unmarshal([]byte(header.Get("HX-Location")), &location); err != nil {
		t.Fatal(err)
	}
	if location.Target != "#main-content" || location.Swap != "outerHTML" {
		t.Fatalf("mutation must preserve shell: %+v", location)
	}
	return location.Path
}
