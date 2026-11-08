package browseredge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

const testRemoteAddr = "127.0.0.1:1234"

func TestLoginEnforcesOriginCSRFAndSetsScopedCookies(t *testing.T) {
	service := &fakeService{}
	service.login = func(_ context.Context, request managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error) {
		require.Equal(t, "admin", request.Username)
		require.Equal(t, "127.0.0.1", *request.BrowserContext.ConnectionPeer)
		return managementv1.WebAuthLoginResult{Outcome: managementv1.Authenticated, Credentials: credentialFixture()}, nil
	}
	app := newTestApp(t, service)

	page := httptest.NewRecorder()
	app.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/login", nil))
	csrf := responseCookie(t, page, csrfCookie)

	form := url.Values{"username": {"admin"}, "password": {"secret"}, "csrf_token": {csrf.Value}}
	request := httptest.NewRequest(http.MethodPost, "http://admin.test/balda/backoffice/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://admin.test")
	request.Header.Set("Cookie", csrfCookie+"="+csrf.Value)
	request.RemoteAddr = testRemoteAddr
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code)
	access := responseCookie(t, response, accessCookie)
	refresh := responseCookie(t, response, refreshCookie)
	require.Equal(t, "/balda/backoffice/", access.Path)
	require.Equal(t, "/balda/backoffice/auth/session/refresh", refresh.Path)
	require.True(t, access.HttpOnly)
	require.NotContains(t, response.Body.String(), "operator-secret")

	bad := httptest.NewRequest(http.MethodPost, "http://admin.test/balda/backoffice/login", strings.NewReader(form.Encode()))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bad.Header.Set("Origin", "http://evil.test")
	bad.Header.Set("Cookie", csrfCookie+"="+csrf.Value)
	badResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(badResponse, bad)
	require.Equal(t, http.StatusForbidden, badResponse.Code)
}

func TestLimiterStopsSixthLoginBeforeCore(t *testing.T) {
	calls := 0
	service := &fakeService{login: func(context.Context, managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error) {
		calls++
		return managementv1.WebAuthLoginResult{}, managementclient.ErrUnauthorized
	}}
	app := newTestApp(t, service)
	for attempt := 1; attempt <= 6; attempt++ {
		request := mutationRequest("/balda/backoffice/login", url.Values{"username": {"nobody"}, "password": {"wrong"}})
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if attempt == 6 {
			require.Equal(t, http.StatusTooManyRequests, response.Code)
			require.NotEmpty(t, response.Header().Get("Retry-After"))
		}
	}
	require.Equal(t, 5, calls)
}

func TestAmbiguousRefreshClearsCredentialsWithoutRetry(t *testing.T) {
	calls := 0
	service := &fakeService{refresh: func(context.Context, managementclient.Auth) (managementv1.WebAuthCredentials, error) {
		calls++
		return managementv1.WebAuthCredentials{}, managementclient.ErrUnavailable
	}}
	app := newTestApp(t, service)
	request := mutationRequest("/balda/backoffice/auth/session/refresh", nil)
	request.Header.Add("Cookie", accessCookie+"=access; "+refreshCookie+"=refresh")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, 1, calls)
	require.Equal(t, -1, responseCookie(t, response, refreshCookie).MaxAge)
}

func TestUnknownProductPageIsNotProxied(t *testing.T) {
	app := newTestApp(t, &fakeService{})
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/providers", nil)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
}

func newTestApp(t *testing.T, service Service) *App {
	t.Helper()
	app, err := New(service, Config{PublicOrigin: "http://admin.test", MountPath: "/balda/backoffice"})
	require.NoError(t, err)
	return app
}

func mutationRequest(path string, form url.Values) *http.Request {
	if form == nil {
		form = make(url.Values)
	}
	form.Set("csrf_token", "csrf")
	request := httptest.NewRequest(http.MethodPost, "http://admin.test"+path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://admin.test")
	request.Header.Set("Cookie", csrfCookie+"=csrf")
	request.RemoteAddr = testRemoteAddr
	return request
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func credentialFixture() *managementv1.WebAuthCredentials {
	return &managementv1.WebAuthCredentials{AccessToken: "access", RefreshToken: "refresh", CSRFToken: "next-csrf", Assurance: managementv1.Normal, AccessExpiresAt: time.Now().Add(time.Minute), RefreshExpiresAt: time.Now().Add(time.Hour)}
}

type fakeService struct {
	login   func(context.Context, managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error)
	refresh func(context.Context, managementclient.Auth) (managementv1.WebAuthCredentials, error)
}

func (f *fakeService) Login(ctx context.Context, request managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error) {
	return f.login(ctx, request)
}
func (f *fakeService) FinishLogin(context.Context, managementv1.WebAuthLoginMFAFinishRequest) (managementv1.WebAuthCredentials, error) {
	return managementv1.WebAuthCredentials{}, errors.New("unexpected call")
}
func (f *fakeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{}, errors.New("unexpected call")
}
func (f *fakeService) Refresh(ctx context.Context, auth managementclient.Auth) (managementv1.WebAuthCredentials, error) {
	return f.refresh(ctx, auth)
}
func (f *fakeService) Logout(context.Context, managementclient.Auth) error {
	return errors.New("unexpected call")
}
func (f *fakeService) ReplacePassword(context.Context, managementclient.Auth, managementv1.WebAuthPasswordReplaceRequest) (managementv1.WebAuthCredentials, error) {
	return managementv1.WebAuthCredentials{}, errors.New("unexpected call")
}
func (f *fakeService) MFAStatus(context.Context, managementclient.Auth) (managementv1.WebAuthMFAStatus, error) {
	return managementv1.WebAuthMFAStatus{}, errors.New("unexpected call")
}
func (f *fakeService) BeginMFA(context.Context, managementclient.Auth, bool, *string, *string) (managementv1.WebAuthMFAContinuation, error) {
	return managementv1.WebAuthMFAContinuation{}, errors.New("unexpected call")
}
func (f *fakeService) FinishMFA(context.Context, managementclient.Auth, bool, managementv1.WebAuthMFAFinishRequest) (managementv1.WebAuthCredentials, error) {
	return managementv1.WebAuthCredentials{}, errors.New("unexpected call")
}
func (f *fakeService) DisableMFA(context.Context, managementclient.Auth, *string) (managementv1.WebAuthCredentials, error) {
	return managementv1.WebAuthCredentials{}, errors.New("unexpected call")
}
func (f *fakeService) Sessions(context.Context, managementclient.Auth) (managementv1.WebAuthSessionPage, error) {
	return managementv1.WebAuthSessionPage{}, errors.New("unexpected call")
}
func (f *fakeService) RevokeSession(context.Context, managementclient.Auth, string, bool) error {
	return errors.New("unexpected call")
}
