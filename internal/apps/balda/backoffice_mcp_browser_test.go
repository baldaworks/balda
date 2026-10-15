package balda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

// The opt-in browser gate uses the host adapter and durable owners, never an
// authentication bypass or a management fake. All credentials/state are synthetic.
type unusedDiagnosticProbe struct{}

func (unusedDiagnosticProbe) DiagnoseMCP(context.Context, mcpcmd.Revision) mcpcmd.DiagnosticResult {
	return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticFailed}
}

const (
	mcpBrowserPassword = "correct-horse-battery"
	mcpBrowserBearer   = "synthetic-knowl-bearer"
)

type browserMCPRecord struct {
	Order         int    `json:"order"`
	Operation     string `json:"operation"`
	HeaderMatched bool   `json:"headerMatched"`
}

type browserMCPRecorder struct {
	handler               http.Handler
	expectedAuthorization string
	reject                atomic.Bool
	mu                    sync.Mutex
	records               []browserMCPRecord
}

func (r *browserMCPRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/__recorder":
		r.mu.Lock()
		records := make([]browserMCPRecord, len(r.records))
		copy(records, r.records)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(records)
		return
	case "/__recorder/reset":
		r.reject.Store(request.URL.Query().Has("reject"))
		r.mu.Lock()
		r.records = nil
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(body))
	var message struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &message)
	headerMatched := request.Header.Get("Authorization") == r.expectedAuthorization
	if message.Method == "initialize" || message.Method == "tools/list" {
		r.mu.Lock()
		r.records = append(r.records, browserMCPRecord{Order: len(r.records) + 1, Operation: message.Method, HeaderMatched: headerMatched})
		r.mu.Unlock()
	}
	if r.reject.Load() || !headerMatched {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	r.handler.ServeHTTP(w, request)
}

func TestBackofficeMCPRecorderKeepsOnlySanitizedProtocolFacts(t *testing.T) {
	recorder := &browserMCPRecorder{
		handler:               http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		expectedAuthorization: "Bearer private-fixture-value",
	}
	request := httptest.NewRequest(http.MethodPost, "/knowl", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"secret":"private-payload"}}`))
	request.Header.Set("Authorization", "Bearer private-fixture-value")
	response := httptest.NewRecorder()
	recorder.ServeHTTP(response, request)
	encoded, err := json.Marshal(recorder.records)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusNoContent || string(encoded) != `[{"order":1,"operation":"initialize","headerMatched":true}]` {
		t.Fatalf("sanitized record = %s, status %d", encoded, response.Code)
	}
	if bytes.Contains(encoded, []byte("private-fixture-value")) || bytes.Contains(encoded, []byte("private-payload")) {
		t.Fatal("recorder retained credential or payload material")
	}
}

func TestBackofficeMCPBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	hash, err := userpassword.Hash([]byte(mcpBrowserPassword))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		id := string(role)
		u := usercmd.User{ID: id, Username: id, NormalizedUsername: id, DisplayName: id, Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
		if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: id, PasswordHash: hash}, usercmd.AuditEvent{ID: id, Action: usercmd.AuditActionUserCreated, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: id, Source: "mcp-browser-fixture", OccurredAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	sdk := mcp.NewServer(&mcp.Implementation{Name: "browser-tools", Version: "1"}, nil)
	mcp.AddTool(sdk, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	mcp.AddTool(sdk, &mcp.Tool{Name: "lookup"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "synthetic-result"}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, nil)
	recorder := &browserMCPRecorder{handler: handler, expectedAuthorization: "Bearer " + mcpBrowserBearer}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/knowl" || r.URL.Path == "/__recorder" || r.URL.Path == "/__recorder/reset" {
			recorder.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/managed" && r.Header.Get("X-Worker") != "synthetic-browser-protected-value" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	credentials, err := mcpmanage.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(p.MCP()), mcpfx.NewOAuthProvider(nil))
	if err != nil {
		t.Fatal(err)
	}
	bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: grants}, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	configured := map[string]agentconfig.MCPServerConfig{"knowl": {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.URL + "/knowl", Headers: map[string]string{"Authorization": "Bearer " + mcpBrowserBearer}}}
	catalog, err := catalogapp.NewRuntime(t.TempDir(), "", "", p, nil, configured, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.MCP().Shutdown(context.Background()) })
	snapshot, err := catalog.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.PublishCandidate(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	launcher := mcpfx.NewClientLauncher()
	probe, err := mcpfx.NewManagedProbe(credentials, launcher, bridge)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := mcpfx.NewDiagnosticProbe(credentials, grants, launcher)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configured, map[string]agentconfig.Config{"hosted": {}}, "hosted", nil), catalog, probe, diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{ListenAddr: address, PublicURL: "http://" + address, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureMCPOperations(mcpbackofficeapp.New(definitions, catalog)); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Stop(context.Background()) })
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	before := mcpBrowserState(t, p, catalog, definitions)
	diagnosticOutput := runMCPBrowser(t, root, "http://"+address, upstream.URL+"/managed", upstream.URL, "diagnostic")
	after := mcpBrowserState(t, p, catalog, definitions)
	if !bytes.Equal(before, after) {
		t.Fatal("configured diagnostic changed MCP definitions, revisions, grants, catalog, or runtime health")
	}
	output := runMCPBrowser(t, root, "http://"+address, upstream.URL+"/managed", upstream.URL, "crud")
	items, err := definitions.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := p.MCP().ListMCPRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || len(revisions) != 6 {
		t.Fatalf("durable definitions/revisions = %d/%d, want 3/6", len(items), len(revisions))
	}
	for _, item := range items {
		if item.Connection.Source == mcpcmd.SourceManaged && item.Status != mcpcmd.StatusDeleted {
			t.Fatal("browser deletion did not tombstone managed connection")
		}
	}
	t.Log(string(diagnosticOutput))
	t.Log(string(output))
}

func runMCPBrowser(t *testing.T, root, baseURL, managedURL, recorderURL, mode string) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/mcp.cjs", baseURL, managedURL, mode, recorderURL)
	command.Dir = root
	command.Env = append(os.Environ(), "BALDA_E2E_ADMIN_PASSWORD="+mcpBrowserPassword, "BALDA_E2E_MCP_BEARER="+mcpBrowserBearer)
	output, err := command.CombinedOutput()
	if bytes.Contains(output, []byte(mcpBrowserPassword)) || bytes.Contains(output, []byte(mcpBrowserBearer)) {
		t.Fatal("MCP browser output contained a test credential")
	}
	if err != nil {
		t.Fatalf("MCP browser %s workflow: %v\n%s", mode, err, output)
	}
	return output
}

func mcpBrowserState(t *testing.T, provider state.Provider, catalog *catalogapp.Runtime, definitions *mcpmanage.Definitions) []byte {
	t.Helper()
	connections, err := provider.MCP().ListMCPConnections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := provider.MCP().ListMCPRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	grants, err := provider.MCP().ListMCPGrants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items, err := definitions.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	application, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal([]any{connections, revisions, grants, items, application, catalog.MCP().Health()})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
