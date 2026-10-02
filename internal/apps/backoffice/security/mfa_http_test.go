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

func TestMFABrowserPendingLoginAndSensitiveGuards(t *testing.T) {
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
			blocked := httptest.NewRecorder()
			guard.ServeHTTP(blocked, read)
			require.False(t, called)
			require.Equal(t, http.StatusSeeOther, blocked.Code)
			require.Equal(t, basePath+"/auth/step-up", blocked.Header().Get("Location"))
			mutation := mfaFormRequest(basePath+"/access/users", url.Values{"csrf_token": {rotated.CSRFToken}})
			mutation.Header.Set("Origin", "https://example.org")
			mutation.Header.Del("Cookie")
			mutation.AddCookie(&http.Cookie{Name: AccessCookieName, Value: rotated.AccessToken})
			mutation.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: rotated.CSRFToken})
			denied := httptest.NewRecorder()
			_, _, ok := b.AdministratorMutation(denied, mutation)
			require.False(t, ok)
			require.Equal(t, http.StatusForbidden, denied.Code)
			require.Contains(t, denied.Header().Get("Link"), "<"+basePath+"/auth/step-up>")
			// Complete explicit step-up over HTTP without changing the refresh cookie.
			step := mfaFormRequest(basePath+"/auth/step-up/start", url.Values{"csrf_token": {rotated.CSRFToken}})
			step.Header.Del("Cookie")
			step.AddCookie(&http.Cookie{Name: AccessCookieName, Value: rotated.AccessToken})
			step.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: rotated.CSRFToken})
			stepResponse := httptest.NewRecorder()
			b.BeginStepUp(stepResponse, step)
			require.Equal(t, http.StatusOK, stepResponse.Code)
			require.NoError(t, json.Unmarshal(stepResponse.Body.Bytes(), &pending))
			for _, cookie := range stepResponse.Result().Cookies() {
				if cookie.Name == MFACookieName {
					binding = cookie
				}
			}
			start = CeremonyStart{Transaction: pending.Transaction, Options: &pending.Options}
			stepFinish := mfaFormRequest(basePath+"/auth/step-up/finish", url.Values{"csrf_token": {rotated.CSRFToken}, "transaction": {start.Transaction}, "credential": {string(assertionResponseForStart(t, start, key, private, 0x05, 2))}})
			stepFinish.Header.Del("Cookie")
			stepFinish.AddCookie(binding)
			stepFinish.AddCookie(&http.Cookie{Name: AccessCookieName, Value: rotated.AccessToken})
			stepFinish.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: rotated.CSRFToken})
			stepped := httptest.NewRecorder()
			b.FinishStepUp(stepped, stepFinish)
			require.Equal(t, http.StatusSeeOther, stepped.Code)
			for _, cookie := range stepped.Result().Cookies() {
				require.NotEqual(t, RefreshCookieName, cookie.Name)
			}
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
