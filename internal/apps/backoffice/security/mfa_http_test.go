package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/stretchr/testify/require"
)

func TestMFABrowserPendingLoginAndAdministratorGuardsAfterRefresh(t *testing.T) {
	for _, basePath := range []string{"", "/balda"} {
		t.Run(basePath, func(t *testing.T) {
			_, s, u, private, key, now := enrolledTestService(t)
			b, err := NewBrowser(s, HTTPConfig{TrustedOrigin: "https://example.org", SecureCookies: true, BasePath: basePath})
			require.NoError(t, err)
			request := mfaFormRequest(basePath+"/login", url.Values{"username": {u.Username}, "password": {testPassword}, "csrf_token": {"csrf"}})
			response := httptest.NewRecorder()
			b.Login(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			var pending struct {
				Transaction string
				Options     protocol.CredentialAssertion
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &pending))
			var binding *http.Cookie
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == MFACookieName {
					binding = cookie
					require.Equal(t, basePath+"/", cookie.Path)
					require.True(t, cookie.HttpOnly)
				}
				if cookie.Name == AccessCookieName || cookie.Name == RefreshCookieName {
					require.Empty(t, cookie.Value)
				}
			}
			require.NotNil(t, binding)
			start := CeremonyStart{Transaction: pending.Transaction, Options: &pending.Options}
			finish := mfaFormRequest(basePath+"/auth/webauthn/finish", url.Values{"transaction": {start.Transaction}, "credential": {string(assertionResponseForStart(t, start, key, private, 0x05, 1))}, "csrf_token": {"csrf"}})
			finish.AddCookie(binding)
			finished := httptest.NewRecorder()
			b.FinishLogin(finished, finish)
			require.Equal(t, http.StatusSeeOther, finished.Code)
			require.Equal(t, basePath+"/overview", finished.Header().Get("Location"))
			var credentials Credentials
			for _, cookie := range finished.Result().Cookies() {
				switch cookie.Name {
				case AccessCookieName:
					credentials.AccessToken = cookie.Value
				case RefreshCookieName:
					credentials.RefreshToken = cookie.Value
				case CSRFCookieName:
					credentials.CSRFToken = cookie.Value
				}
			}
			s.now = func() time.Time { return now.Add(16 * time.Minute) }
			rotated, err := s.Refresh(t.Context(), credentials.RefreshToken, credentials.CSRFToken)
			require.NoError(t, err)
			called := false
			guard := b.Authenticate(b.RequireAdministrator(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })))
			read := httptest.NewRequest(http.MethodGet, basePath+"/access", nil)
			read.AddCookie(&http.Cookie{Name: AccessCookieName, Value: rotated.AccessToken})
			allowed := httptest.NewRecorder()
			guard.ServeHTTP(allowed, read)
			require.True(t, called)
			require.Equal(t, http.StatusOK, allowed.Code)
			invalidCSRF := mfaFormRequest(basePath+"/access/users", url.Values{"csrf_token": {"wrong"}})
			invalidCSRF.Header.Del("Cookie")
			invalidCSRF.AddCookie(&http.Cookie{Name: AccessCookieName, Value: rotated.AccessToken})
			invalidCSRF.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: rotated.CSRFToken})
			denied := httptest.NewRecorder()
			_, _, ok := b.AdministratorMutation(denied, invalidCSRF)
			require.False(t, ok)
			require.Equal(t, http.StatusForbidden, denied.Code)
			mutation := mfaFormRequest(basePath+"/access/users", url.Values{"csrf_token": {rotated.CSRFToken}})
			mutation.Header.Set("Origin", "https://example.org")
			mutation.Header.Del("Cookie")
			mutation.AddCookie(&http.Cookie{Name: AccessCookieName, Value: rotated.AccessToken})
			mutation.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: rotated.CSRFToken})
			mutationResponse := httptest.NewRecorder()
			_, principal, ok := b.AdministratorMutation(mutationResponse, mutation)
			require.True(t, ok)
			require.Equal(t, u.ID, principal.User.ID)
		})
	}
}

func mfaFormRequest(path string, values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://example.org")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf"})
	return r
}

