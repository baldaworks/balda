package catalogapp

import (
	"context"
	"net/http"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func testConfiguredOAuthRecovery(t *testing.T, original *Runtime, worker *workerGrantFixture, pinned runtimecatalogcmd.Snapshot, configured map[string]agentconfig.MCPServerConfig) {
	t.Helper()
	for _, removed := range []bool{false, true} {
		changed := make(map[string]agentconfig.MCPServerConfig)
		if !removed {
			config := configured["worker-tools"]
			config.Headers = map[string]string{"X-Worker-Secret": "changed-file-secret"}
			changed["worker-tools"] = config
		}
		bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: worker.grants}, nil)
		if err := bridge.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = bridge.Close(t.Context()) }()
		registry := mcpregistry.New(nil)
		recovered, err := NewRuntime(original.stateDir, "", "", worker.provider, nil, changed, registry, commandcmd.NewRegistry(), worker.credentials, bridge)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = recovered.MCP().Shutdown(t.Context()) }()
		providers := map[string]agentconfig.Config{"alpha": {}, "beta": {}}
		if !removed {
			providers["beta"] = agentconfig.Config{MCPServers: []string{"worker-tools"}}
		}
		if err := recovered.configureProviderMCP(providers, "beta", nil); err != nil {
			t.Fatal(err)
		}
		current, err := recovered.PreparePluginCandidate(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := recovered.PublishCandidate(t.Context(), current); err != nil {
			t.Fatal(err)
		}
		if !removed {
			ids, release, err := recovered.AcquireProviderMCPServerIDs(t.Context(), current.ID, map[string][]string{"beta": {"worker-tools"}})
			if release != nil {
				release()
			}
			if err == nil || len(ids) != 0 {
				t.Fatal("edited OAuth file silently became an anonymous current selection")
			}
		}
		selection, release, err := recovered.AcquireProviderMCPServerIDs(t.Context(), pinned.ID, map[string][]string{"alpha": {}, "beta": {}})
		if err != nil {
			t.Fatal(err)
		}
		if len(selection["alpha"]) != 1 || len(selection["beta"]) != 0 {
			release()
			t.Fatal("recovery changed retained configured provider targets")
		}
		config, found := registry.Get(selection["alpha"][0])
		if !found {
			release()
			t.Fatal("retained configured capture did not project")
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "restored-configured-worker", Version: "1"}, nil)
		httpClient := &http.Client{Transport: projectedHeaders{headers: config.Headers}}
		var transport mcp.Transport = &mcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: httpClient}
		if config.Type == agentconfig.MCPServerTypeSSE {
			transport = &mcp.SSEClientTransport{Endpoint: config.URL, HTTPClient: httpClient}
		}
		session, err := client.Connect(t.Context(), transport, nil)
		if err != nil {
			release()
			t.Fatal(err)
		}
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "restored protected file"}})
		_ = session.Close()
		release()
		if err != nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != "restored protected file" {
			t.Fatal("retained OAuth file capture did not invoke the original protected resource")
		}
		for _, health := range recovered.MCP().Health() {
			if health.State == mcpruntime.HealthReady {
				t.Fatal("released historical instance remained ready after recovery")
			}
		}
		connection, found, err := worker.provider.MCP().GetMCPConnection(context.Background(), worker.binding.ConnectionID)
		if err != nil || !found {
			t.Fatal(err)
		}
		status, _, err := recovered.MCPHealth(t.Context(), connection)
		if err != nil || status != mcpcmd.StatusAuthRequired {
			t.Fatal("file change did not expose authorization-required readiness")
		}
	}
}
