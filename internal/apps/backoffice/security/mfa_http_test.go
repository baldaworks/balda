package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/stretchr/testify/require"
)

func TestMFABrowserFinishLoginSetsCredentialsAndClearsBinding(t *testing.T) {
	now := time.Now().UTC()
	service := &fakeMFAService{}
	service.finishLogin = func(_ context.Context, transaction, browser, csrf string, response []byte) (browserauth.Credentials, error) {
		require.Equal(t, "transaction", transaction)
		require.Equal(t, "browser-binding", browser)
		require.Equal(t, "csrf", csrf)
		require.Equal(t, "assertion", string(response))
		return browserauth.Credentials{AccessToken: "access", WebAuthRefreshToken: "refresh", CSRFToken: "next-csrf", AccessExpiresAt: now.Add(time.Minute), RefreshExpiresAt: now.Add(time.Hour)}, nil
	}
	browser, err := NewBrowser(service, HTTPConfig{TrustedOrigin: "https://example.org", SecureCookies: true})
	require.NoError(t, err)
	request := mfaFormRequest("/auth/webauthn/finish", url.Values{"transaction": {"transaction"}, "credential": {"assertion"}, "csrf_token": {"csrf"}})
	request.AddCookie(&http.Cookie{Name: MFACookieName, Value: "browser-binding"})
	response := httptest.NewRecorder()
	browser.FinishLogin(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code)
	require.Equal(t, "/overview", response.Header().Get("Location"))
	assertClearedCookie(t, response.Result().Cookies(), MFACookieName, "/")
	assertSecurityCookie(t, response.Result().Cookies(), AccessCookieName, "/", "access", true)
	assertSecurityCookie(t, response.Result().Cookies(), RefreshCookieName, RefreshPath, "refresh", true)
}

func TestMFAAccountHTTPEnablePreservesBrowserBinding(t *testing.T) {
	service := &fakeMFAService{fakeBrowserService: fakeBrowserService{csrf: func(context.Context, string, string) error { return nil }}}
	service.beginEnable = func(_ context.Context, access string, password []byte, browser, csrf string) (browserauth.CeremonyStart, error) {
		require.Equal(t, "access", access)
		require.Equal(t, "password", string(password))
		require.NotEmpty(t, browser)
		require.Equal(t, "csrf", csrf)
		return browserauth.CeremonyStart{Transaction: "transaction", Options: map[string]any{"challenge": "opaque"}}, nil
	}
	service.finishEnable = func(_ context.Context, access, transaction, browser, csrf string, response []byte) (browserauth.Credentials, error) {
		require.Equal(t, "access", access)
		require.Equal(t, "transaction", transaction)
		require.NotEmpty(t, browser)
		require.Equal(t, "csrf", csrf)
		require.Equal(t, "registration", string(response))
		now := time.Now().UTC()
		return browserauth.Credentials{AccessToken: "next-access", WebAuthRefreshToken: "next-refresh", CSRFToken: "next-csrf", AccessExpiresAt: now.Add(time.Minute), RefreshExpiresAt: now.Add(time.Hour)}, nil
	}
	browser, err := NewBrowser(service, HTTPConfig{TrustedOrigin: "https://example.org", SecureCookies: true})
	require.NoError(t, err)
	start := mfaFormRequest("/account/2fa/enable/start", url.Values{"password": {"password"}, "csrf_token": {"csrf"}})
	start.Header.Del("Cookie")
	start.AddCookie(&http.Cookie{Name: AccessCookieName, Value: "access"})
	start.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf"})
	started := httptest.NewRecorder()
	browser.BeginEnable(started, start)
	require.Equal(t, http.StatusOK, started.Code)
	var binding *http.Cookie
	for _, cookie := range started.Result().Cookies() {
		if cookie.Name == MFACookieName {
			binding = cookie
		}
	}
	require.NotNil(t, binding)
	finish := mfaFormRequest("/account/2fa/enable/finish", url.Values{"transaction": {"transaction"}, "credential": {"registration"}, "csrf_token": {"csrf"}})
	finish.Header.Del("Cookie")
	finish.AddCookie(&http.Cookie{Name: AccessCookieName, Value: "access"})
	finish.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf"})
	finish.AddCookie(binding)
	finished := httptest.NewRecorder()
	browser.FinishEnable(finished, finish)
	require.Equal(t, http.StatusSeeOther, finished.Code)
	require.Equal(t, "/account", finished.Header().Get("Location"))
	assertClearedCookie(t, finished.Result().Cookies(), MFACookieName, "/")
}

func mfaFormRequest(target string, values url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://example.org")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf"})
	return request
}

type fakeMFAService struct {
	fakeBrowserService
	finishLogin  func(context.Context, string, string, string, []byte) (browserauth.Credentials, error)
	beginEnable  func(context.Context, string, []byte, string, string) (browserauth.CeremonyStart, error)
	finishEnable func(context.Context, string, string, string, string, []byte) (browserauth.Credentials, error)
}

func (f *fakeMFAService) FinishLogin(ctx context.Context, transaction, browser, csrf string, response []byte) (browserauth.Credentials, error) {
	return f.finishLogin(ctx, transaction, browser, csrf, response)
}

func (f *fakeMFAService) BeginEnable(ctx context.Context, token string, password []byte, browser, csrf string) (browserauth.CeremonyStart, error) {
	return f.beginEnable(ctx, token, password, browser, csrf)
}

func (f *fakeMFAService) FinishEnable(ctx context.Context, token, transaction, browser, csrf string, response []byte) (browserauth.Credentials, error) {
	return f.finishEnable(ctx, token, transaction, browser, csrf, response)
}

func (f *fakeMFAService) BeginReplace(context.Context, string, []byte, bool, string, string) (browserauth.CeremonyStart, error) {
	return browserauth.CeremonyStart{}, browserauth.ErrForbidden
}

func (f *fakeMFAService) FinishReplace(context.Context, string, string, string, string, []byte) (browserauth.Credentials, error) {
	return browserauth.Credentials{}, browserauth.ErrForbidden
}

func (f *fakeMFAService) Disable(context.Context, string, []byte, bool) (browserauth.Credentials, error) {
	return browserauth.Credentials{}, browserauth.ErrForbidden
}

var _ accountMFAService = (*fakeMFAService)(nil)
var _ mfaBrowserService = (*fakeMFAService)(nil)
