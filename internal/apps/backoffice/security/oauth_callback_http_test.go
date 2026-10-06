package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestOAuthCallbackCookieScopeAndBoundedExpiration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, base, origin string
		secure             bool
		access, attempt    time.Time
		want               time.Time
	}{
		{"HTTPS access deadline", "", "https://backoffice.example", true, now.Add(time.Minute), now.Add(2 * time.Minute), now.Add(time.Minute)},
		{"HTTPS attempt deadline with base path", "/balda", "https://backoffice.example", true, now.Add(2 * time.Minute), now.Add(time.Minute), now.Add(time.Minute)},
		{"local HTTP", "/balda", "http://localhost:8095", false, now.Add(time.Minute), now.Add(2 * time.Minute), now.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service := &fakeBrowserService{validate: func(context.Context, string) (Principal, error) {
				return Principal{AccessExpiresAt: tc.access}, nil
			}}
			b, err := NewBrowser(service, HTTPConfig{TrustedOrigin: tc.origin, SecureCookies: tc.secure, BasePath: tc.base})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, tc.base+"/mcp/connections/worker/oauth/browser", nil)
			r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: "current-access"})
			w := httptest.NewRecorder()
			if err := b.PreserveOAuthCallbackCredential(w, r, tc.attempt); err != nil {
				t.Fatal(err)
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("callback cookies = %d, want 1", len(cookies))
			}
			c := cookies[0]
			if c.Name != OAuthCallbackCookieName || c.Value != "current-access" || c.Path != tc.base+"/mcp/oauth/callback" || c.Domain != "" || !c.HttpOnly || c.Secure != tc.secure || c.SameSite != http.SameSiteLaxMode || !c.Expires.Equal(tc.want) {
				t.Fatal("callback cookie lost host-only scope, native return policy, or canonical deadline")
			}
		})
	}
}

func TestOAuthCallbackCredentialOnlyRestoresExactNativeCallback(t *testing.T) {
	t.Parallel()
	b, err := NewBrowser(&fakeBrowserService{}, HTTPConfig{TrustedOrigin: "https://backoffice.example", SecureCookies: true, BasePath: "/balda"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, access, want string
		ordinary, cleared                bool
	}{
		{"native callback", "GET", "/balda/mcp/oauth/callback", "", "callback-access", false, true},
		{"ordinary access preferred", "GET", "/balda/mcp/oauth/callback", "other-family", "other-family", true, true},
		{"empty ordinary access fails closed", "GET", "/balda/mcp/oauth/callback", "", "", true, true},
		{"metadata page", "GET", "/balda/mcp", "", "", false, false},
		{"callback child path", "GET", "/balda/mcp/oauth/callback/child", "", "", false, false},
		{"unprefixed callback", "GET", "/mcp/oauth/callback", "", "", false, false},
		{"POST callback", "POST", "/balda/mcp/oauth/callback", "", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.AddCookie(&http.Cookie{Name: OAuthCallbackCookieName, Value: "callback-access"})
			if tc.ordinary {
				r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: tc.access})
			}
			w := httptest.NewRecorder()
			restored := b.RestoreOAuthCallbackCredential(w, r)
			var access string
			if c, err := restored.Cookie(AccessCookieName); err == nil {
				access = c.Value
			}
			if access != tc.want {
				t.Fatal("callback credential escaped its route or replaced ordinary access")
			}
			cookies := w.Result().Cookies()
			if tc.cleared {
				assertClearedCookie(t, cookies, OAuthCallbackCookieName, "/balda/mcp/oauth/callback")
				if !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Domain != "" {
					t.Fatal("callback clearing lost its cookie scope")
				}
			} else if len(cookies) != 0 {
				t.Fatal("another route modified the callback credential")
			}
			if !tc.ordinary {
				if _, err := r.Cookie(AccessCookieName); err == nil {
					t.Fatal("callback restoration mutated the original request")
				}
			}
		})
	}
}

func TestOAuthCallbackCredentialRequiresCurrentCanonicalAccess(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		invalid, expired, revoked bool
		role                      usercmd.Role
		credentialState           usercmd.CredentialState
		want                      int
	}{
		{name: "current access", role: usercmd.RoleAdministrator, credentialState: usercmd.CredentialStateActive, want: http.StatusNoContent},
		{name: "invalid access", invalid: true, role: usercmd.RoleAdministrator, credentialState: usercmd.CredentialStateActive, want: http.StatusUnauthorized},
		{name: "expired access", expired: true, role: usercmd.RoleAdministrator, credentialState: usercmd.CredentialStateActive, want: http.StatusUnauthorized},
		{name: "revoked family", revoked: true, role: usercmd.RoleAdministrator, credentialState: usercmd.CredentialStateActive, want: http.StatusUnauthorized},
		{name: "operator access", role: usercmd.RoleOperator, credentialState: usercmd.CredentialStateActive, want: http.StatusForbidden},
		{name: "restricted access", role: usercmd.RoleAdministrator, credentialState: usercmd.CredentialStateTemporary, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, service, now := newSecurityTestService(t)
			createSecurityTestUser(t, provider.Users(), "admin", "admin", tc.credentialState, tc.role, tc.role == usercmd.RoleAdministrator, now)
			c, err := service.Login(t.Context(), "admin", []byte(testPassword))
			if err != nil {
				t.Fatal(err)
			}
			b := newHTTPTestBrowser(t, service, false, 0)
			access := c.AccessToken
			if tc.invalid {
				access = "invalid-access"
			}
			if tc.expired {
				service.now = func() time.Time { return c.AccessExpiresAt }
			}
			if tc.revoked {
				if err := service.Logout(t.Context(), access); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback", nil)
			r.AddCookie(&http.Cookie{Name: OAuthCallbackCookieName, Value: access})
			w := httptest.NewRecorder()
			r = b.RestoreOAuthCallbackCredential(w, r)
			b.Authenticate(b.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := PrincipalFromContext(r.Context())
				if !ok || p.User.ID != "admin" || !p.AccessExpiresAt.Equal(c.AccessExpiresAt) {
					t.Fatal("callback did not receive the current canonical administrator")
				}
				w.WriteHeader(http.StatusNoContent)
			}))).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("callback guard status = %d, want %d", w.Code, tc.want)
			}
			assertClearedCookie(t, w.Result().Cookies(), OAuthCallbackCookieName, "/mcp/oauth/callback")
		})
	}
}

func TestOAuthCallbackCredentialAllowsNormalAdministrator(t *testing.T) {
	t.Parallel()
	service := &fakeBrowserService{
		validate: func(_ context.Context, access string) (Principal, error) {
			if access != "initiating-access" {
				t.Fatalf("callback access token = %q", access)
			}
			return Principal{User: usercmd.User{Role: usercmd.RoleAdministrator}, Assurance: usercmd.SessionAssuranceNormal}, nil
		},
	}
	b := newHTTPTestBrowser(t, service, false, 0)
	r := httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback", nil)
	r.AddCookie(&http.Cookie{Name: OAuthCallbackCookieName, Value: "initiating-access"})
	w := httptest.NewRecorder()
	r = b.RestoreOAuthCallbackCredential(w, r)
	b.Authenticate(b.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("callback guard status = %d, want %d", w.Code, http.StatusNoContent)
	}
	assertClearedCookie(t, w.Result().Cookies(), OAuthCallbackCookieName, "/mcp/oauth/callback")
}
