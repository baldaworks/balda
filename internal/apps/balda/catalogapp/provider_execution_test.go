package catalogapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

func testHostedPoolInvocation(t *testing.T, catalog *Runtime, registry *mcpregistry.MapRegistry, snapshotID runtimecatalogcmd.SnapshotID, source mcpcmd.Source, worker *workerGrantFixture) {
	t.Helper()
	var configuredIDs []string
	if source == mcpcmd.SourceConfig {
		configuredIDs = []string{"worker-tools"}
	}
	testHostedPoolInvocationWithTools(t, catalog, registry, snapshotID, configuredIDs, worker, []string{"echo"}, nil, false)
}

func testHostedPoolInvocationWithTools(t *testing.T, catalog *Runtime, registry *mcpregistry.MapRegistry, snapshotID runtimecatalogcmd.SnapshotID, configuredIDs []string, worker *workerGrantFixture, names []string, observe providerLifecycleObserver, poolOnly bool) {
	t.Helper()
	var excludedLeaf atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Model == "beta" {
			if len(request.Tools) != 0 {
				t.Error("pool beta member received alpha-only MCP tools")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"beta has no worker tool"}}]}`)
			return
		}
		if excludedLeaf.Load() {
			if len(request.Tools) != 0 {
				t.Error("direct alpha member received pool-only MCP tools")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"alpha has no worker tool"}}]}`)
			return
		}
		if len(request.Tools) != len(names) {
			t.Error("pool alpha member did not receive exactly its selected worker tools")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"worker tool missing"}}]}`)
			return
		}
		for _, name := range names {
			found := false
			for _, tool := range request.Tools {
				found = found || tool.Function.Name == name
			}
			if !found {
				t.Errorf("hosted provider is missing selected tool %s", name)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if len(request.Messages) > 0 {
			message := request.Messages[len(request.Messages)-1]
			if message.Role == "tool" {
				if !strings.Contains(string(message.Content), "actual hosted tool") {
					t.Error("hosted provider did not receive the real MCP tool result")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hosted tool completed"}}]}`)
				return
			}
		}
		calls := make([]any, 0, len(names))
		for _, name := range names {
			calls = append(calls, map[string]any{"id": "call-" + name, "type": "function", "function": map[string]string{"name": name, "arguments": `{"text":"actual hosted tool"}`}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": calls}}}})
	}))
	defer model.Close()
	t.Setenv("OPENAI_BASE_URL", model.URL)
	bundled := mcp.NewServer(&mcp.Implementation{Name: "bundled-fixture", Version: "1"}, nil)
	bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
	defer func() { bundledHTTP.CloseClientConnections(); bundledHTTP.Close() }()
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
	providers := map[string]agentconfig.Config{
		"pool-alpha": {Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"alpha"}}},
		"pool-beta":  {Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"beta"}}},
		"alpha":      {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "alpha"}},
		"beta":       {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "beta"}},
	}
	if len(configuredIDs) > 0 {
		alpha := providers["alpha"]
		alpha.MCPServers = append([]string(nil), configuredIDs...)
		providers["alpha"] = alpha
	}
	skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	binder := &sessionCapabilityBinder{catalog: catalog, skills: skills, providers: providers}
	providerIDs := []string{"alpha", "pool-alpha", "pool-beta", "root-alpha"}
	if poolOnly {
		providers["pool-shared-alpha"] = agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"alpha"}}}
		providerIDs = append(providerIDs, "pool-shared-alpha", "alpha")
	}
	scopedFactory := NewProviderFactory(providers, registry)
	for _, providerID := range providerIDs {
		excludedLeaf.Store(poolOnly && (providerID == "alpha" || providerID == "pool-shared-alpha"))
		var check func(bool)
		if observe != nil {
			check = observe("hosted/" + providerID)
		}
		root := providerID == "root-alpha"
		if root {
			providerID = "pool-alpha"
		}
		workspace := t.TempDir()
		builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(providers, registry), ScopedFactory: scopedFactory, NormaCfg: runtimeconfig.RuntimeConfig{Providers: providers}})
		manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{
			Builder:         builder,
			BaldaProviderID: providerID, WorkingDir: workspace, StateDir: t.TempDir(),
			CapabilityBinder: binder, MCPRegistry: registry, Logger: zerolog.Nop(),
		})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var runtime *baldaagent.BuiltRuntime
		if root {
			runtime, err = manager.Runtime(ctx)
		} else {
			runtime, err = manager.RuntimeForSession(ctx, baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: string(snapshotID)})
		}
		if err != nil {
			cancel()
			t.Fatalf("hosted %s runtime construction (root=%t): %v", providerID, root, err)
		}
		created, err := builder.CreateRuntimeSession(ctx, runtime, providerID, "fixture-user", "provider-session", workspace, baldaagent.RuntimeSessionContext{BaldaSessionID: "hosted-fixture"})
		if err != nil {
			cancel()
			_ = manager.Stop(ctx)
			t.Fatal(err)
		}
		final, err := runProviderTurn(ctx, runtime, created.ID(), "invoke worker")
		if err == nil && worker != nil && providerID == "alpha" {
			before := worker.renewals.Load()
			worker.expire(t)
			final, err = runProviderTurn(ctx, runtime, created.ID(), "invoke worker again")
			if worker.renewals.Load() != before+1 || runtime.RuntimeSnapshotID != string(snapshotID) {
				t.Error("established hosted session did not renew once within its retained pin")
			}
		}
		if check != nil {
			check(false)
		}
		closeErr := manager.Stop(ctx)
		if check != nil {
			check(true)
		}
		cancel()
		want := "hosted tool completed"
		if excludedLeaf.Load() {
			want = "alpha has no worker tool"
		}
		if providerID == "pool-beta" {
			want = "beta has no worker tool"
		}
		if err != nil || closeErr != nil || !strings.Contains(final, want) {
			t.Fatalf("actual hosted %s execution failed: final=%q error=%v close=%v", providerID, final, err, closeErr)
		}
	}
}

func runProviderTurn(ctx context.Context, runtime *baldaagent.BuiltRuntime, sessionID, prompt string) (string, error) {
	var text strings.Builder
	for event, err := range runtime.Runner.Run(ctx, "fixture-user", sessionID, genai.NewContentFromText(prompt, genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			return text.String(), err
		}
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String(), nil
}
