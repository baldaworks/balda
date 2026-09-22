package security

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestBrowserLoginSetsStrictScopedCookiesAndSafeRedirect(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	want := Credentials{
		AccessToken: "access.raw", RefreshToken: "refresh.raw", CSRFToken: "session-csrf",
		AccessExpiresAt: now.Add(15 * time.Minute), RefreshExpiresAt: now.Add(12 * time.Hour),
		Assurance: usercmd.SessionAssuranceNormal,
	}
	service := &fakeBrowserService{
		login: func(_ context.Context, username string, password []byte) (Credentials, error) {
			if username != "admin" || string(password) != testPassword {
				t.Fatalf("Login() input = %q, %q", username, password)
			}
			return want, nil
		},
	}
	browser := newHTTPTestBrowser(t, service, true, 0)
	request := mutationRequest(http.MethodPost, "/login", url.Values{
		"username": {"admin"}, "password": {testPassword}, "csrf_token": {"csrf-proof"},
		"return_to": {"https://attacker.example/steal"},
	})
	request.Header.Set("Origin", "https://backoffice.example")
	response := httptest.NewRecorder()
	browser.Login(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/overview" {
		t.Fatalf("Login() status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	assertSecurityCookie(t, cookies, AccessCookieName, "/", "access.raw", true)
	assertSecurityCookie(t, cookies, RefreshCookieName, RefreshPath, "refresh.raw", true)
	assertSecurityCookie(t, cookies, CSRFCookieName, "/", "session-csrf", true)
}

func TestBrowserRefreshRotatesCookiesAndNeverReplaysUnsafeRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	service := &fakeBrowserService{
		refresh: func(_ context.Context, refreshToken, csrfToken string) (Credentials, error) {
			if refreshToken != "old.refresh" || csrfToken != "csrf-proof" {
				t.Fatalf("Refresh() input = %q, %q", refreshToken, csrfToken)
			}
			return Credentials{
				AccessToken: "new.access", RefreshToken: "new.refresh", CSRFToken: csrfToken,
				AccessExpiresAt: now.Add(15 * time.Minute), RefreshExpiresAt: now.Add(12 * time.Hour),
			}, nil
		},
	}
	browser := newHTTPTestBrowser(t, service, false, 0)
	request := mutationRequest(http.MethodPost, RefreshPath, url.Values{
		"csrf_token": {"csrf-proof"}, "return_to": {"/access/users/user-1?tab=sessions"},
	})
	request.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: "old.refresh", Path: RefreshPath})
	response := httptest.NewRecorder()
	browser.Refresh(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/access/users/user-1?tab=sessions" {
		t.Fatalf("Refresh() status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	assertSecurityCookie(t, response.Result().Cookies(), RefreshCookieName, RefreshPath, "new.refresh", false)
}

func TestBrowserRestrictedSessionAlwaysContinuesToPasswordReplacement(t *testing.T) {
	t.Parallel()
	service := &fakeBrowserService{login: func(context.Context, string, []byte) (Credentials, error) {
		return Credentials{
			AccessToken: "restricted.access", RefreshToken: "restricted.refresh", CSRFToken: "session-csrf",
			AccessExpiresAt: time.Now().Add(time.Minute), RefreshExpiresAt: time.Now().Add(time.Hour),
			Assurance: usercmd.SessionAssuranceRestricted,
		}, nil
	}}
	browser := newHTTPTestBrowser(t, service, false, 0)
	request := mutationRequest(http.MethodPost, "/login", url.Values{
		"csrf_token": {"csrf-proof"}, "return_to": {"/access"},
	})
	response := httptest.NewRecorder()
	browser.Login(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/account/password" {
		t.Fatalf("Login(restricted) status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
}

func TestBrowserMutationGuardsRejectBeforeService(t *testing.T) {
	t.Parallel()
	called := false
	service := &fakeBrowserService{login: func(context.Context, string, []byte) (Credentials, error) {
		called = true
		return Credentials{}, nil
	}}
	tests := []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{name: "method", mutate: func(r *http.Request) { r.Method = http.MethodGet }, status: http.StatusMethodNotAllowed},
		{name: "origin", mutate: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") }, status: http.StatusForbidden},
		{name: "fetch metadata", mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, status: http.StatusForbidden},
		{name: "csrf", mutate: func(r *http.Request) { r.Header.Set("Cookie", CSRFCookieName+"=different") }, status: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			browser := newHTTPTestBrowser(t, service, false, 0)
			request := mutationRequest(http.MethodPost, "/login", url.Values{"csrf_token": {"csrf-proof"}})
			tt.mutate(request)
			response := httptest.NewRecorder()
			browser.Login(response, request)
			if response.Code != tt.status {
				t.Fatalf("Login() status = %d, want %d", response.Code, tt.status)
			}
		})
	}
	if called {
		t.Fatal("guard failure reached authentication service")
	}
}

func TestBrowserBoundsFormBodies(t *testing.T) {
	t.Parallel()
	called := false
	service := &fakeBrowserService{login: func(context.Context, string, []byte) (Credentials, error) {
		called = true
		return Credentials{}, nil
	}}
	browser := newHTTPTestBrowser(t, service, false, 256)
	form := url.Values{"csrf_token": {"csrf-proof"}, "password": {strings.Repeat("x", 512)}}
	request := mutationRequest(http.MethodPost, "/login", form)
	response := httptest.NewRecorder()
	browser.Login(response, request)
	if response.Code != http.StatusBadRequest || called {
		t.Fatalf("Login(oversized) status=%d called=%t", response.Code, called)
	}
}

func TestBrowserAuthenticationFailuresAreGenericAndRefreshClearsFamilyCookies(t *testing.T) {
	t.Parallel()
	service := &fakeBrowserService{
		login:   func(context.Context, string, []byte) (Credentials, error) { return Credentials{}, ErrUnauthenticated },
		refresh: func(context.Context, string, string) (Credentials, error) { return Credentials{}, ErrUnauthenticated },
	}
	browser := newHTTPTestBrowser(t, service, false, 0)
	loginRequest := mutationRequest(http.MethodPost, "/login", url.Values{"csrf_token": {"csrf-proof"}})
	loginResponse := httptest.NewRecorder()
	browser.Login(loginResponse, loginRequest)
	refreshRequest := mutationRequest(http.MethodPost, RefreshPath, url.Values{"csrf_token": {"csrf-proof"}})
	refreshRequest.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: "invalid.refresh"})
	refreshResponse := httptest.NewRecorder()
	browser.Refresh(refreshResponse, refreshRequest)
	if loginResponse.Code != http.StatusUnauthorized || refreshResponse.Code != http.StatusUnauthorized || loginResponse.Body.String() != refreshResponse.Body.String() {
		t.Fatalf("login=(%d,%q), refresh=(%d,%q)", loginResponse.Code, loginResponse.Body.String(), refreshResponse.Code, refreshResponse.Body.String())
	}
	assertClearedCookie(t, refreshResponse.Result().Cookies(), AccessCookieName, "/")
	assertClearedCookie(t, refreshResponse.Result().Cookies(), RefreshCookieName, RefreshPath)
}

