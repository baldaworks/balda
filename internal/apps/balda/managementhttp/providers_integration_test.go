package managementhttp

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/accessapp"
	"github.com/baldaworks/balda/internal/apps/balda/auditapp"
	authapp "github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func TestGeneratedClientProviderLifecycleAndRedaction(t *testing.T) {
	handler := newProviderTestHandler(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)
	operatorCredentials := generatedProviderLogin(t, client, "operator")
	forbidden, err := client.ListProvidersWithResponse(t.Context(), credentialRequestEditor(operatorCredentials.AccessToken, ""))
	if err != nil || forbidden.StatusCode() != http.StatusForbidden {
		t.Fatalf("operator list = %#v, %v", forbidden, err)
	}

	initial, err := client.ListProvidersWithResponse(t.Context(), read)
	if err != nil || initial.JSON200 == nil || len(initial.JSON200.Items) != 1 || initial.JSON200.Items[0].ID != "configured" {
		t.Fatalf("initial inventory = %#v, %v", initial, err)
	}
	if initial.JSON200.Items[0].Source != managementv1.ProviderSourceConfigured || !initial.JSON200.Items[0].HasAPIKey {
		t.Fatalf("configured projection = %#v", initial.JSON200.Items[0])
	}
	configuredUpdate, err := client.UpdateProviderWithResponse(t.Context(), "configured", managementv1.ProviderUpdateRequest{
		ExpectedVersion: initial.JSON200.Version, Config: initial.JSON200.Items[0].Config, Protected: keepProviderSecrets(),
	}, write)
	if err != nil || configuredUpdate.StatusCode() != http.StatusBadRequest {
		t.Fatalf("configured update = %#v, %v", configuredUpdate, err)
	}
	configuredSelection, err := client.SelectProviderWithResponse(t.Context(), "configured", managementv1.ProviderSelectionRequest{
		ExpectedVersion: initial.JSON200.Version, Enabled: false,
	}, write)
	if err != nil || configuredSelection.StatusCode() != http.StatusBadRequest {
		t.Fatalf("configured selection = %#v, %v", configuredSelection, err)
	}
	configuredDelete, err := client.DeleteProviderWithResponse(t.Context(), "configured", managementv1.ProviderDeleteRequest{
		ExpectedVersion: initial.JSON200.Version,
	}, write)
	if err != nil || configuredDelete.StatusCode() != http.StatusBadRequest {
		t.Fatalf("configured delete = %#v, %v", configuredDelete, err)
	}

	apiKey := "provider-api-key-canary"
	create := managementv1.ProviderCreateRequest{ExpectedVersion: initial.JSON200.Version, ID: "managed", Config: managementv1.ProviderConfig{
		Type: agentconfig.AgentTypeOpenAI, Model: stringPointerForTest("gpt-test"),
	}, Protected: managementv1.ProviderProtectedEdit{
		APIKey:      managementv1.ProviderSecretEdit{Operation: managementv1.ProviderEditOperationReplace, Value: &apiKey},
		Environment: managementv1.ProviderEnvironmentEdit{Operation: managementv1.ProviderEditOperationKeep},
	}}
	created, err := client.CreateProviderWithResponse(t.Context(), create, write)
	if err != nil || created.StatusCode() != http.StatusNoContent {
		t.Fatalf("create = %#v, %v", created, err)
	}

	inventory, err := client.ListProvidersWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || inventory.JSON200.Version != 1 {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	managed, ok := providerDTOItem(*inventory.JSON200, "managed")
	if !ok || !managed.HasAPIKey || len(managed.EnvironmentNames) != 0 {
		t.Fatalf("managed projection = %#v", managed)
	}
	raw := string(inventory.Body)
	if strings.Contains(raw, apiKey) {
		t.Fatalf("inventory disclosed %q: %s", apiKey, raw)
	}

	environment := map[string]string{"PRIVATE_CANARY": "environment-secret-canary"}
	createACP := managementv1.ProviderCreateRequest{ExpectedVersion: inventory.JSON200.Version, ID: "managed-acp", Config: managementv1.ProviderConfig{
		Type: agentconfig.AgentTypeCodexACP, Model: stringPointerForTest("gpt-test"),
	}, Protected: managementv1.ProviderProtectedEdit{
		APIKey:      managementv1.ProviderSecretEdit{Operation: managementv1.ProviderEditOperationKeep},
		Environment: managementv1.ProviderEnvironmentEdit{Operation: managementv1.ProviderEditOperationReplace, Values: &environment},
	}}
	createdACP, err := client.CreateProviderWithResponse(t.Context(), createACP, write)
	if err != nil || createdACP.StatusCode() != http.StatusNoContent {
		t.Fatalf("create ACP = %#v, %v", createdACP, err)
	}
	inventory, err = client.ListProvidersWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || inventory.JSON200.Version != 2 {
		t.Fatalf("inventory after ACP create = %#v, %v", inventory, err)
	}
	managedACP, ok := providerDTOItem(*inventory.JSON200, "managed-acp")
	if !ok || managedACP.HasAPIKey || len(managedACP.EnvironmentNames) != 1 || managedACP.EnvironmentNames[0] != "PRIVATE_CANARY" {
		t.Fatalf("managed ACP projection = %#v", managedACP)
	}
	raw = string(inventory.Body)
	for _, secret := range []string{apiKey, environment["PRIVATE_CANARY"]} {
		if strings.Contains(raw, secret) {
			t.Fatalf("inventory disclosed %q: %s", secret, raw)
		}
	}

	stale, err := client.UpdateProviderWithResponse(t.Context(), "managed", managementv1.ProviderUpdateRequest{
		ExpectedVersion: 0, Config: create.Config, Protected: keepProviderSecrets(),
	}, write)
	if err != nil || stale.StatusCode() != http.StatusConflict {
		t.Fatalf("stale update = %#v, %v", stale, err)
	}

	withoutCSRF, err := client.SelectProviderWithResponse(t.Context(), "managed", managementv1.ProviderSelectionRequest{
		ExpectedVersion: inventory.JSON200.Version, Enabled: false,
	}, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden {
		t.Fatalf("selection without CSRF = %#v, %v", withoutCSRF, err)
	}
	selected, err := client.SelectProviderWithResponse(t.Context(), "managed", managementv1.ProviderSelectionRequest{
		ExpectedVersion: inventory.JSON200.Version, Enabled: false,
	}, write)
	if err != nil || selected.StatusCode() != http.StatusNoContent {
		t.Fatalf("selection = %#v, %v", selected, err)
	}

	afterSelection, err := client.ListProvidersWithResponse(t.Context(), read)
	if err != nil || afterSelection.JSON200 == nil {
		t.Fatalf("inventory after selection = %#v, %v", afterSelection, err)
	}
	deleted, err := client.DeleteProviderWithResponse(t.Context(), "managed", managementv1.ProviderDeleteRequest{ExpectedVersion: afterSelection.JSON200.Version}, write)
	if err != nil || deleted.StatusCode() != http.StatusNoContent {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
}

func newProviderTestHandler(t *testing.T) http.Handler {
	return newProviderTestHandlerWithMCP(t, nil)
}

func newProviderTestHandlerWithMCP(t *testing.T, mcp mcpOperations, schedules ...scheduleOperations) http.Handler {
	var scheduleService scheduleOperations
	if len(schedules) > 0 {
		scheduleService = schedules[0]
	}
	return newManagementTestHandler(t, mcp, scheduleService, nil, nil, nil, nil, nil)
}

func newManagementTestHandler(t *testing.T, mcp mcpOperations, schedules scheduleOperations, webhooks webhookOperations, aliases aliasOperations, hooks hookOperations, plugins pluginOperations, skills skillOperations) http.Handler {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Now().UTC()
	hash, err := userpassword.Hash([]byte(browserAuthTestPassword))
	if err != nil {
		t.Fatal(err)
	}
	user := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Administrator", Status: usercmd.StatusActive,
		Role: usercmd.RoleAdministrator, Primary: true, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	audit := usercmd.AuditEvent{ID: "create-admin", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "test", OccurredAt: now}
	if err := provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, audit); err != nil {
		t.Fatal(err)
	}
	operator := usercmd.User{ID: "operator", Username: "operator", NormalizedUsername: "operator", DisplayName: "Operator", Status: usercmd.StatusActive,
		Role: usercmd.RoleOperator, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	operatorAudit := usercmd.AuditEvent{ID: "create-operator", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: operator.ID, Source: "test", OccurredAt: now}
	if err := provider.Users().CreateUser(t.Context(), operator, usercmd.CredentialSecret{UserID: operator.ID, PasswordHash: hash}, operatorAudit); err != nil {
		t.Fatal(err)
	}
	auth, err := browserauth.NewService(provider.Users(), browserauth.Config{AccessTTL: 15 * time.Minute, RefreshTTL: time.Hour, Origin: "https://example.org"})
	if err != nil {
		t.Fatal(err)
	}
	protector, err := secrets.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	configured := map[string]agentconfig.Config{"configured": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "configured-secret", Model: "configured-model"}}}
	providerService := providers.New(configured, provider.ProviderManagement(), protector)
	invitations, err := authapp.NewBindingInvitations(provider.Users().(usercmd.InvitationStore))
	if err != nil {
		t.Fatal(err)
	}
	channels := authapp.NewBindingChannels([]string{"telegram"})
	if err := channels.Register(usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: "telegram", Key: "bot-id"}, Name: "Telegram", BotUsername: "balda_test_bot", CommandsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	config, err := (Config{Enabled: true, ListenAddr: "127.0.0.1:8081", OperatorToken: "operator-secret"}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return config.Handler(Services{Access: accessapp.NewService(provider.Users()), Audit: auditapp.NewService(provider.Users()), Invitations: invitations, Channels: channels, BrowserAuth: auth, Providers: providerService, MCP: mcp, Schedules: schedules, Webhooks: webhooks, Aliases: aliases, Hooks: hooks, Plugins: plugins, Skills: skills})
}

func keepProviderSecrets() managementv1.ProviderProtectedEdit {
	return managementv1.ProviderProtectedEdit{APIKey: managementv1.ProviderSecretEdit{Operation: managementv1.ProviderEditOperationKeep}, Environment: managementv1.ProviderEnvironmentEdit{Operation: managementv1.ProviderEditOperationKeep}}
}

func providerDTOItem(inventory managementv1.ProviderInventory, id string) (managementv1.ProviderItem, bool) {
	for _, item := range inventory.Items {
		if item.ID == id {
			return item, true
		}
	}
	return managementv1.ProviderItem{}, false
}

func stringPointerForTest(value string) *string { return &value }

func generatedProviderLogin(t *testing.T, client *managementv1.ClientWithResponses, username string) managementv1.WebAuthCredentials {
	t.Helper()
	password := browserAuthTestPassword
	binding, csrf := strings.Repeat("p", 32), strings.Repeat("q", 32)
	response, err := client.LoginWebAuthWithResponse(t.Context(), managementv1.LocalLoginRequest{
		Username: username, Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{BrowserBinding: &binding, CSRFToken: &csrf},
	})
	if err != nil || response.JSON200 == nil || response.JSON200.Credentials == nil {
		t.Fatalf("login %q = %#v, %v", username, response, err)
	}
	return *response.JSON200.Credentials
}
