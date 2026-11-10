package browseredge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestMCPPagesAndMutationsUseGeneratedClientSurface(t *testing.T) {
	recovery := managementv1.CaptureRequired
	managed := managementv1.MCPItem{ConnectionID: "managed", PublicID: "managed", Source: managementv1.MCPItemSourceManaged, CurrentRevisionID: "rev", Enabled: false, Version: 2, PublishedVersion: 1, Definition: managementv1.MCPDefinition{Transport: managementv1.Stdio, Directory: stringPointer("/work"), Env: &map[string]managementv1.MCPValueBinding{"TOKEN": {Kind: managementv1.MCPValueBindingKindProtected}}, Targets: managementv1.MCPTargets{All: true, Providers: []string{}}}, Status: managementv1.MCPItemStatusPending, Authorization: "auth_required", Recovery: &recovery}
	service := &mcpServiceFixture{fakeService: &fakeService{}, inventory: managementv1.MCPInventory{ProviderIds: []string{"provider"}, Items: []managementv1.MCPItem{{ConnectionID: "configured", PublicID: "configured", Source: managementv1.MCPItemSourceConfig, CurrentRevisionID: "cfg", Enabled: true, Version: 1, Definition: managementv1.MCPDefinition{Transport: managementv1.HTTP, Targets: managementv1.MCPTargets{All: true, Providers: []string{}}}, Status: managementv1.MCPItemStatusReady, Authorization: "authorized"}, managed}}}
	app := newTestApp(t, service)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://admin.test"+path, nil)
		r.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
		app.Handler().ServeHTTP(w, r)
		return w
	}
	list := get("/balda/backoffice/mcp")
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), "capture_required")
	configured := get("/balda/backoffice/mcp/configured")
	require.Contains(t, configured.Body.String(), "read-only")
	require.NotContains(t, configured.Body.String(), `method="post"`)
	detail := get("/balda/backoffice/mcp/managed")
	require.Contains(t, detail.Body.String(), `value="/work"`)
	require.Contains(t, detail.Body.String(), "TOKEN|protected|keep|")
	require.Contains(t, detail.Body.String(), `name="enabled" value="yes"`)
	create := mutationRequest("/balda/backoffice/mcp", url.Values{"public_id": {"created"}, "transport": {"stdio"}, "command": {"agent"}, "targets_all": {"yes"}, "env": {"TOKEN|protected|set|secret-canary"}, "operation": {"save"}})
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, create)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "secret-canary", *(*service.created.Env)["TOKEN"].Value)
	require.NotContains(t, w.Body.String(), "secret-canary")
	probe := mutationRequest("/balda/backoffice/mcp", url.Values{"public_id": {"candidate"}, "transport": {"stdio"}, "operation": {"probe"}})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, probe)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "No definition was saved")
	update := mutationRequest("/balda/backoffice/mcp/managed", url.Values{"expected_version": {"2"}, "transport": {"stdio"}, "directory": {"/work"}, "env": {"TOKEN|protected|keep|"}, "operation": {"save"}})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, update)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.False(t, service.updated.Enabled)
	require.Equal(t, "/work", *service.updated.Definition.Directory)
	selection := mutationRequest("/balda/backoffice/mcp/managed/selection", url.Values{"expected_version": {"2"}, "enabled": {"yes"}, "confirm": {"yes"}})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, selection)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.True(t, service.selected.Enabled)
	diagnostic := mutationRequest("/balda/backoffice/mcp/managed/diagnostic", url.Values{})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, diagnostic)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "3 tools")
	remove := mutationRequest("/balda/backoffice/mcp/managed/delete", url.Values{"expected_version": {"2"}, "confirm": {"yes"}})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, remove)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.True(t, service.deleted)
	edits, err := mcpValueEdits("X%7CTrace|literal|keep|")
	require.NoError(t, err)
	require.Contains(t, edits, "X|Trace")
	_, err = mcpValueEdits("A|literal|keep|\nA|literal|remove|")
	require.Error(t, err)
}

