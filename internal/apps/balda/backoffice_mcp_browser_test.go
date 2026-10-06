package balda

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
func TestBackofficeMCPBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	hash, err := userpassword.Hash([]byte("correct-horse-battery"))
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
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	configured := map[string]agentconfig.MCPServerConfig{"file-server": {Type: agentconfig.MCPServerTypeHTTP, URL: upstream.URL + "/configured"}}
	catalog, err := catalogapp.NewRuntime(t.TempDir(), "", "", p, nil, configured, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.MCP().Shutdown(context.Background()) })
	snapshot, err := catalog.PreparePluginCandidate(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.PublishCandidate(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), bridge)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configured, map[string]agentconfig.Config{"hosted": {}}, "hosted", nil), catalog, probe)
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
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/mcp.cjs", "http://"+address, upstream.URL+"/managed")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("MCP browser workflow: %v\n%s", err, output)
	}
	items, err := definitions.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := p.MCP().ListMCPRevisions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 || len(revisions) != 12 {
		t.Fatalf("durable definitions/revisions = %d/%d, want 5/12", len(items), len(revisions))
	}
	for _, item := range items {
		if item.Connection.Source == mcpcmd.SourceManaged && item.Status != mcpcmd.StatusDeleted {
			t.Fatal("browser deletion did not tombstone managed connection")
		}
	}
	t.Log(string(output))
}
