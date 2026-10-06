package catalogapp

import (
	"testing"

	"github.com/normahq/runtime/v2/agentconfig"
)

func TestProviderMCPDefaultsValidatesSelectedGraph(t *testing.T) {
	valid := agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "fixture-model"}}
	invalid := agentconfig.Config{Type: "fixture-unsupported", OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key"}}
	for _, tc := range []struct {
		name            string
		selected, other agentconfig.Config
		wantError       bool
	}{
		{"valid graph", valid, valid, false},
		{"invalid selected member", invalid, valid, true},
		{"missing selected key", agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: "fixture"}}, valid, true},
		{"missing selected model", agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture"}}, valid, true},
		{"invalid unselected member", valid, invalid, false},
		{"nested selected pool", agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"other"}}}, valid, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providers := map[string]agentconfig.Config{
				"pool":     {Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"selected"}}},
				"selected": tc.selected, "other": tc.other,
			}
			_, err := providerMCPDefaults(providers, "pool", nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("selected graph error = %v, want failure %t", err, tc.wantError)
			}
		})
	}
}
