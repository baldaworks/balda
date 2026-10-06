package catalogapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	acp "github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
)

const (
	alphaID     = "alpha"
	poolAlphaID = "pool-alpha"
)

func testACPInvocation(t *testing.T, catalog *Runtime, registry *mcpregistry.MapRegistry, snapshotID runtimecatalogcmd.SnapshotID, source mcpcmd.Source, worker *workerGrantFixture) {
	t.Helper()
	var configuredIDs []string
	if source == mcpcmd.SourceConfig {
		configuredIDs = []string{"worker-tools"}
	}
	testACPInvocationWithServers(t, catalog, registry, snapshotID, configuredIDs, worker, nil, false, false)
}

func testACPInvocationWithServers(t *testing.T, catalog *Runtime, registry *mcpregistry.MapRegistry, snapshotID runtimecatalogcmd.SnapshotID, configuredIDs []string, worker *workerGrantFixture, observe providerLifecycleObserver, forcedExit, poolOnly bool) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALDA_MCP_ACP_CHILD_FIXTURE", "1")
	bundled := mcp.NewServer(&mcp.Implementation{Name: "bundled-fixture", Version: "1"}, nil)
	bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
	defer func() { bundledHTTP.CloseClientConnections(); bundledHTTP.Close() }()
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
	providers := map[string]agentconfig.Config{
		poolAlphaID: {Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{alphaID}}},
		"pool-beta": {Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"beta"}}},
		alphaID:     {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{executable, "-test.run=^TestMCPACPProviderChild$"}}},
		"beta":      {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{executable, "-test.run=^TestMCPACPProviderChild$"}}},
	}
	if len(configuredIDs) > 0 {
		alpha := providers[alphaID]
		alpha.MCPServers = append([]string(nil), configuredIDs...)
		providers[alphaID] = alpha
	}
	skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	binder := &sessionCapabilityBinder{catalog: catalog, skills: skills, providers: providers}
	providerIDs := []string{alphaID, poolAlphaID, "pool-beta", "root-alpha"}
	if poolOnly {
		providers["pool-shared-alpha"] = agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{alphaID}}}
		providerIDs = append(providerIDs, "pool-shared-alpha", alphaID)
	}
	scopedFactory := NewProviderFactory(providers, registry)
	for _, providerID := range providerIDs {
		var check func(bool)
		if observe != nil {
			check = observe("ACP/" + providerID)
		}
		root := providerID == "root-alpha"
		if root {
			providerID = poolAlphaID
		}
		workspace := t.TempDir()
		builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(providers, registry), ScopedFactory: scopedFactory, NormaCfg: runtimeconfig.RuntimeConfig{Providers: providers}})
		manager := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: providerID, WorkingDir: workspace, StateDir: t.TempDir(), CapabilityBinder: binder, MCPRegistry: registry, Logger: zerolog.Nop()})
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		var runtime *baldaagent.BuiltRuntime
		if root {
			runtime, err = manager.Runtime(ctx)
		} else {
			runtime, err = manager.RuntimeForSession(ctx, baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: string(snapshotID)})
		}
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		created, err := builder.CreateRuntimeSession(ctx, runtime, providerID, "fixture-user", "provider-session", workspace, baldaagent.RuntimeSessionContext{BaldaSessionID: "ACP-fixture"})
		if err != nil {
			cancel()
			_ = manager.Stop(context.Background())
			t.Fatal(err)
		}
		final, err := runProviderTurn(ctx, runtime, created.ID(), "invoke worker")
		if err == nil && worker != nil && providerID == alphaID {
			before := worker.renewals.Load()
			worker.expire(t)
			final, err = runProviderTurn(ctx, runtime, created.ID(), "invoke worker again")
			if worker.renewals.Load() != before+1 || runtime.RuntimeSnapshotID != string(snapshotID) {
				t.Error("established external ACP session did not renew once within its retained pin")
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
		want := "actual ACP tool"
		if providerID == "pool-beta" || (poolOnly && (providerID == alphaID || providerID == "pool-shared-alpha")) {
			want = "ACP has no worker tool"
		}
		if forcedExit && closeErr != nil {
			if !forcedProcessExit(closeErr) {
				t.Errorf("actual forced ACP %s shutdown returned an unexpected error: %v", providerID, closeErr)
			}
		}
		if err != nil || (!forcedExit && closeErr != nil) || !strings.Contains(final, want) {
			t.Fatalf("actual ACP %s execution failed: final=%q error=%v close=%v", providerID, final, err, closeErr)
		}
	}
}

func forcedProcessExit(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !forcedProcessExit(child) {
				return false
			}
		}
		return len(joined.Unwrap()) > 0
	}
	if child := errors.Unwrap(err); child != nil {
		return forcedProcessExit(child)
	}
	exit, ok := err.(*exec.ExitError)
	return ok && !exit.Success()
}

