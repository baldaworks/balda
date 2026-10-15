package mcpfx

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioDiscoveryInheritsHostEnvironmentForConfiguredAndManaged(t *testing.T) {
	t.Setenv("BALDA_MCP_STDIO_HOST_FIXTURE", "host-present")
	t.Setenv("BALDA_MCP_STDIO_OVERLAY_FIXTURE", "parent-value")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := mcpmanage.New("")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"configured", "managed", "plugin"} {
		t.Run(source, func(t *testing.T) {
			config := mcpruntime.LaunchConfig{Transport: transportStdio, Command: executable,
				Args: []string{"-test.run=^TestMCPStdioEnvironmentChild$"},
				Env:  map[string]string{"BALDA_MCP_STDIO_CHILD_FIXTURE": "1", "BALDA_MCP_STDIO_OVERLAY_FIXTURE": "child-value"}}
			if source == "managed" {
				revision := mcpcmd.Revision{Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: config.Command, Args: config.Args, Env: map[string]mcpcmd.ValueBinding{
					"BALDA_MCP_STDIO_CHILD_FIXTURE":   {Kind: mcpcmd.ValueLiteral, Value: "1"},
					"BALDA_MCP_STDIO_OVERLAY_FIXTURE": {Kind: mcpcmd.ValueLiteral, Value: "child-value"},
				}}}
				config, err = ResolveManagedLaunch(revision, credentials)
				if err != nil {
					t.Fatal(err)
				}
			}
			if source == "configured" {
				descriptor := runtimecatalogcmd.MCPServerDescriptor{ID: runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: "worker"}, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: "worker"}, Revision: "fixture-revision", Name: "worker", Transport: transportStdio}
				resolver := BridgeResolver{Resolver: mcpruntime.NewStaticResolver(map[runtimecatalogcmd.ContributionID]mcpruntime.StaticLaunchEntry{descriptor.ID: {Revision: descriptor.Revision, Config: config}})}
				config, err = resolver.ResolveLaunch(t.Context(), descriptor)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			instance, err := NewClientLauncher().Start(ctx, mcpruntime.InstanceKey{Name: "worker"}, config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = instance.Close(ctx) }()
			want := "host-present:child-value"
			if source == "plugin" {
				want = ":child-value"
			}
			tools := instance.Tools()
			if len(tools) != 1 || tools[0].Description != want {
				t.Fatalf("%s stdio environment differs from its launch policy: %+v", source, tools)
			}
		})
	}
}

func TestMCPStdioEnvironmentChild(t *testing.T) {
	if os.Getenv("BALDA_MCP_STDIO_CHILD_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "stdio-environment", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "environment", Description: os.Getenv("BALDA_MCP_STDIO_HOST_FIXTURE") + ":" + os.Getenv("BALDA_MCP_STDIO_OVERLAY_FIXTURE")}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}
