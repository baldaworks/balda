//go:build integration && (sqlite || postgres)

package state

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func checkEncryptedStateMixedFramesSurviveRestart(t *testing.T, open contractOpener) {
	path := t.TempDir() + "/state.db"
	provider, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if provider != nil {
			_ = provider.Close()
		}
	})

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	// Deprecated root-direct encryption is required only to build retained v1 fixtures.
	//nolint:staticcheck
	legacyProtector, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	legacyMCP, err := mcpmanage.New(legacyProtector, legacyProtector)
	if err != nil {
		t.Fatal(err)
	}
	root, err := secrets.NewRoot(key)
	if err != nil {
		t.Fatal(err)
	}
	revisionProtector, err := root.Protector(secrets.ScopeMCPRevision)
	if err != nil {
		t.Fatal(err)
	}
	grantProtector, err := root.Protector(secrets.ScopeMCPGrant)
	if err != nil {
		t.Fatal(err)
	}
	scopedMCP, err := mcpmanage.New(revisionProtector, grantProtector)
	if err != nil {
		t.Fatal(err)
	}
	providerProtector, err := root.Protector(secrets.ScopeProviderRevision)
	if err != nil {
		t.Fatal(err)
	}

	mcpMutation := contractMCPMutation(t, provider)
	first, err := legacyMCP.PrepareRevision(nil, *mcpMutation.Revision, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
		"Authorization": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "legacy-mcp-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProtectedValues[0] != 1 {
		t.Fatalf("legacy MCP frame = %d, want v1", first.ProtectedValues[0])
	}
	mcpMutation.Revision = &first
	if err := provider.MCP().SaveMCPConnection(t.Context(), mcpMutation); err != nil {
		t.Fatal(err)
	}

	second := first
	second.ID = "revision-2"
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	second, err = scopedMCP.PrepareRevision(&first, second, mcpcmd.ValueEdits{})
	if err != nil {
		t.Fatal(err)
	}
	if second.ProtectedValues[0] != 2 {
		t.Fatalf("scoped MCP frame = %d, want v2", second.ProtectedValues[0])
	}
	mcpMutation.ExpectedVersion = 1
	mcpMutation.Connection.CurrentRevisionID = second.ID
	mcpMutation.Revision = &second
	mcpMutation.Audit.ID = "mcp-scoped-update"
	if err := provider.MCP().SaveMCPConnection(t.Context(), mcpMutation); err != nil {
		t.Fatal(err)
	}

	grant := mcpcmd.Grant{ID: "legacy-grant", Binding: mcpcmd.AuthBinding{
		ConnectionID: mcpMutation.Connection.ID, Resource: second.Definition.URL,
		Issuer: "https://issuer.example.org", ClientID: "legacy-client",
	}, Generation: 1, Status: mcpcmd.GrantAuthRequired, Scopes: []string{"tools"}, TokenEndpointAuthMethod: "none",
		CreatedAt: mcpMutation.Authority.At, UpdatedAt: mcpMutation.Authority.At}
	grantMutation := MCPGrantMutation{ExpectedRevisionID: second.ID, Grant: grant, Operation: mcpcmd.GrantRegister,
		Authority: &mcpMutation.Authority, Audit: usercmd.AuditEvent{ID: "mixed-grant-register", Action: usercmd.AuditActionMCPAuthorizationChanged,
			Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: mcpMutation.Authority.UserID, ActorSessionID: mcpMutation.Authority.SessionID,
			TargetType: usercmd.AuditTargetMCP, TargetID: mcpMutation.Connection.ID, Source: "encryption-compatibility", OccurredAt: mcpMutation.Authority.At}}
	if err := provider.MCP().SaveMCPGrant(t.Context(), grantMutation); err != nil {
		t.Fatal(err)
	}
	grantMutation.ExpectedGeneration = grant.Generation
	grantMutation.Grant.Generation++
	grantMutation.Grant.Status = mcpcmd.GrantAuthorized
	grantMutation.Operation = mcpcmd.GrantAuthorize
	grantMutation.Audit.ID = "mixed-grant-authorize"
	grantMutation.Grant.ProtectedValues, err = legacyMCP.ProtectGrant(grantMutation.Grant, mcpmanage.GrantSecrets{AccessToken: "legacy-access", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	if grantMutation.Grant.ProtectedValues[0] != 1 {
		t.Fatalf("legacy grant frame = %d, want v1", grantMutation.Grant.ProtectedValues[0])
	}
	if err := provider.MCP().SaveMCPGrant(t.Context(), grantMutation); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	providerAudit := func(id string) usercmd.AuditEvent {
		return usercmd.AuditEvent{ID: id, Source: "encryption-compatibility", OccurredAt: now}
	}
	config := agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: "mixed-model"}}
	apiKey := "legacy-provider-secret"
	legacyProviders := providers.New(nil, provider.ProviderManagement(), legacyProtector)
	providerState, err := legacyProviders.Create(t.Context(), providers.Change{ID: "mixed-provider", Config: config,
		Protected: providers.ProtectedEdit{APIKey: &apiKey}, Audit: providerAudit("mixed-provider-v1")})
	if err != nil {
		t.Fatal(err)
	}
	if providerState.Revisions[0].ProtectedValues[0] != 1 {
		t.Fatalf("legacy Provider frame = %d, want v1", providerState.Revisions[0].ProtectedValues[0])
	}
	scopedProviders := providers.New(nil, provider.ProviderManagement(), providerProtector)
	providerState, err = scopedProviders.Update(t.Context(), providers.Change{ExpectedVersion: providerState.Version,
		ID: "mixed-provider", Config: config, Audit: providerAudit("mixed-provider-v2")})
	if err != nil {
		t.Fatal(err)
	}
	if len(providerState.Revisions) != 1 || providerState.Revisions[0].ProtectedValues[0] != 2 {
		t.Fatalf("current Provider frame = %d, want v2", providerState.Revisions[0].ProtectedValues[0])
	}
	retainedProviders, err := provider.ProviderManagement().ListRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(retainedProviders) != 2 || retainedProviders[0].ProtectedValues[0] != 1 || retainedProviders[1].ProtectedValues[0] != 2 {
		t.Fatalf("retained Provider frames = %d, want v1 and v2", len(retainedProviders))
	}

	beforeRevisions, err := provider.MCP().ListMCPRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeGrants, err := provider.MCP().ListMCPGrants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeProviders, err := provider.ProviderManagement().ListRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider = nil
	provider, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := scopedMCP.ValidateCredentials(t.Context(), provider.MCP()); err != nil {
		t.Fatalf("mixed MCP state rejected after restart: %v", err)
	}
	scopedProviders = providers.New(nil, provider.ProviderManagement(), providerProtector)
	if err := scopedProviders.ValidateCredentials(t.Context()); err != nil {
		t.Fatalf("mixed Provider state rejected after restart: %v", err)
	}
	afterRevisions, err := provider.MCP().ListMCPRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	afterGrants, err := provider.MCP().ListMCPGrants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	afterProviders, err := provider.ProviderManagement().ListRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRevisions, beforeRevisions) || !reflect.DeepEqual(afterGrants, beforeGrants) || !reflect.DeepEqual(afterProviders, beforeProviders) {
		t.Fatal("retained-state validation rewrote mixed encrypted state")
	}
}
