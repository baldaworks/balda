package managementhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientMCPDefinitionLifecycleAndRedaction(t *testing.T) {
	fixture := &mcpFixture{items: []mcpcmd.Item{
		{Connection: mcpcmd.Connection{ID: "configured", PublicID: "configured", Source: mcpcmd.SourceConfig, CurrentRevisionID: "cfg", Enabled: true, Version: 1}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://example.test", Headers: map[string]mcpcmd.ValueBinding{"Authorization": {Kind: mcpcmd.ValueProtected}}}, Status: mcpcmd.StatusReady, Authorization: mcpcmd.GrantAuthorized},
		{Connection: mcpcmd.Connection{ID: "managed", PublicID: "managed", Source: mcpcmd.SourceManaged, CurrentRevisionID: "rev", Enabled: true, Version: 2, PublishedVersion: 1}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: "agent"}, Status: mcpcmd.StatusPending, ToolCount: 7, Recovery: mcpcmd.RecoveryCaptureRequired, LastDiagnostic: &mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 7, CheckedAt: time.Now().UTC()}},
		{Connection: mcpcmd.Connection{ID: "deleted", PublicID: "deleted", Source: mcpcmd.SourceManaged, CurrentRevisionID: "old", Deleted: true, Version: 3}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: "old"}, Status: mcpcmd.StatusDeleted},
	}}
	server := httptest.NewServer(newProviderTestHandlerWithMCP(t, fixture))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)
	inventory, err := client.ListMCPConnectionsWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Items) != 3 {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	if strings.Contains(string(inventory.Body), "secret-canary") {
		t.Fatalf("inventory leaked secret: %s", inventory.Body)
	}
	if inventory.JSON200.Items[1].PublishedVersion != 1 || inventory.JSON200.Items[1].Recovery == nil || inventory.JSON200.Items[1].LastDiagnostic == nil || inventory.JSON200.Items[1].ToolCount != 7 {
		t.Fatalf("safe projection = %#v", inventory.JSON200.Items[1])
	}
	if inventory.JSON200.Items[1].ActiveAttempt == nil || inventory.JSON200.Items[1].ActiveAttempt.AttemptID != "active-attempt" {
		t.Fatalf("active attempt = %#v", inventory.JSON200.Items[1].ActiveAttempt)
	}
	value := "secret-canary"
	request := managementv1.MCPCreateRequest{PublicID: "created", Enabled: true, Definition: managementv1.MCPDefinition{Transport: managementv1.Stdio, Command: stringPointerForTest("agent"), Targets: managementv1.MCPTargets{All: true, Providers: []string{}}}, Env: &managementv1.MCPValueEdits{"TOKEN": {Operation: managementv1.MCPValueEditOperationSet, Kind: managementv1.MCPValueEditKindProtected, Value: &value}}, Headers: &managementv1.MCPValueEdits{}}
	created, err := client.CreateMCPConnectionWithResponse(t.Context(), request, write)
	if err != nil || created.JSON200 == nil {
		t.Fatalf("create = %#v, %v", created, err)
	}
	if strings.Contains(string(created.Body), value) || fixture.created.Values.Env["TOKEN"].Value != value {
		t.Fatalf("protected value boundary failed: %s", created.Body)
	}
	if fixture.created.Authority.UserID != "admin" || fixture.created.Authority.WebAuthSessionID == "" {
		t.Fatalf("authority = %#v", fixture.created.Authority)
	}
	forbidden, err := client.UpdateMCPConnectionWithResponse(t.Context(), "configured", managementv1.MCPUpdateRequest{ExpectedVersion: 1, Enabled: true, Definition: request.Definition, Env: &managementv1.MCPValueEdits{}, Headers: &managementv1.MCPValueEdits{}}, write)
	if err != nil || forbidden.StatusCode() != http.StatusForbidden {
		t.Fatalf("configured update = %#v, %v", forbidden, err)
	}
	configuredDelete, err := client.DeleteMCPConnectionWithResponse(t.Context(), "configured", managementv1.MCPSelectionRequest{ExpectedVersion: 1}, write)
	if err != nil || configuredDelete.StatusCode() != http.StatusForbidden {
		t.Fatalf("configured delete = %#v, %v", configuredDelete, err)
	}
	missing, err := client.UpdateMCPConnectionWithResponse(t.Context(), "missing", managementv1.MCPUpdateRequest{ExpectedVersion: 1, Enabled: true, Definition: request.Definition, Env: &managementv1.MCPValueEdits{}, Headers: &managementv1.MCPValueEdits{}}, write)
	if err != nil || missing.StatusCode() != http.StatusNotFound {
		t.Fatalf("missing update = %#v, %v", missing, err)
	}
	deletedConflict, err := client.UpdateMCPConnectionWithResponse(t.Context(), "deleted", managementv1.MCPUpdateRequest{ExpectedVersion: 3, Definition: request.Definition, Env: &managementv1.MCPValueEdits{}, Headers: &managementv1.MCPValueEdits{}}, write)
	if err != nil || deletedConflict.StatusCode() != http.StatusConflict {
		t.Fatalf("deleted update = %#v, %v", deletedConflict, err)
	}
	forged := request
	revision := "forged"
	forged.Definition.ConfigRevision = &revision
	rejected, err := client.CreateMCPConnectionWithResponse(t.Context(), forged, write)
	if err != nil || rejected.StatusCode() != http.StatusBadRequest {
		t.Fatalf("forged config revision = %#v, %v", rejected, err)
	}
	withoutCSRF, err := client.SelectMCPConnectionWithResponse(t.Context(), "managed", managementv1.MCPSelectionRequest{ExpectedVersion: 2, Enabled: false}, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden {
		t.Fatalf("selection without CSRF = %#v, %v", withoutCSRF, err)
	}
	probe, err := client.ProbeMCPConnectionWithResponse(t.Context(), request, write)
	if err != nil || probe.JSON200 == nil || probe.JSON200.ToolCount != 3 {
		t.Fatalf("probe = %#v, %v", probe, err)
	}
	if fixture.createCount != 1 || fixture.probeCount != 1 {
		t.Fatalf("probe persisted: creates=%d probes=%d", fixture.createCount, fixture.probeCount)
	}
	updateRequest := managementv1.MCPUpdateRequest{ExpectedVersion: 2, Enabled: true, Definition: request.Definition, Env: &managementv1.MCPValueEdits{}, Headers: &managementv1.MCPValueEdits{}}
	staleRequest := updateRequest
	staleRequest.ExpectedVersion = 1
	stale, err := client.UpdateMCPConnectionWithResponse(t.Context(), "managed", staleRequest, write)
	if err != nil || stale.StatusCode() != http.StatusConflict {
		t.Fatalf("stale update = %#v, %v", stale, err)
	}
	updated, err := client.UpdateMCPConnectionWithResponse(t.Context(), "managed", updateRequest, write)
	if err != nil || updated.JSON200 == nil {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	probedUpdate, err := client.ProbeMCPConnectionUpdateWithResponse(t.Context(), "managed", updateRequest, write)
	if err != nil || probedUpdate.JSON200 == nil {
		t.Fatalf("probe update = %#v, %v", probedUpdate, err)
	}
	selected, err := client.SelectMCPConnectionWithResponse(t.Context(), "managed", managementv1.MCPSelectionRequest{ExpectedVersion: 2, Enabled: false}, write)
	if err != nil || selected.JSON200 == nil {
		t.Fatalf("select = %#v, %v", selected, err)
	}
	diagnostic, err := client.DiagnoseMCPConnectionWithResponse(t.Context(), "managed", write)
	if err != nil || diagnostic.JSON200 == nil || diagnostic.JSON200.ToolCount != 3 {
		t.Fatalf("diagnostic = %#v, %v", diagnostic, err)
	}
	deleted, err := client.DeleteMCPConnectionWithResponse(t.Context(), "managed", managementv1.MCPSelectionRequest{ExpectedVersion: 2, Enabled: false}, write)
	if err != nil || deleted.JSON200 == nil {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
	secret := "client-secret-canary"
	authInput := managementv1.MCPAuthorizationInput{Scopes: []string{"tools"}, ClientID: "client", ClientAuthMethod: managementv1.MCPAuthorizationInputClientAuthMethodNone, ClientSecret: &secret}
	operatorCredentials := generatedProviderLogin(t, client, "operator")
	operatorForbidden, err := client.BeginMCPBrowserAuthorizationWithResponse(t.Context(), "managed", authInput, credentialRequestEditor(operatorCredentials.AccessToken, operatorCredentials.CSRFToken))
	if err != nil || operatorForbidden.StatusCode() != http.StatusForbidden {
		t.Fatalf("WebAuth operator begin = %#v, %v", operatorForbidden, err)
	}
	withoutCSRFBegin, err := client.BeginMCPBrowserAuthorizationWithResponse(t.Context(), "managed", authInput, read)
	if err != nil || withoutCSRFBegin.StatusCode() != http.StatusForbidden {
		t.Fatalf("begin without CSRF = %#v, %v", withoutCSRFBegin, err)
	}
	badOperator, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer wrong-operator")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	operatorDenied, err := badOperator.BeginMCPBrowserAuthorizationWithResponse(t.Context(), "managed", authInput, write)
	if err != nil || operatorDenied.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("begin with invalid operator = %#v, %v", operatorDenied, err)
	}
	browser, err := client.BeginMCPBrowserAuthorizationWithResponse(t.Context(), "managed", authInput, write)
	if err != nil || browser.JSON200 == nil || !strings.Contains(browser.JSON200.AuthorizationURL, "issuer.test") {
		t.Fatalf("begin browser = %#v, %v", browser, err)
	}
	if strings.Contains(string(browser.Body), secret) {
		t.Fatalf("browser response leaked client secret: %s", browser.Body)
	}
	device, err := client.BeginMCPDeviceAuthorizationWithResponse(t.Context(), "managed", authInput, write)
	if err != nil || device.JSON200 == nil || device.JSON200.UserCode != "code-canary" {
		t.Fatalf("begin device = %#v, %v", device, err)
	}
	status, err := client.GetMCPDeviceAuthorizationWithResponse(t.Context(), "device-attempt", read)
	if err != nil || status.JSON200 == nil || strings.Contains(string(status.Body), "code-canary") {
		t.Fatalf("device status = %#v, %v", status, err)
	}
	state, code := "state", "code"
	callback := managementv1.MCPBrowserCallback{State: &state, Code: &code, Issuer: "issuer", Denied: false}
	completed, err := client.CompleteMCPBrowserAuthorizationWithResponse(t.Context(), callback, read)
	if err != nil || completed.JSON200 == nil {
		t.Fatalf("callback = %#v, %v", completed, err)
	}
	replayed, err := client.CompleteMCPBrowserAuthorizationWithResponse(t.Context(), callback, read)
	if err != nil || replayed.StatusCode() != http.StatusNotFound {
		t.Fatalf("callback replay = %#v, %v", replayed, err)
	}
	onboardRequest := managementv1.MCPAuthorizationOnboardRequest{Creation: request, Authorization: authInput}
	onboardRequest.Authorization.ClientID = "fail"
	partial, err := client.OnboardMCPBrowserAuthorizationWithResponse(t.Context(), onboardRequest, write)
	if err != nil || partial.JSON200 == nil || partial.JSON200.Item.ConnectionID == "" || partial.JSON200.StartError == nil {
		t.Fatalf("partial onboarding = %#v, %v", partial, err)
	}
	cancelled, err := client.CancelMCPAuthorizationWithResponse(t.Context(), "active-attempt", write)
	if err != nil || cancelled.StatusCode() != http.StatusNoContent {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
	cancelConflict, err := client.CancelMCPAuthorizationWithResponse(t.Context(), "conflict", write)
	if err != nil || cancelConflict.StatusCode() != http.StatusConflict {
		t.Fatalf("cancel conflict = %#v, %v", cancelConflict, err)
	}
	for name, call := range map[string]func() (int, error){
		"cancel": func() (int, error) {
			response, callErr := client.CancelMCPAuthorizationWithResponse(t.Context(), "active-attempt", read)
			return response.StatusCode(), callErr
		},
		"disconnect": func() (int, error) {
			response, callErr := client.DisconnectMCPAuthorizationWithResponse(t.Context(), "managed", read)
			return response.StatusCode(), callErr
		},
		"retry": func() (int, error) {
			response, callErr := client.RetryMCPAuthorizationWithResponse(t.Context(), "managed", managementv1.MCPRetryAuthorizationRequest{ExpectedRevisionID: "rev"}, read)
			return response.StatusCode(), callErr
		},
	} {
		t.Run(name+" without CSRF", func(t *testing.T) {
			status, callErr := call()
			if callErr != nil || status != http.StatusForbidden {
				t.Fatalf("status = %d, %v", status, callErr)
			}
		})
	}
	disconnected, err := client.DisconnectMCPAuthorizationWithResponse(t.Context(), "managed", write)
	if err != nil || disconnected.StatusCode() != http.StatusNoContent {
		t.Fatalf("disconnect = %#v, %v", disconnected, err)
	}
	disconnectConflict, err := client.DisconnectMCPAuthorizationWithResponse(t.Context(), "conflict", write)
	if err != nil || disconnectConflict.StatusCode() != http.StatusConflict {
		t.Fatalf("disconnect conflict = %#v, %v", disconnectConflict, err)
	}
	retried, err := client.RetryMCPAuthorizationWithResponse(t.Context(), "managed", managementv1.MCPRetryAuthorizationRequest{ExpectedRevisionID: "rev"}, write)
	if err != nil || retried.JSON200 == nil {
		t.Fatalf("retry = %#v, %v", retried, err)
	}
	staleRetry, err := client.RetryMCPAuthorizationWithResponse(t.Context(), "managed", managementv1.MCPRetryAuthorizationRequest{ExpectedRevisionID: "stale"}, write)
	if err != nil || staleRetry.StatusCode() != http.StatusConflict {
		t.Fatalf("stale retry = %#v, %v", staleRetry, err)
	}
}

type mcpFixture struct {
	items         []mcpcmd.Item
	created       mcpcmd.CreateDefinition
	createCount   int
	probeCount    int
	callbackCount int
}

func (f *mcpFixture) Inventory(context.Context) ([]mcpcmd.Item, error) { return f.items, nil }
func (f *mcpFixture) ProviderIDs(context.Context) ([]string, error)    { return []string{"provider"}, nil }
func (f *mcpFixture) Create(_ context.Context, r mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	f.created = r
	f.createCount++
	return mcpcmd.Item{Connection: mcpcmd.Connection{ID: "created", PublicID: r.PublicID, Source: mcpcmd.SourceManaged, CurrentRevisionID: "new", Enabled: true, Version: 1}, Definition: r.Definition, Status: mcpcmd.StatusPending}, nil
}
func (f *mcpFixture) Update(_ context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	if request.ExpectedVersion != 2 {
		return mcpcmd.Item{}, mcpcmd.ErrConflict
	}
	return f.items[1], nil
}
func (f *mcpFixture) SetEnabled(context.Context, mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	return f.items[1], nil
}
func (f *mcpFixture) Delete(context.Context, mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	return f.items[1], nil
}
func (f *mcpFixture) Probe(_ context.Context, r mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	f.probeCount++
	return mcpcmd.Item{Connection: mcpcmd.Connection{ID: "probe", PublicID: r.PublicID, Source: mcpcmd.SourceManaged, CurrentRevisionID: "probe", Version: 1}, Definition: r.Definition, Status: mcpcmd.StatusReady, ToolCount: 3}, nil
}
func (f *mcpFixture) ProbeUpdate(context.Context, mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	return f.items[1], nil
}
func (f *mcpFixture) DiagnoseCurrent(context.Context, string, mcpcmd.Authority) (mcpcmd.DiagnosticResult, error) {
	return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 3, CheckedAt: time.Now().UTC()}, nil
}
func (f *mcpFixture) CreateAndBeginBrowser(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BrowserAuthorization, error) {
	item, _ := f.Create(ctx, creation)
	if request.ClientID == "fail" {
		return item, mcpcmd.BrowserAuthorization{}, mcpcmd.ErrUnavailable
	}
	return item, mcpcmd.BrowserAuthorization{ID: "browser-attempt", ConnectionID: item.Connection.ID, AuthorizationURL: "https://issuer.test/authorize?secret=url-canary", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (f *mcpFixture) CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error) {
	item, _ := f.Create(ctx, creation)
	return item, fixtureDevice(item.Connection.ID), nil
}
func (f *mcpFixture) BeginBrowser(context.Context, mcpcmd.BeginAuthorization) (mcpcmd.BrowserAuthorization, error) {
	return mcpcmd.BrowserAuthorization{ID: "browser-attempt", ConnectionID: "managed", AuthorizationURL: "https://issuer.test/authorize?secret=url-canary", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (f *mcpFixture) BeginDevice(context.Context, mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error) {
	return fixtureDevice("managed"), nil
}
func (f *mcpFixture) CompleteBrowser(context.Context, mcpcmd.BrowserCallback) (mcpcmd.Item, error) {
	f.callbackCount++
	if f.callbackCount > 1 {
		return mcpcmd.Item{}, mcpcmd.ErrAuthAttempt
	}
	return f.items[1], nil
}
func (f *mcpFixture) Device(context.Context, string, mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error) {
	device := fixtureDevice("managed")
	device.VerificationURI = ""
	device.VerificationURIComplete = ""
	device.UserCode = ""
	return device, nil
}
func (f *mcpFixture) CurrentAttempt(_ context.Context, id string, _ mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error) {
	if id != "managed" {
		return mcpcmd.AuthorizationAttempt{}, false, nil
	}
	return mcpcmd.AuthorizationAttempt{ID: "active-attempt", ConnectionID: id, ExpiresAt: time.Now().Add(time.Minute), Device: true}, true, nil
}
func (f *mcpFixture) Cancel(_ context.Context, attemptID string, _ mcpcmd.Authority) error {
	if attemptID == "conflict" {
		return mcpcmd.ErrConflict
	}
	return nil
}
func (f *mcpFixture) Disconnect(_ context.Context, connectionID string, _ mcpcmd.Authority) error {
	if connectionID == "conflict" {
		return mcpcmd.ErrConflict
	}
	return nil
}
func (f *mcpFixture) RetryAuthorization(_ context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	if request.ExpectedRevisionID != "rev" {
		return mcpcmd.Item{}, mcpcmd.ErrConflict
	}
	return f.items[1], nil
}
func fixtureDevice(id string) mcpcmd.DeviceAuthorization {
	return mcpcmd.DeviceAuthorization{ID: "device-attempt", ConnectionID: id, VerificationURI: "https://issuer.test/device", VerificationURIComplete: "https://issuer.test/device?code=code-canary", UserCode: "code-canary", ExpiresAt: time.Now().Add(time.Minute), Status: mcpcmd.DevicePending}
}