func TestMFAAccountHTTPEnableGuardsAndCookieScopes(t *testing.T) {
	for _, basePath := range []string{"", "/balda"} {
		t.Run(basePath, func(t *testing.T) {
			p, s, _ := newSecurityTestService(t)
			now := time.Now().UTC()
			s.now = func() time.Time { return now }
			u := createSecurityTestUser(t, p.Users(), "http-enable", "http-enable", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
			credentials, err := s.Login(t.Context(), u.Username, []byte(testPassword))
			require.NoError(t, err)
			s.webauthn, err = newWebAuthnEngine("https://example.org")
			require.NoError(t, err)
			b, err := NewBrowser(s, HTTPConfig{TrustedOrigin: "https://example.org", SecureCookies: true, BasePath: basePath})
			require.NoError(t, err)
			request := mfaFormRequest(basePath+"/account/2fa/enable/start", url.Values{"password": {testPassword}, "csrf_token": {credentials.CSRFToken}})
			request.Header.Del("Cookie")
			request.AddCookie(&http.Cookie{Name: AccessCookieName, Value: credentials.AccessToken})
			request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: credentials.CSRFToken})
			response := httptest.NewRecorder()
			b.BeginEnable(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			var start struct {
				Transaction string
				Options     protocol.CredentialCreation
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &start))
			profile, err := p.Users().GetMFAProfile(t.Context(), u.ID)
			require.NoError(t, err)
			require.False(t, profile.Enabled)
			finish := mfaFormRequest(basePath+"/account/2fa/enable/finish", url.Values{"csrf_token": {credentials.CSRFToken}, "transaction": {start.Transaction},
				"credential": {string(registrationResponseForStart(t, CeremonyStart{Transaction: start.Transaction, Options: &start.Options}))}})
			finish.Header.Del("Cookie")
			finish.AddCookie(&http.Cookie{Name: AccessCookieName, Value: credentials.AccessToken})
			finish.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: credentials.CSRFToken})
			for _, cookie := range response.Result().Cookies() {
				finish.AddCookie(cookie)
			}
			finishResponse := httptest.NewRecorder()
			b.FinishEnable(finishResponse, finish)
			require.Equal(t, http.StatusSeeOther, finishResponse.Code)
			require.Equal(t, basePath+"/account", finishResponse.Header().Get("Location"))
			for _, cookie := range finishResponse.Result().Cookies() {
				if cookie.Name == RefreshCookieName {
					require.Equal(t, basePath+RefreshPath, cookie.Path)
				} else {
					require.Equal(t, basePath+"/", cookie.Path)
				}
			}
			profile, err = p.Users().GetMFAProfile(t.Context(), u.ID)
			require.NoError(t, err)
			require.True(t, profile.Enabled)
		})
	}
}

func TestMFAAccountHTTPDisableWithPasswordAndConfirmation(t *testing.T) {
	p, s, u, private, key, _ := enrolledTestService(t)
	ctx := withMFABrowser(t.Context(), "login-browser", "csrf")
	pending, err := s.Login(ctx, u.Username, []byte(testPassword))
	require.NoError(t, err)
	credentials, err := s.FinishLogin(ctx, pending.Pending.Transaction, "login-browser", "csrf", assertionResponseForStart(t, *pending.Pending, key, private, 0x05, 1))
	require.NoError(t, err)
	b, err := NewBrowser(s, HTTPConfig{TrustedOrigin: "https://example.org", SecureCookies: true})
	require.NoError(t, err)
	r := mfaFormRequest("/account/2fa/disable", url.Values{"password": {testPassword}, "confirm": {"on"}, "csrf_token": {credentials.CSRFToken}})
	r.Header.Del("Cookie")
	r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: credentials.AccessToken})
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: credentials.CSRFToken})
	w := httptest.NewRecorder()
	b.Disable(w, r)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/account", w.Header().Get("Location"))
	profile, err := p.Users().GetMFAProfile(ctx, u.ID)
	require.NoError(t, err)
	require.False(t, profile.Enabled)
	_, err = s.ValidateAccess(ctx, credentials.AccessToken)
	require.ErrorIs(t, err, ErrUnauthenticated)
}