func TestBrowserLogoutRequiresSessionCSRFAndClearsCredentials(t *testing.T) {
	t.Parallel()
	validated := false
	loggedOut := false
	service := &fakeBrowserService{
		csrf: func(_ context.Context, token, csrf string) error {
			validated = token == "access.raw" && csrf == "csrf-proof"
			return nil
		},
		logout: func(_ context.Context, token string) error {
			loggedOut = token == "access.raw"
			return nil
		},
	}
	browser := newHTTPTestBrowser(t, service, false, 0)
	request := mutationRequest(http.MethodPost, "/logout", url.Values{"csrf_token": {"csrf-proof"}})
	request.AddCookie(&http.Cookie{Name: AccessCookieName, Value: "access.raw"})
	response := httptest.NewRecorder()
	browser.Logout(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" || !validated || !loggedOut {
		t.Fatalf("Logout() status=%d location=%q validated=%t loggedOut=%t", response.Code, response.Header().Get("Location"), validated, loggedOut)
	}
	assertClearedCookie(t, response.Result().Cookies(), AccessCookieName, "/")
	assertClearedCookie(t, response.Result().Cookies(), RefreshCookieName, RefreshPath)
}

func TestBrowserMiddlewareUsesCurrentPrincipalRoleAndAssurance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		principal Principal
		status    int
	}{
		{
			name: "administrator", status: http.StatusNoContent,
			principal: Principal{User: usercmd.User{Role: usercmd.RoleAdministrator}, Assurance: usercmd.SessionAssuranceNormal},
		},
		{
			name: "operator", status: http.StatusForbidden,
			principal: Principal{User: usercmd.User{Role: usercmd.RoleOperator}, Assurance: usercmd.SessionAssuranceNormal},
		},
		{
			name: "restricted", status: http.StatusForbidden,
			principal: Principal{User: usercmd.User{Role: usercmd.RoleAdministrator}, Assurance: usercmd.SessionAssuranceRestricted},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeBrowserService{validate: func(context.Context, string) (Principal, error) { return tt.principal, nil }}
			browser := newHTTPTestBrowser(t, service, false, 0)
			handler := browser.Authenticate(browser.RequireNormal(browser.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))))
			request := httptest.NewRequest(http.MethodGet, "/access", nil)
			request.AddCookie(&http.Cookie{Name: AccessCookieName, Value: "access.raw"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("middleware status = %d, want %d", response.Code, tt.status)
			}
		})
	}
}

