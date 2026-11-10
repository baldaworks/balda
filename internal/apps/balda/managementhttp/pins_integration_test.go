package managementhttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func TestManagementMutationsPreserveExactAISessionPins(t *testing.T) {
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
	if err := provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, usercmd.AuditEvent{
		ID: "create-admin", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "test", OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	auth, err := browserauth.NewService(provider.Users(), browserauth.Config{AccessTTL: 15 * time.Minute, RefreshTTL: time.Hour, Origin: "https://example.org"})
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	providerProtector, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	providerService := providers.New(nil, provider.ProviderManagement(), providerProtector)
	mcpCredentials, err := mcpmanage.New(key)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := mcpmanage.NewDefinitions(mcpCredentials, mcpfx.NewDefinitionStore(provider.MCP()), pinConfigured{}, pinCatalog{}, pinProbe{}, pinDiagnostic{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := (Config{Enabled: true, ListenAddr: "127.0.0.1:8081", OperatorToken: "operator-secret"}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	operations := &pinMCPOperations{Operations: mcpbackofficeapp.New(definitions, nil), definitions: definitions}
	server := httptest.NewServer(config.Handler(Services{BrowserAuth: auth, Providers: providerService, MCP: operations}))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	t.Run("provider binding", func(t *testing.T) {
		oldSecret, newSecret := "old-provider-secret", "new-provider-secret"
		created, callErr := client.CreateProviderWithResponse(t.Context(), managementv1.ProviderCreateRequest{ID: "managed", Config: managementv1.ProviderConfig{
			Type: agentconfig.AgentTypeOpenAI, Model: stringPointerForTest("old-model"),
		}, Protected: managementv1.ProviderProtectedEdit{APIKey: managementv1.ProviderSecretEdit{Operation: managementv1.ProviderEditOperationReplace, Value: &oldSecret}, Environment: managementv1.ProviderEnvironmentEdit{Operation: managementv1.ProviderEditOperationKeep}}}, write)
		if callErr != nil || created.StatusCode() != 204 {
			t.Fatalf("create provider = %#v, %v", created, callErr)
		}
		pinned, compileErr := providerService.CompileCurrent(t.Context(), "managed")
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		update, callErr := client.UpdateProviderWithResponse(t.Context(), "managed", managementv1.ProviderUpdateRequest{ExpectedVersion: 1, Config: managementv1.ProviderConfig{
			Type: agentconfig.AgentTypeOpenAI, Model: stringPointerForTest("new-model"),
		}, Protected: managementv1.ProviderProtectedEdit{APIKey: managementv1.ProviderSecretEdit{Operation: managementv1.ProviderEditOperationReplace, Value: &newSecret}, Environment: managementv1.ProviderEnvironmentEdit{Operation: managementv1.ProviderEditOperationKeep}}}, write)
		if callErr != nil || update.StatusCode() != 204 {
			t.Fatalf("update provider = %#v, %v", update, callErr)
		}
		assertProviderPin(t, providerService, pinned.Binding, oldSecret)
		disabled, callErr := client.SelectProviderWithResponse(t.Context(), "managed", managementv1.ProviderSelectionRequest{ExpectedVersion: 2, Enabled: false}, write)
		if callErr != nil || disabled.StatusCode() != 204 {
			t.Fatalf("disable provider = %#v, %v", disabled, callErr)
		}
		assertProviderPin(t, providerService, pinned.Binding, oldSecret)
		deleted, callErr := client.DeleteProviderWithResponse(t.Context(), "managed", managementv1.ProviderDeleteRequest{ExpectedVersion: 3}, write)
		if callErr != nil || deleted.StatusCode() != 204 {
			t.Fatalf("delete provider = %#v, %v", deleted, callErr)
		}
		assertProviderPin(t, providerService, pinned.Binding, oldSecret)
	})

	t.Run("MCP snapshot", func(t *testing.T) {
		oldSecret, newSecret := "old-mcp-secret", "new-mcp-secret"
		created, callErr := client.CreateMCPConnectionWithResponse(t.Context(), managementv1.MCPCreateRequest{PublicID: "worker", Enabled: true,
			Definition: managementv1.MCPDefinition{Transport: managementv1.Stdio, Command: stringPointerForTest("old-command"), Targets: managementv1.MCPTargets{All: true, Providers: []string{}}},
			Env:        &managementv1.MCPValueEdits{"WORKER_SECRET": {Operation: managementv1.MCPValueEditOperationSet, Kind: managementv1.MCPValueEditKindProtected, Value: &oldSecret}}, Headers: &managementv1.MCPValueEdits{}}, write)
		if callErr != nil || created.JSON200 == nil {
			t.Fatalf("create MCP = %#v, %v", created, callErr)
		}
		item := *created.JSON200
		pin := runtimecatalogcmd.MCPServerDescriptor{ID: runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: item.ConnectionID}, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: item.PublicID}, Revision: runtimecatalogcmd.RevisionID(item.CurrentRevisionID), Name: item.PublicID, Transport: "stdio", ConfigRef: item.ConnectionID}
		resolver := &mcpfx.ManagedResolver{Store: provider.MCP(), Values: mcpCredentials}
		update, callErr := client.UpdateMCPConnectionWithResponse(t.Context(), item.ConnectionID, managementv1.MCPUpdateRequest{ExpectedVersion: 1, Enabled: true,
			Definition: managementv1.MCPDefinition{Transport: managementv1.Stdio, Command: stringPointerForTest("new-command"), Targets: managementv1.MCPTargets{All: true, Providers: []string{}}},
			Env:        &managementv1.MCPValueEdits{"WORKER_SECRET": {Operation: managementv1.MCPValueEditOperationSet, Kind: managementv1.MCPValueEditKindProtected, Value: &newSecret}}, Headers: &managementv1.MCPValueEdits{}}, write)
		if callErr != nil || update.JSON200 == nil {
			t.Fatalf("update MCP = %#v, %v", update, callErr)
		}
		assertMCPPin(t, resolver, pin, oldSecret)
		disabled, callErr := client.SelectMCPConnectionWithResponse(t.Context(), item.ConnectionID, managementv1.MCPSelectionRequest{ExpectedVersion: 2, Enabled: false}, write)
		if callErr != nil || disabled.JSON200 == nil {
			t.Fatalf("disable MCP = %#v, %v", disabled, callErr)
		}
		assertMCPPin(t, resolver, pin, oldSecret)
		deleted, callErr := client.DeleteMCPConnectionWithResponse(t.Context(), item.ConnectionID, managementv1.MCPSelectionRequest{ExpectedVersion: 3}, write)
		if callErr != nil || deleted.JSON200 == nil {
			t.Fatalf("delete MCP = %#v, %v", deleted, callErr)
		}
		assertMCPPin(t, resolver, pin, oldSecret)
	})
}

func assertProviderPin(t *testing.T, service *providers.Service, binding providers.Binding, secret string) {
	t.Helper()
	restored, err := service.Restore(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	config := restored.Configs["managed"]
	if config.OpenAI == nil || config.OpenAI.Model != "old-model" || config.OpenAI.APIKey != secret {
		t.Fatalf("restored provider = %#v", config)
	}
}

func assertMCPPin(t *testing.T, resolver *mcpfx.ManagedResolver, pin runtimecatalogcmd.MCPServerDescriptor, secret string) {
	t.Helper()
	launch, err := resolver.ResolveLaunch(t.Context(), pin)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Command != "old-command" || launch.Env["WORKER_SECRET"] != secret {
		t.Fatalf("restored MCP launch = %#v", launch)
	}
}

type pinConfigured struct{}

func (pinConfigured) MCPDefinitions(context.Context) ([]mcpcmd.Item, error) { return nil, nil }
func (pinConfigured) ProviderIDs(context.Context) ([]string, error)         { return []string{"managed"}, nil }

type pinMCPOperations struct {
	*mcpbackofficeapp.Operations
	definitions *mcpmanage.Definitions
}

func (o *pinMCPOperations) Inventory(ctx context.Context) ([]mcpcmd.Item, error) {
	return o.definitions.Inventory(ctx)
}

type pinCatalog struct{}

func (pinCatalog) PublishMCP(_ context.Context, commit func() error) error { return commit() }
func (pinCatalog) MCPHealth(context.Context, mcpcmd.Connection) (mcpcmd.Status, int, error) {
	return mcpcmd.StatusPending, 0, nil
}
func (pinCatalog) RetryMCPAuthorization(context.Context, mcpcmd.AuthBinding) error { return nil }

type pinProbe struct{}

func (pinProbe) ProbeMCP(context.Context, mcpcmd.Revision) (int, error) { return 0, nil }

type pinDiagnostic struct{}

func (pinDiagnostic) DiagnoseMCP(context.Context, mcpcmd.Revision) mcpcmd.DiagnosticResult {
	return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, CheckedAt: time.Now().UTC()}
}
