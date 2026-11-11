package browseredge

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	"github.com/baldaworks/balda/internal/apps/balda/accessapp"
	"github.com/baldaworks/balda/internal/apps/balda/auditapp"
	authapp "github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/managementhttp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/stretchr/testify/require"
)

func TestGeneratedCoreLoginAndImmediateRevokeIntegration(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	now := time.Now().UTC()
	hash, err := userpassword.Hash([]byte("correct horse battery staple"))
	require.NoError(t, err)
	user := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Administrator",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Primary: true, Version: 1,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash},
		usercmd.AuditEvent{ID: "create-admin", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
			TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "test", OccurredAt: now}))
	auth, err := browserauth.NewService(provider.Users(), browserauth.Config{AccessTTL: time.Minute, RefreshTTL: time.Hour, Origin: "http://admin.test"})
	require.NoError(t, err)
	invitations, err := authapp.NewBindingInvitations(provider.Users().(usercmd.InvitationStore))
	require.NoError(t, err)
	channels := authapp.NewBindingChannels([]string{"telegram"})
	require.NoError(t, channels.Register(usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: "telegram", Key: "bot-id"}, Name: "Telegram"}))
	coreConfig, err := (managementhttp.Config{Enabled: true, ListenAddr: "127.0.0.1:8097", OperatorToken: "operator-secret"}).Resolve()
	require.NoError(t, err)
	core := httptest.NewServer(coreConfig.Handler(managementhttp.Services{Access: accessapp.NewService(provider.Users()), Audit: auditapp.NewService(provider.Users()), Invitations: invitations, Channels: channels, BrowserAuth: auth}))
	t.Cleanup(core.Close)
	httpClient := core.Client()
	// Race instrumentation makes the deliberately expensive password hash much
	// slower than the production client deadline.
	httpClient.Timeout = time.Minute
	client, err := managementclient.New(managementclient.Config{BaseURL: core.URL, OperatorToken: "operator-secret", HTTPClient: httpClient})
	require.NoError(t, err)
	app := newTestApp(t, client)

	loginPage := httptest.NewRecorder()
	app.Handler().ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/login", nil))
	csrf := responseCookie(t, loginPage, csrfCookie).Value
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}, "csrf_token": {csrf}}
	login := httptest.NewRequest(http.MethodPost, "http://admin.test/balda/backoffice/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Origin", "http://admin.test")
	login.Header.Set("Cookie", csrfCookie+"="+csrf)
	login.RemoteAddr = testRemoteAddr
	loggedIn := httptest.NewRecorder()
	app.Handler().ServeHTTP(loggedIn, login)
	require.Equal(t, http.StatusSeeOther, loggedIn.Code)
	access := responseCookie(t, loggedIn, accessCookie)
	nextCSRF := responseCookie(t, loggedIn, csrfCookie)

	users := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/access/users", nil)
	users.Header.Set("Cookie", accessCookie+"="+access.Value+"; "+csrfCookie+"="+nextCSRF.Value)
	usersResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(usersResponse, users)
	require.Equal(t, http.StatusOK, usersResponse.Code)
	require.Contains(t, usersResponse.Body.String(), "Administrator")

	audit := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/audit?action=web_auth.login.succeeded", nil)
	audit.Header.Set("Cookie", accessCookie+"="+access.Value+"; "+csrfCookie+"="+nextCSRF.Value)
	auditResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(auditResponse, audit)
	require.Equal(t, http.StatusOK, auditResponse.Code)
	require.Contains(t, auditResponse.Body.String(), "web_auth.login.succeeded")
	require.NotContains(t, auditResponse.Body.String(), "legacy_web_auth")

	require.NoError(t, auth.Logout(t.Context(), access.Value))
	overview := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/overview", nil)
	overview.Header.Set("Cookie", accessCookie+"="+access.Value+"; "+csrfCookie+"="+nextCSRF.Value)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, overview)
	require.Equal(t, http.StatusSeeOther, response.Code)
	require.Equal(t, "/balda/backoffice/login", response.Header().Get("Location"))
}