func TestMCPAuthorizationBrowserAndDeviceBoundary(t *testing.T) {
	service := &mcpServiceFixture{fakeService: &fakeService{}, accessExpiresAt: time.Now().Add(30 * time.Second), inventory: managementv1.MCPInventory{Items: []managementv1.MCPItem{{ConnectionID: "managed", PublicID: "managed", Source: managementv1.MCPItemSourceManaged, CurrentRevisionID: "rev", Enabled: true, Version: 1, Definition: managementv1.MCPDefinition{Transport: managementv1.HTTP, Targets: managementv1.MCPTargets{Providers: []string{}}}, Status: managementv1.MCPItemStatusAuthRequired, Authorization: "auth_required"}}}}
	app := newTestApp(t, service)
	begin := mutationRequest("/balda/backoffice/mcp/managed/authorization/browser", url.Values{"client_auth_method": {"none"}})
	begin.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, begin)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "https://issuer.test/authorize", w.Header().Get("Location"))
	require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	var callbackCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == oauthAccessCookie {
			callbackCookie = cookie
		}
	}
	require.NotNil(t, callbackCookie)
	require.Equal(t, "access", callbackCookie.Value)
	require.WithinDuration(t, service.accessExpiresAt, callbackCookie.Expires, time.Second)
	callback := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/mcp/authorization/callback?state=state&code=code&iss=issuer", nil)
	callback.AddCookie(callbackCookie)
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, callback)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, 1, service.callbackCount)
	require.Equal(t, "access", service.callbackAccess)
	require.NotContains(t, w.Header().Get("Location"), "code")
	require.Equal(t, -1, responseCookie(t, w, oauthAccessCookie).MaxAge)

	malformed := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/mcp/authorization/callback?state=state&code=secret&code=duplicate", nil)
	malformed.AddCookie(callbackCookie)
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, malformed)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.NotContains(t, w.Header().Get("Location"), "secret")
	require.Equal(t, 1, service.callbackCount)
	replay := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/mcp/authorization/callback?state=state&code=code", nil)
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, replay)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.NotContains(t, w.Header().Get("Location"), "code")
	require.Equal(t, 1, service.callbackCount)
	device := mutationRequest("/balda/backoffice/mcp/managed/authorization/device", url.Values{"client_auth_method": {"none"}})
	device.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, device)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "one-time-code")
	status := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/mcp/authorization/device/device-attempt", nil)
	status.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, status)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "one-time-code")
	require.Contains(t, w.Body.String(), "/mcp/managed")

	onboard := mutationRequest("/balda/backoffice/mcp/authorization/browser/onboard", url.Values{"public_id": {"saved"}, "transport": {"stdio"}, "command": {"agent"}, "targets_all": {"yes"}, "client_secret": {"secret-canary"}})
	onboard.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, onboard)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "/mcp/saved")
	require.NotContains(t, w.Body.String(), "secret-canary")

	cancel := mutationRequest("/balda/backoffice/mcp/authorization/attempts/device-attempt/cancel", nil)
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, cancel)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "device-attempt", service.cancelledAttempt)
	disconnect := mutationRequest("/balda/backoffice/mcp/managed/authorization/disconnect", url.Values{"confirm": {"yes"}})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, disconnect)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "managed", service.disconnectedConnection)
	retry := mutationRequest("/balda/backoffice/mcp/managed/authorization/retry", url.Values{"expected_revision_id": {"rev"}})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, retry)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "rev", service.retriedRevision)
}

func TestMCPAuthorizationCallbackCookieUsesShortestDeadline(t *testing.T) {
	for _, test := range []struct {
		name          string
		accessExpires time.Time
		attemptExpiry time.Time
		wantStatus    int
	}{
		{name: "attempt expires first", accessExpires: time.Now().Add(time.Minute), attemptExpiry: time.Now().Add(15 * time.Second), wantStatus: http.StatusSeeOther},
		{name: "expired access", accessExpires: time.Now().Add(-time.Second), attemptExpiry: time.Now().Add(time.Minute), wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &mcpServiceFixture{fakeService: &fakeService{}, accessExpiresAt: test.accessExpires, browserExpiresAt: test.attemptExpiry}
			app := newTestApp(t, service)
			request := mutationRequest("/balda/backoffice/mcp/managed/authorization/browser", url.Values{"client_auth_method": {"none"}})
			request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code)
			if test.wantStatus == http.StatusSeeOther {
				require.WithinDuration(t, test.attemptExpiry, responseCookie(t, response, oauthAccessCookie).Expires, time.Second)
			}
		})
	}
}

