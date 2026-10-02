package mcpfx

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

func TestConfiguredInventoryRetainsGlobalSelectionWithoutExposingCredentials(t *testing.T) {
	configs := map[string]agentconfig.MCPServerConfig{
		"shared":   {Type: agentconfig.MCPServerTypeHTTP, URL: "https://shared.example.org/mcp", Headers: map[string]string{"Authorization": "configured-secret-fixture"}},
		"selected": {Type: agentconfig.MCPServerTypeStdio, Cmd: []string{"worker", "--stdio"}, Env: map[string]string{"KEY": "configured-env-secret-fixture"}},
	}
	providers := map[string]agentconfig.Config{"hosted": {MCPServers: []string{"selected"}}, "acp": {}}
	// Balda's global selection supplements provider-local references.
	c := NewConfiguredDefinitions(configs, providers, []string{"shared"})
	items, err := c.MCPDefinitions(t.Context())
	if err != nil || len(items) != 2 {
		t.Fatalf("inventory: %v", err)
	}
	for _, item := range items {
		if item.Connection.Source != mcpcmd.SourceConfig || item.Status != mcpcmd.StatusPending {
			t.Fatal("configured source reported managed or ready")
		}
		if item.Connection.PublicID == "shared" && !item.Definition.Targets.All {
			t.Fatal("global configured selection lost")
		}
		if item.Connection.PublicID == "selected" && (len(item.Definition.Targets.Providers) != 1 || item.Definition.Targets.Providers[0] != "hosted") {
			t.Fatal("provider-local configured selection lost")
		}
	}
	data, err := json.Marshal(items)
	if err != nil || bytes.Contains(data, []byte("configured-secret-fixture")) || bytes.Contains(data, []byte("configured-env-secret-fixture")) {
		t.Fatal("configured secret exposed in inventory")
	}
	var sharedIndex int
	for i, item := range items {
		if item.Connection.PublicID == "shared" {
			sharedIndex = i
		}
	}
	items[sharedIndex].Definition.Headers["Authorization"] = mcpcmd.ValueBinding{Kind: mcpcmd.ValueLiteral, Value: "caller-mutation"}
	again, _ := c.MCPDefinitions(t.Context())
	if again[sharedIndex].Definition.Headers["Authorization"].Kind != mcpcmd.ValueProtected {
		t.Fatal("caller changed immutable configured metadata")
	}
}