func TestSafeReturnPathRejectsNonAllowlistedAndAmbiguousPaths(t *testing.T) {
	t.Parallel()
	browser := newHTTPTestBrowser(t, &fakeBrowserService{}, false, 0)
	for _, unsafe := range []string{"https://attacker.example/", "//attacker.example/", `/access\\evil`, "/auth/session/refresh", "/access/../auth/session/refresh"} {
		if got := browser.SafeReturnPath(unsafe, "/overview"); got != "/overview" {
			t.Errorf("SafeReturnPath(%q) = %q", unsafe, got)
		}
	}
	if got := browser.SafeReturnPath("/account/sessions?active=1", "/overview"); got != "/account/sessions?active=1" {
		t.Errorf("SafeReturnPath(allowed) = %q", got)
	}
}

type fakeBrowserService struct {
	login    func(context.Context, string, []byte) (Credentials, error)
	validate func(context.Context, string) (Principal, error)
	csrf     func(context.Context, string, string) error
	refresh  func(context.Context, string, string) (Credentials, error)
	logout   func(context.Context, string) error
	replace  func(context.Context, string, []byte, []byte) (Credentials, error)
}

func (f *fakeBrowserService) Login(ctx context.Context, username string, password []byte) (Credentials, error) {
	if f.login == nil {
		return Credentials{}, errors.New("unexpected Login call")
	}
	return f.login(ctx, username, password)
}

func (f *fakeBrowserService) ValidateAccess(ctx context.Context, token string) (Principal, error) {
	if f.validate == nil {
		return Principal{}, errors.New("unexpected ValidateAccess call")
	}
	return f.validate(ctx, token)
}

func (f *fakeBrowserService) ValidateCSRF(ctx context.Context, token, csrf string) error {
	if f.csrf == nil {
		return errors.New("unexpected ValidateCSRF call")
	}
	return f.csrf(ctx, token, csrf)
}

func (f *fakeBrowserService) Refresh(ctx context.Context, token, csrf string) (Credentials, error) {
	if f.refresh == nil {
		return Credentials{}, errors.New("unexpected Refresh call")
	}
	return f.refresh(ctx, token, csrf)
}

func (f *fakeBrowserService) Logout(ctx context.Context, token string) error {
	if f.logout == nil {
		return errors.New("unexpected Logout call")
	}
	return f.logout(ctx, token)
}

func (f *fakeBrowserService) ReplacePassword(ctx context.Context, token string, current, next []byte) (Credentials, error) {
	if f.replace == nil {
		return Credentials{}, errors.New("unexpected ReplacePassword call")
	}
	return f.replace(ctx, token, current, next)
}

func newHTTPTestBrowser(t *testing.T, service browserService, secure bool, maxBodyBytes int64) *Browser {
	t.Helper()
	origin := "http://backoffice.example"
	if secure {
		origin = "https://backoffice.example"
	}
	browser, err := NewBrowser(service, HTTPConfig{TrustedOrigin: origin, SecureCookies: secure, MaxBodyBytes: maxBodyBytes})
	if err != nil {
		t.Fatal(err)
	}
	return browser
}

func mutationRequest(method, target string, form url.Values) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://backoffice.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-proof"})
	return request
}

func assertSecurityCookie(t *testing.T, cookies []*http.Cookie, name, cookiePath, value string, secure bool) {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name != name {
			continue
		}
		if cookie.Value != value || cookie.Path != cookiePath || !cookie.HttpOnly || cookie.Secure != secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "" {
			t.Fatalf("cookie %s = %+v", name, cookie)
		}
		return
	}
	t.Fatalf("cookie %s not found", name)
}

func assertClearedCookie(t *testing.T, cookies []*http.Cookie, name, cookiePath string) {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name && cookie.Path == cookiePath && cookie.MaxAge < 0 {
			return
		}
	}
	t.Fatalf("cleared cookie %s path %s not found", name, cookiePath)
}