func TestMCPBrowserAuthorizationValidatesAccessBeforeSideEffect(t *testing.T) {
	service := &mcpServiceFixture{fakeService: &fakeService{}, principalErr: managementclient.ErrUnavailable}
	app := newTestApp(t, service)
	requests := []*http.Request{
		mutationRequest("/balda/backoffice/mcp/managed/authorization/browser", url.Values{"client_auth_method": {"none"}}),
		mutationRequest("/balda/backoffice/mcp/authorization/browser/onboard", url.Values{"public_id": {"saved"}, "transport": {"stdio"}, "command": {"agent"}, "targets_all": {"yes"}}),
	}
	for _, request := range requests {
		request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
	}
	require.Zero(t, service.beginBrowserCount)
	require.Zero(t, service.onboardBrowserCount)
}

func TestMCPAuthorizationAttemptNavigationDistinguishesBrowserAndDevice(t *testing.T) {
	item := managementv1.MCPItem{ConnectionID: "managed", PublicID: "managed", Source: managementv1.MCPItemSourceManaged, CurrentRevisionID: "rev", Enabled: true, Version: 1, Definition: managementv1.MCPDefinition{Transport: managementv1.HTTP, Targets: managementv1.MCPTargets{}}, ActiveAttempt: &managementv1.MCPAuthorizationAttempt{AttemptID: "attempt", ConnectionID: "managed", ExpiresAt: time.Now().Add(time.Minute)}}
	service := &mcpServiceFixture{fakeService: &fakeService{}, accessExpiresAt: time.Now().Add(time.Minute), inventory: managementv1.MCPInventory{Items: []managementv1.MCPItem{item}}}
	app := newTestApp(t, service)
	get := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/mcp/managed", nil)
		request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		return response
	}
	browser := get()
	require.Equal(t, http.StatusOK, browser.Code)
	require.NotContains(t, browser.Body.String(), "Check authorization")
	service.inventory.Items[0].ActiveAttempt.Device = true
	device := get()
	require.Contains(t, device.Body.String(), "Check authorization")
}

type mcpServiceFixture struct {
	*fakeService
	inventory              managementv1.MCPInventory
	created                managementv1.MCPCreateRequest
	updated                managementv1.MCPUpdateRequest
	selected               managementv1.MCPSelectionRequest
	deleted                bool
	callbackCount          int
	callbackAccess         string
	cancelledAttempt       string
	disconnectedConnection string
	retriedRevision        string
	accessExpiresAt        time.Time
	browserExpiresAt       time.Time
	principalErr           error
	beginBrowserCount      int
	onboardBrowserCount    int
}

func (f *mcpServiceFixture) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	if f.principalErr != nil {
		return managementv1.WebAuthPrincipal{}, f.principalErr
	}
	return managementv1.WebAuthPrincipal{UserID: "admin", Username: "admin", DisplayName: "Administrator", Role: managementv1.Administrator, AccessExpiresAt: f.accessExpiresAt}, nil
}
func (f *mcpServiceFixture) MCPConnections(context.Context, managementclient.Auth) (managementv1.MCPInventory, error) {
	return f.inventory, nil
}
func (f *mcpServiceFixture) CreateMCPConnection(_ context.Context, _ managementclient.Auth, r managementv1.MCPCreateRequest) (managementv1.MCPItem, error) {
	f.created = r
	return mcpResult(r.PublicID, r.Definition), nil
}
func (f *mcpServiceFixture) ProbeMCPConnection(_ context.Context, _ managementclient.Auth, r managementv1.MCPCreateRequest) (managementv1.MCPItem, error) {
	item := mcpResult(r.PublicID, r.Definition)
	item.ToolCount = 3
	item.Status = managementv1.MCPItemStatusReady
	return item, nil
}
func (f *mcpServiceFixture) UpdateMCPConnection(_ context.Context, _ managementclient.Auth, _ string, r managementv1.MCPUpdateRequest) (managementv1.MCPItem, error) {
	f.updated = r
	return f.inventory.Items[len(f.inventory.Items)-1], nil
}
func (f *mcpServiceFixture) ProbeMCPConnectionUpdate(context.Context, managementclient.Auth, string, managementv1.MCPUpdateRequest) (managementv1.MCPItem, error) {
	return f.inventory.Items[1], nil
}
func (f *mcpServiceFixture) SelectMCPConnection(_ context.Context, _ managementclient.Auth, _ string, request managementv1.MCPSelectionRequest) (managementv1.MCPItem, error) {
	f.selected = request
	return f.inventory.Items[1], nil
}

