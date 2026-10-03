package catalogapp

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestStdioDiscoveryAndActualProvidersUseHostEnvironmentAndOverlay(t *testing.T) {
	t.Setenv("BALDA_MCP_CATALOG_STDIO_HOST", "host-value")
	t.Setenv("BALDA_MCP_CATALOG_STDIO_OVERLAY", "parent-value")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []mcpcmd.Source{mcpcmd.SourceManaged, mcpcmd.SourceConfig} {
		t.Run(string(source), func(t *testing.T) {
			p, original, _, mutation, credentials := hybridCatalogFixture(t)
			config := agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeStdio, Cmd: []string{executable}, Args: []string{"-test.run=^TestMCPCatalogStdioChild$"}, WorkingDir: t.TempDir(), Env: map[string]string{"BALDA_MCP_CATALOG_STDIO_CHILD": "1", "BALDA_MCP_CATALOG_STDIO_OVERLAY": "child-value"}}
			config.Env["BALDA_MCP_CATALOG_STDIO_DIRECTORY"] = config.WorkingDir
			var configured map[string]agentconfig.MCPServerConfig
			if source == mcpcmd.SourceConfig {
				configured = map[string]agentconfig.MCPServerConfig{"worker-tools": config}
			} else {
				r, err := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: mutation.Connection.ID, ID: mutation.Connection.CurrentRevisionID, CreatedAt: mutation.Connection.UpdatedAt, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: executable, Args: config.Args, Directory: config.WorkingDir, Targets: mcpcmd.Targets{Providers: []string{"alpha"}}}}, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{
					"BALDA_MCP_CATALOG_STDIO_CHILD":     {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueLiteral, Value: "1"},
					"BALDA_MCP_CATALOG_STDIO_OVERLAY":   {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "child-value"},
					"BALDA_MCP_CATALOG_STDIO_DIRECTORY": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueLiteral, Value: config.WorkingDir},
				}})
				if err != nil {
					t.Fatal(err)
				}
				mutation.Revision = &r
				if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
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
				if err := catalog.configureProviderMCP(map[string]agentconfig.Config{"alpha": {MCPServers: []string{"worker-tools"}}, "beta": {}}, "alpha", nil); err != nil {
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
			if health := catalog.MCP().Health(); len(health) != 1 || health[0].ToolCount != 1 {
				t.Fatal("stdio discovery lost host environment or protected overlay")
			}
			for _, descriptor := range snapshot.MCPServers {
				projected, found := registry.Get(descriptorRegistryID(descriptor))
				if !found || projected.Type != agentconfig.MCPServerTypeStdio {
					t.Fatal("stdio was not projected directly")
				}
			}
			testHostedPoolInvocation(t, catalog, registry, snapshot.ID, source, nil)
			testACPInvocation(t, catalog, registry, snapshot.ID, source, nil)
		})
	}
}

func TestMCPCatalogStdioChild(t *testing.T) {
	if os.Getenv("BALDA_MCP_CATALOG_STDIO_CHILD") != "1" {
		t.Skip("subprocess fixture")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "catalog-stdio", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		if os.Getenv("BALDA_MCP_CATALOG_STDIO_HOST") != "host-value" || os.Getenv("BALDA_MCP_CATALOG_STDIO_OVERLAY") != "child-value" {
			return nil, nil, fmt.Errorf("stdio execution environment differs from discovery")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
