package catalogapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestMain(m *testing.M) {
	if handled, code := mcpfx.RunStdioMode(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestStdioDiscoveryAndActualProvidersUseHostEnvironmentAndOverlay(t *testing.T) {
	t.Setenv("BALDA_MCP_CATALOG_STDIO_HOST", "host-value")
	t.Setenv("BALDA_MCP_CATALOG_STDIO_OVERLAY", "parent-value")
	t.Setenv("BALDA_MCP_CATALOG_STDIO_REFERENCE_SOURCE", "referenced-value")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []mcpcmd.Source{mcpcmd.SourceManaged, mcpcmd.SourceConfig} {
		t.Run(string(source), func(t *testing.T) {
			p, original, _, mutation, credentials := hybridCatalogFixture(t)
			configured := make(map[string]agentconfig.MCPServerConfig)
			var directories []string
			names := []string{"echo", "echo_two"}
			ids := []string{"worker-tools", "worker-tools-two"}
			for index, name := range names {
				directory := t.TempDir()
				directories = append(directories, directory)
				config := agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeStdio, Cmd: []string{executable}, Args: append([]string{"-test.run=^TestMCPCatalogStdioChild$", "--"}, stdioCatalogLiteralArgs()...), WorkingDir: directory, Env: map[string]string{"BALDA_MCP_CATALOG_STDIO_CHILD": "1", "BALDA_MCP_CATALOG_STDIO_OVERLAY": "child-value", "BALDA_MCP_CATALOG_STDIO_DIRECTORY": directory, "BALDA_MCP_CATALOG_STDIO_TOOL": name, "BALDA_MCP_CATALOG_STDIO_REFERENCE": "referenced-value"}}
				if source == mcpcmd.SourceConfig {
					configured[ids[index]] = config
					continue
				}
				next := mutation
				if index == 1 {
					next.Connection.ID, next.Connection.PublicID, next.Connection.CurrentRevisionID = "worker-two", ids[index], "revision-two"
					next.Audit.ID, next.Audit.TargetID = "create-mcp-two", next.Connection.ID
				}
				edits := make(map[string]mcpcmd.ValueEdit)
				for key, value := range config.Env {
					edits[key] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueLiteral, Value: value}
				}
				overlay := edits["BALDA_MCP_CATALOG_STDIO_OVERLAY"]
				overlay.Kind = mcpcmd.ValueProtected
				edits["BALDA_MCP_CATALOG_STDIO_OVERLAY"] = overlay
				edits["BALDA_MCP_CATALOG_STDIO_REFERENCE"] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueEnvironment, Value: "BALDA_MCP_CATALOG_STDIO_REFERENCE_SOURCE"}
				revision, err := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: next.Connection.ID, ID: next.Connection.CurrentRevisionID, CreatedAt: next.Connection.UpdatedAt, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: executable, Args: config.Args, Directory: directory, Targets: mcpcmd.Targets{Providers: []string{"alpha"}}}}, mcpcmd.ValueEdits{Env: edits})
				if err != nil {
					t.Fatal(err)
				}
				next.Revision = &revision
				if err := p.MCP().SaveMCPConnection(t.Context(), next); err != nil {
					t.Fatal(err)
				}
			}
			registry := mcpregistry.New(nil)
			catalog, err := NewRuntime(original.stateDir, "", "", p, nil, configured, registry, commandcmd.NewRegistry(), credentials, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = catalog.MCP().Shutdown(t.Context()) }()
			if source == mcpcmd.SourceConfig {
				if err := catalog.configureProviderMCP(map[string]agentconfig.Config{"alpha": {MCPServers: ids}, "beta": {}}, "alpha", nil); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := catalog.PreparePluginCandidate(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.PublishCandidate(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			if health := catalog.MCP().Health(); len(health) != 2 || health[0].ToolCount != 1 || health[1].ToolCount != 1 {
				t.Fatal("stdio discovery lost host environment or protected overlay")
			}
			for _, descriptor := range snapshot.MCPServers {
				projected, found := registry.Get(descriptorRegistryID(descriptor))
				if !found || projected.Type != agentconfig.MCPServerTypeStdio {
					t.Fatal("stdio was not projected directly")
				}
			}
			testHostedPoolInvocationWithTools(t, catalog, registry, snapshot.ID, source, nil, names)
			testACPInvocationWithServers(t, catalog, registry, snapshot.ID, source, nil, ids)
			for _, directory := range directories {
				calls, err := os.ReadFile(filepath.Join(directory, "invocations.txt"))
				if err != nil || !strings.Contains(string(calls), "actual hosted tool") || !strings.Contains(string(calls), "actual ACP tool") {
					t.Fatalf("both providers must invoke each independent server: %s: %v", calls, err)
				}
			}
		})
	}
}

func TestMCPCatalogStdioChild(t *testing.T) {
	if os.Getenv("BALDA_MCP_CATALOG_STDIO_CHILD") != "1" {
		t.Skip("subprocess fixture")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "catalog-stdio", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: os.Getenv("BALDA_MCP_CATALOG_STDIO_TOOL")}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		if os.Getenv("BALDA_MCP_CATALOG_STDIO_HOST") != "host-value" || os.Getenv("BALDA_MCP_CATALOG_STDIO_OVERLAY") != "child-value" || os.Getenv("BALDA_MCP_CATALOG_STDIO_REFERENCE") != "referenced-value" || !slices.Equal(os.Args[3:], stdioCatalogLiteralArgs()) {
			return nil, nil, fmt.Errorf("stdio execution environment differs from discovery")
		}
		directory, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		actual, err := os.Stat(directory)
		if err != nil {
			return nil, nil, err
		}
		expected, err := os.Stat(os.Getenv("BALDA_MCP_CATALOG_STDIO_DIRECTORY"))
		if err != nil || !os.SameFile(actual, expected) {
			return nil, nil, fmt.Errorf("stdio execution lost its configured working directory")
		}
		calls, err := os.OpenFile("invocations.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return nil, nil, err
		}
		_, err = fmt.Fprintln(calls, args.Text)
		closeErr := calls.Close()
		if err != nil || closeErr != nil {
			return nil, nil, fmt.Errorf("record stdio invocation")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func stdioCatalogLiteralArgs() []string {
	return []string{"a b", "$HOME", "$(false)", `back\slash`, `quote"here`, "юникод"}
}