func (f *mcpServiceFixture) DeleteMCPConnection(context.Context, managementclient.Auth, string, managementv1.MCPSelectionRequest) (managementv1.MCPItem, error) {
	f.deleted = true
	return f.inventory.Items[1], nil
}
func (f *mcpServiceFixture) DiagnoseMCPConnection(context.Context, managementclient.Auth, string) (managementv1.MCPDiagnostic, error) {
	return managementv1.MCPDiagnostic{Outcome: managementv1.MCPDiagnosticOutcomeSucceeded, ToolCount: 3, CheckedAt: time.Now()}, nil
}
func mcpResult(id string, d managementv1.MCPDefinition) managementv1.MCPItem {
	return managementv1.MCPItem{ConnectionID: id, PublicID: id, Source: managementv1.MCPItemSourceManaged, CurrentRevisionID: "rev", Enabled: true, Version: 1, Definition: d, Status: managementv1.MCPItemStatusPending, Authorization: ""}
}
func (f *mcpServiceFixture) OnboardMCPBrowser(_ context.Context, _ managementclient.Auth, request managementv1.MCPAuthorizationOnboardRequest) (managementv1.MCPBrowserOnboardResult, error) {
	f.onboardBrowserCount++
	return managementv1.MCPBrowserOnboardResult{Item: mcpResult(request.Creation.PublicID, request.Creation.Definition), StartError: pointer(managementv1.MCPBrowserOnboardResultStartErrorUnavailable)}, nil
}
func (f *mcpServiceFixture) OnboardMCPDevice(context.Context, managementclient.Auth, managementv1.MCPAuthorizationOnboardRequest) (managementv1.MCPDeviceOnboardResult, error) {
	return managementv1.MCPDeviceOnboardResult{}, nil
}
func (f *mcpServiceFixture) BeginMCPBrowser(context.Context, managementclient.Auth, string, managementv1.MCPAuthorizationInput) (managementv1.MCPBrowserAuthorizationStart, error) {
	f.beginBrowserCount++
	expires := f.browserExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(time.Minute)
	}
	return managementv1.MCPBrowserAuthorizationStart{AttemptID: "browser-attempt", ConnectionID: "managed", AuthorizationURL: "https://issuer.test/authorize", ExpiresAt: expires}, nil
}
func (f *mcpServiceFixture) BeginMCPDevice(context.Context, managementclient.Auth, string, managementv1.MCPAuthorizationInput) (managementv1.MCPDeviceAuthorizationStart, error) {
	return managementv1.MCPDeviceAuthorizationStart{AttemptID: "device-attempt", ConnectionID: "managed", VerificationURI: "https://issuer.test/device", UserCode: "one-time-code", ExpiresAt: time.Now().Add(time.Minute), Status: managementv1.MCPDeviceAuthorizationStartStatusPending}, nil
}
func (f *mcpServiceFixture) CompleteMCPBrowser(_ context.Context, auth managementclient.Auth, _ managementv1.MCPBrowserCallback) (managementv1.MCPItem, error) {
	f.callbackCount++
	f.callbackAccess = auth.Access
	return f.inventory.Items[len(f.inventory.Items)-1], nil
}
func (f *mcpServiceFixture) MCPDeviceStatus(context.Context, managementclient.Auth, string) (managementv1.MCPDeviceAuthorizationStatus, error) {
	return managementv1.MCPDeviceAuthorizationStatus{AttemptID: "device-attempt", ConnectionID: "managed", ExpiresAt: time.Now().Add(time.Minute), Status: managementv1.MCPDeviceAuthorizationStatusStatusPending}, nil
}
func (f *mcpServiceFixture) CancelMCPAuthorization(_ context.Context, _ managementclient.Auth, attemptID string) error {
	f.cancelledAttempt = attemptID
	return nil
}
func (f *mcpServiceFixture) DisconnectMCPAuthorization(_ context.Context, _ managementclient.Auth, connectionID string) error {
	f.disconnectedConnection = connectionID
	return nil
}
func (f *mcpServiceFixture) RetryMCPAuthorization(_ context.Context, _ managementclient.Auth, _ string, request managementv1.MCPRetryAuthorizationRequest) (managementv1.MCPItem, error) {
	f.retriedRevision = request.ExpectedRevisionID
	return f.inventory.Items[len(f.inventory.Items)-1], nil
}

func pointer[T any](value T) *T { return &value }
