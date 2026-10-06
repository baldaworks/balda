package balda

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	acp "github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

func oauthBrowserProviders(t *testing.T) map[string]agentconfig.Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALDA_BACKOFFICE_OAUTH_ACP_CHILD", "1")
	return map[string]agentconfig.Config{
		"hosted": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "oauth-browser"}, MCPServers: []string{"worker-tools"}},
		"acp":    {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{executable, "-test.run=^TestBackofficeOAuthACPChild$"}}, MCPServers: []string{"worker-tools"}},
	}
}

// Adapted from catalogapp's actual provider fixtures: the model asks the hosted
// runtime for a real tool call; the external ACP subprocess consumes session/new.
func verifyOAuthBrowserExecution(t *testing.T, catalog *catalogapp.Runtime, registry *mcpregistry.MapRegistry, binder baldaagent.SessionCapabilityBinder, providers map[string]agentconfig.Config, snapshot runtimecatalogcmd.SnapshotID, resource string) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
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
			t.Error("hosted model request invalid")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != "echo" {
			t.Error("hosted execution did not receive the authorized MCP tool")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == "tool" {
			if !strings.Contains(string(request.Messages[len(request.Messages)-1].Content), "actual hosted tool "+resource) {
				t.Error("hosted runtime did not consume actual MCP result")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hosted tool completed"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-echo","type":"function","function":{"name":"echo","arguments":"{\"text\":\"actual hosted tool\"}"}}]}}]}`)
	}))
	defer model.Close()
	t.Setenv("OPENAI_BASE_URL", model.URL)
	bundled := mcp.NewServer(&mcp.Implementation{Name: "bundled-fixture", Version: "1"}, nil)
	bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
	defer bundledHTTP.Close()
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
	for _, id := range []string{"hosted", "acp"} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		workspace := t.TempDir()
		builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(providers, registry), ScopedFactory: catalogapp.NewProviderFactory(providers, registry), NormaCfg: runtimeconfig.RuntimeConfig{Providers: providers}})
		manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: id, WorkingDir: workspace, StateDir: t.TempDir(), CapabilityBinder: binder, MCPRegistry: registry, Logger: zerolog.Nop()})
		runtime, err := manager.RuntimeForSession(ctx, baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: string(snapshot)})
		if err != nil {
			cancel()
			t.Fatalf("%s authorized runtime: %v", id, err)
		}
		session, err := builder.CreateRuntimeSession(ctx, runtime, id, "fixture-user", "provider-session", workspace, baldaagent.RuntimeSessionContext{BaldaSessionID: "browser-authorized"})
		if err != nil {
			_ = manager.Stop(context.Background())
			cancel()
			t.Fatal(err)
		}
		var final strings.Builder
		for event, runErr := range runtime.Runner.Run(ctx, "fixture-user", session.ID(), genai.NewContentFromText("invoke worker", genai.RoleUser), adkagent.RunConfig{}) {
			if runErr != nil {
				err = runErr
				break
			}
			if event.Content != nil {
				for _, part := range event.Content.Parts {
					final.WriteString(part.Text)
				}
			}
		}
		closeErr := manager.Stop(ctx)
		cancel()
		want := "hosted tool completed"
		if id == "acp" {
			want = "actual ACP tool " + resource
		}
		if err != nil || closeErr != nil || !strings.Contains(final.String(), want) {
			t.Fatalf("actual %s invocation failed: run=%v close=%v", id, err, closeErr)
		}
	}
}

func TestBackofficeOAuthACPChild(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_OAUTH_ACP_CHILD") != "1" {
		t.Skip("external ACP fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &oauthBrowserACP{ctx: ctx, sessions: make(map[acp.SessionId][]*mcp.ClientSession), ready: make(chan struct{})}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.connection = acp.NewAgentSideConnection(p, os.Stdout, os.Stdin)
	close(p.ready)
	<-p.connection.Done()
	cancel()
	for _, sessions := range p.sessions {
		for _, session := range sessions {
			_ = session.Close()
		}
	}
	// The Go test runner must not write into the ACP protocol stream.
	os.Exit(0)
}

type oauthBrowserACP struct {
	ctx        context.Context
	connection *acp.AgentSideConnection
	ready      chan struct{}
	mu         sync.Mutex
	sessions   map[acp.SessionId][]*mcp.ClientSession
}

func (*oauthBrowserACP) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AgentCapabilities: acp.AgentCapabilities{McpCapabilities: acp.McpCapabilities{Http: true, Sse: true}}}, nil
}
func (p *oauthBrowserACP) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	var sessions []*mcp.ClientSession
	complete := false
	defer func() {
		if !complete {
			for _, s := range sessions {
				_ = s.Close()
			}
		}
	}()
	for _, server := range request.McpServers {
		var endpoint string
		var headers []acp.HttpHeader
		var sse bool
		switch {
		case server.Http != nil:
			endpoint, headers = server.Http.Url, server.Http.Headers
		case server.Sse != nil:
			endpoint, headers, sse = server.Sse.Url, server.Sse.Headers, true
		default:
			return acp.NewSessionResponse{}, fmt.Errorf("unsupported fixture transport")
		}
		private := make(map[string]string, len(headers))
		for _, h := range headers {
			private[h.Name] = h.Value
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "external-browser-ACP", Version: "1"}, nil)
		httpClient := &http.Client{Transport: oauthBrowserHeaders{headers: private}}
		var wire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
		if sse {
			wire = &mcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
		}
		session, err := client.Connect(p.ctx, wire, nil)
		if err != nil {
			return acp.NewSessionResponse{}, fmt.Errorf("external ACP initialize failed")
		}
		if _, err := session.ListTools(ctx, &mcp.ListToolsParams{}); err != nil {
			_ = session.Close()
			return acp.NewSessionResponse{}, fmt.Errorf("external ACP discovery failed")
		}
		sessions = append(sessions, session)
	}
	p.mu.Lock()
	id := acp.SessionId(fmt.Sprintf("fixture-%d", len(p.sessions)+1))
	p.sessions[id] = sessions
	p.mu.Unlock()
	complete = true
	return acp.NewSessionResponse{SessionId: id}, nil
}
func (p *oauthBrowserACP) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	<-p.ready
	p.mu.Lock()
	sessions := append([]*mcp.ClientSession(nil), p.sessions[request.SessionId]...)
	p.mu.Unlock()
	text := "worker tool missing"
	for _, session := range sessions {
		tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			return acp.PromptResponse{}, fmt.Errorf("external ACP list failed")
		}
		for _, tool := range tools.Tools {
			if tool.Name != "echo" {
				continue
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "actual ACP tool"}})
			if err != nil || result.IsError || len(result.Content) != 1 {
				return acp.PromptResponse{}, fmt.Errorf("external ACP invocation failed")
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				return acp.PromptResponse{}, fmt.Errorf("external ACP result invalid")
			}
			text = content.Text
		}
	}
	if err := p.connection.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.UpdateAgentMessageText(text)}); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}
func (*oauthBrowserACP) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (*oauthBrowserACP) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (*oauthBrowserACP) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (*oauthBrowserACP) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (*oauthBrowserACP) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (*oauthBrowserACP) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, fmt.Errorf("fixture does not support resume")
}
func (*oauthBrowserACP) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
func (*oauthBrowserACP) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

type oauthBrowserHeaders struct{ headers map[string]string }

func (h oauthBrowserHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	cloned.Header = r.Header.Clone()
	for k, v := range h.headers {
		cloned.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(cloned)
}
