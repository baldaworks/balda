package catalogapp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
)

// Discovery has already succeeded. These fresh, actual provider transports
// must reject a subsequent origin change rather than trust discovery alone.
func testActualProviderOriginGuard(t *testing.T, catalog *Runtime, registry *mcpregistry.MapRegistry, snapshotID runtimecatalogcmd.SnapshotID) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"unexpected unguarded provider"}}]}`)
	}))
	defer model.Close()
	t.Setenv("OPENAI_BASE_URL", model.URL)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALDA_MCP_ACP_CHILD_FIXTURE", "1")
	bundled := mcp.NewServer(&mcp.Implementation{Name: "bundled-guard-fixture", Version: "1"}, nil)
	bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
	defer func() { bundledHTTP.CloseClientConnections(); bundledHTTP.Close() }()
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
	for _, providerType := range []string{agentconfig.AgentTypeOpenAI, agentconfig.AgentTypeGenericACP} {
		provider := agentconfig.Config{Type: providerType}
		if providerType == agentconfig.AgentTypeOpenAI {
			provider.OpenAI = &agentconfig.LocalAPIConfig{APIKey: "fixture", Model: "alpha"}
		} else {
			provider.GenericACP = &agentconfig.ACPConfig{Cmd: []string{executable, "-test.run=^TestMCPACPProviderChild$"}}
		}
		providers := map[string]agentconfig.Config{"alpha": provider}
		workspace := t.TempDir()
		builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(providers, registry), ScopedFactory: NewProviderFactory(providers, registry), NormaCfg: runtimeconfig.RuntimeConfig{Providers: providers}})
		skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
		if err != nil {
			t.Fatal(err)
		}
		manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: "alpha", WorkingDir: workspace, StateDir: t.TempDir(), CapabilityBinder: &sessionCapabilityBinder{catalog: catalog, skills: skills, providers: providers}, MCPRegistry: registry, Logger: zerolog.Nop()})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		runtime, err := manager.RuntimeForSession(ctx, baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: string(snapshotID)})
		if err == nil {
			created, createErr := builder.CreateRuntimeSession(ctx, runtime, "alpha", "fixture-user", "guard-session", workspace, baldaagent.RuntimeSessionContext{BaldaSessionID: "guard-fixture"})
			err = createErr
			if err == nil {
				_, err = runProviderTurn(ctx, runtime, created.ID(), "invoke worker")
			}
		}
		cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = manager.Stop(closeCtx)
		closeCancel()
		if err == nil {
			t.Fatalf("actual %s provider accepted a foreign origin after successful discovery", providerType)
		}
	}
}
