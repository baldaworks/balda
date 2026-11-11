package balda

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

// The fixture clock is paused before expiry. Its explicit generation barrier
// advances observed expiry together with issuer bearer validity, not wall time.
// SaveGrant delegates unchanged, retaining SQL authority, encryption and CAS.
type oauthControlledExpiryStore struct {
	mcpmanage.GrantStore
	lease             time.Duration
	expiredGeneration atomic.Uint64
}

func (s *oauthControlledExpiryStore) GetMCPGrant(ctx context.Context, binding mcpcmd.AuthBinding) (mcpcmd.Grant, bool, error) {
	grant, found, err := s.GrantStore.GetMCPGrant(ctx, binding)
	if err == nil && found && grant.Status == mcpcmd.GrantAuthorized {
		grant.AccessExpiresAt = time.Now().Add(s.lease)
		if grant.Generation <= s.expiredGeneration.Load() {
			grant.AccessExpiresAt = time.Now().Add(-time.Second)
		}
	}
	return grant, found, err
}

func verifyOAuthBrowserRenewal(t *testing.T, catalog *catalogapp.Runtime, registry *mcpregistry.MapRegistry, binder baldaagent.SessionCapabilityBinder, providers map[string]agentconfig.Config, provider state.Provider, credentials *mcpmanage.Service, store *oauthControlledExpiryStore, issuer *backofficeOAuthIssuer, declined bool) {
	t.Helper()
	pin, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	revision := oauthBrowserConnectionRevision(t, provider, "managed-tools")
	if revision.Definition.AuthBinding == nil || len(revision.Definition.Scopes) != 0 {
		t.Fatal("normal resource-default browser input did not retain its scope intent and discovered binding")
	}
	binding := *revision.Definition.AuthBinding
	grant, found, err := store.GrantStore.GetMCPGrant(t.Context(), binding)
	if err != nil || !found {
		t.Fatalf("read authorized grant: %v", err)
	}
	before, err := credentials.OpenGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	wantScopes := []string{"openid", "offline_access"}
	if declined {
		wantScopes = []string{"openid"}
	}
	if !slices.Equal(grant.Scopes, wantScopes) || (before.RefreshToken == "") != declined || issuer.exchanges.Load() != 1 {
		t.Fatal("normal browser consent did not retain actual resource/renewal authorization")
	}
	bundled := mcp.NewServer(&mcp.Implementation{Name: "bundled-renewal-fixture", Version: "1"}, nil)
	bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
	defer bundledHTTP.Close()
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
	workspace := t.TempDir()
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(providers, registry), ScopedFactory: catalogapp.NewProviderFactory(providers, registry), NormaCfg: runtimeconfig.RuntimeConfig{Providers: providers}})
	manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: "managed-acp", WorkingDir: workspace, StateDir: t.TempDir(), CapabilityBinder: binder, MCPRegistry: registry, Logger: zerolog.Nop()})
	defer func() { _ = manager.Stop(context.Background()) }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	runtime, err := manager.RuntimeForSession(ctx, baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: string(pin.ID)})
	if err != nil {
		t.Fatal(err)
	}
	session, err := builder.CreateFreshRuntimeSession(ctx, runtime, "managed-acp", "fixture-user", "renewal-session", workspace, baldaagent.RuntimeSessionContext{BaldaSessionID: "browser-renewal"})
	if err != nil {
		t.Fatal(err)
	}
	invoke := func() (string, error) {
		var output strings.Builder
		for event, runErr := range runtime.Runner.Run(ctx, "fixture-user", session.ID(), genai.NewContentFromText("invoke worker", genai.RoleUser), adkagent.RunConfig{}) {
			if runErr != nil {
				return output.String(), runErr
			}
			if event.Content != nil {
				for _, part := range event.Content.Parts {
					output.WriteString(part.Text)
				}
			}
		}
		return output.String(), nil
	}
	first, err := invoke()
	if err != nil || !strings.Contains(first, "actual ACP tool /mcp") || issuer.acpCalls.Load() != 1 || issuer.refreshes.Load() != 0 {
		t.Fatalf("actual native tool use before controlled expiry failed: %v", err)
	}
	markerIndex := strings.LastIndex(first, "Native session ")
	if markerIndex < 0 {
		t.Fatal("native invocation did not identify its fixture process/session")
	}
	marker := strings.TrimSpace(first[markerIndex:])
	codeExchanges := issuer.exchanges.Load()
	expire := func(generation uint64) {
		store.expiredGeneration.Store(generation)
		issuer.currentAccess.Store("")
	}
	upstream := issuer.upstreamRequests.Load()
	expire(grant.Generation)
	second, err := invoke()
	if declined {
		if err == nil || issuer.acpCalls.Load() != 1 || issuer.refreshes.Load() != 0 || issuer.upstreamRequests.Load() != upstream {
			t.Fatal("expired nonrenewable grant forwarded upstream or reported native tool success")
		}
	} else {
		if err != nil || !strings.Contains(second, "actual ACP tool /mcp") || !strings.Contains(second, marker) || issuer.acpCalls.Load() != 2 || issuer.refreshes.Load() != 1 {
			t.Fatalf("same native session did not invoke tool with renewed credentials: %v", err)
		}
		renewed, exists, readErr := store.GrantStore.GetMCPGrant(ctx, binding)
		if readErr != nil || !exists || renewed.Generation != grant.Generation+1 || renewed.Binding != grant.Binding || !slices.Equal(renewed.Scopes, wantScopes) {
			t.Fatal("renewal changed identity or lost captured scope")
		}
		after, openErr := credentials.OpenGrant(renewed)
		if openErr != nil || after.AccessToken == before.AccessToken || after.RefreshToken == before.RefreshToken || after.AccessToken != issuer.currentAccess.Load().(string) || after.RefreshToken != issuer.currentRefresh.Load().(string) {
			t.Fatal("renewal did not securely rotate issuer credentials")
		}
		issuer.rejectRefresh.Store(true)
		upstream = issuer.upstreamRequests.Load()
		expire(renewed.Generation)
		if _, err := invoke(); err == nil || issuer.acpCalls.Load() != 2 || issuer.refreshes.Load() != 2 || issuer.upstreamRequests.Load() != upstream {
			t.Fatal("refused renewal forwarded unsigned upstream traffic or claimed tool success")
		}
	}
	terminal, _, err := store.GrantStore.GetMCPGrant(ctx, binding)
	if err != nil || terminal.Status != mcpcmd.GrantAuthRequired {
		t.Fatal("expired or rejected renewal did not require supported reauthorization")
	}
	currentPin, err := catalog.Store().Application()
	currentRevision := oauthBrowserConnectionRevision(t, provider, "managed-tools")
	if err != nil || currentPin.ID != pin.ID || !sameOAuthBrowserRevision(currentRevision, revision) || issuer.exchanges.Load() != codeExchanges {
		t.Fatal("native renewal/rejection repinned the session, changed binding or repeated browser authorization")
	}
	for _, output := range []string{first, second} {
		if strings.Contains(output, "synthetic-browser-") || strings.Contains(output, "synthetic-oauth-header") {
			t.Fatal("native output disclosed upstream credentials")
		}
	}
}
