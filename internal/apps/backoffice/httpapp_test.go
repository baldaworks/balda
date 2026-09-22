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