func TestMCPACPProviderChild(t *testing.T) {
	if os.Getenv("BALDA_MCP_ACP_CHILD_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	recordCatalogProcess(t, os.Getenv("BALDA_MCP_CATALOG_PROCESS_DIRECTORY"), "ACP")
	ctx, cancel := context.WithCancel(context.Background())
	provider := &mcpACPProvider{ctx: ctx, sessions: make(map[acp.SessionId][]*mcp.ClientSession), ready: make(chan struct{})}
	// NewAgentSideConnection starts readers immediately. Configure the child
	// logger first rather than racing SetLogger against those goroutines.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	provider.connection = acp.NewAgentSideConnection(provider, os.Stdout, os.Stdin)
	close(provider.ready)
	<-provider.connection.Done()
	cancel()
	provider.close()
	// Test runner output would corrupt the ACP protocol on stdout.
	os.Exit(0)
}

// The controlled external ACP provider consumes the real session/new payload
// and uses its HTTP/SSE MCP transports. It does not borrow the discovery client.
type mcpACPProvider struct {
	ctx        context.Context
	ready      chan struct{}
	connection *acp.AgentSideConnection
	mu         sync.Mutex
	sessions   map[acp.SessionId][]*mcp.ClientSession
}

func (*mcpACPProvider) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AgentCapabilities: acp.AgentCapabilities{McpCapabilities: acp.McpCapabilities{Http: true, Sse: true}}}, nil
}

func (p *mcpACPProvider) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	var sessions []*mcp.ClientSession
	complete := false
	defer func() {
		if !complete {
			for _, session := range sessions {
				_ = session.Close()
			}
		}
	}()
	for _, server := range request.McpServers {
		var endpoint, name, transport string
		var headers []acp.HttpHeader
		var stdio *acp.McpServerStdio
		switch {
		case server.Http != nil:
			endpoint, name, transport, headers = server.Http.Url, server.Http.Name, string(mcpcmd.TransportHTTP), server.Http.Headers
		case server.Sse != nil:
			endpoint, name, transport, headers = server.Sse.Url, server.Sse.Name, string(mcpcmd.TransportSSE), server.Sse.Headers
		case server.Stdio != nil:
			stdio, name, transport = server.Stdio, server.Stdio.Name, string(mcpcmd.TransportStdio)
		default:
			return acp.NewSessionResponse{}, fmt.Errorf("unsupported fixture transport")
		}
		private := make(map[string]string, len(headers))
		for _, header := range headers {
			private[header.Name] = header.Value
		}
		if strings.HasPrefix(name, "balda.catalog.") && len(private) != 0 {
			target, err := url.Parse(endpoint)
			if err != nil || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() || len(private) != 1 || private[mcpbridge.CapabilityHeader] == "" {
				return acp.NewSessionResponse{}, fmt.Errorf("external ACP received unguarded worker credentials")
			}
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "external-ACP-fixture", Version: "1"}, nil)
		httpClient := &http.Client{Transport: projectedHeaders{headers: private}}
		var wire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
		if transport == string(mcpcmd.TransportSSE) {
			wire = &mcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
		}
		if stdio != nil {
			command := exec.CommandContext(p.ctx, stdio.Command, stdio.Args...)
			command.Dir = request.Cwd
			command.Env = append([]string(nil), os.Environ()...)
			for _, entry := range stdio.Env {
				command.Env = append(command.Env, entry.Name+"="+entry.Value)
			}
			wire = &mcp.CommandTransport{Command: command}
		}
		// An SSE stream belongs to the external provider lifetime, not the
		// short session/new RPC context, which ACP cancels after the response.
		session, err := client.Connect(p.ctx, wire, nil)
		if err != nil {
			return acp.NewSessionResponse{}, fmt.Errorf("external ACP MCP initialize failed")
		}
		if _, err := session.ListTools(ctx, &mcp.ListToolsParams{}); err != nil {
			_ = session.Close()
			return acp.NewSessionResponse{}, fmt.Errorf("external ACP MCP discovery failed")
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

func (p *mcpACPProvider) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	<-p.ready
	p.mu.Lock()
	sessions := append([]*mcp.ClientSession(nil), p.sessions[request.SessionId]...)
	p.mu.Unlock()
	text := "ACP has no worker tool"
	for _, session := range sessions {
		tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			return acp.PromptResponse{}, fmt.Errorf("external ACP MCP list failed")
		}
		for _, tool := range tools.Tools {
			if tool.Name != "echo" && tool.Name != "echo_two" {
				continue
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{"text": "actual ACP tool"}})
			if err != nil || result.IsError || len(result.Content) != 1 {
				return acp.PromptResponse{}, fmt.Errorf("external ACP MCP invocation failed")
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				return acp.PromptResponse{}, fmt.Errorf("external ACP MCP result invalid")
			}
			text = content.Text
		}
	}
	if err := p.connection.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.UpdateAgentMessageText(text)}); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (p *mcpACPProvider) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sessions := range p.sessions {
		for _, session := range sessions {
			_ = session.Close()
		}
	}
}

func (*mcpACPProvider) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (*mcpACPProvider) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (*mcpACPProvider) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (*mcpACPProvider) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (*mcpACPProvider) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (*mcpACPProvider) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, fmt.Errorf("fixture does not support resume")
}
func (*mcpACPProvider) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
func (*mcpACPProvider) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}
