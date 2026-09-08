package welcome

import (
	"testing"
)

func TestBuildAgentWelcomeMessage(t *testing.T) {
	tests := []struct {
		name       string
		agentName  string
		sessionID  string
		agentType  string
		model      string
		reasoning  string
		mcpServers []string
		want       string
	}{
		{
			name:       "full info",
			agentName:  "balda",
			sessionID:  "tg-1-0",
			agentType:  "opencode_acp",
			model:      "gpt-5",
			reasoning:  "high",
			mcpServers: []string{" balda ", "workspace", "balda", ""},
			want:       "🚀 **Session Started** • **Name:** `balda` • **ID:** `tg-1-0` • **Model:** `gpt-5` • **Reasoning:** `high` • **Type:** `opencode_acp` • **MCP:** `balda, workspace` ",
		},
		{
			name:       "missing info uses none",
			agentName:  " ",
			sessionID:  " ",
			agentType:  " ",
			model:      " ",
			reasoning:  " ",
			mcpServers: nil,
			want:       "🚀 **Session Started** • **Name:** `none` • **ID:** `none` • **Model:** `none` • **Reasoning:** `none` • **Type:** `none` • **MCP:** `none` ",
		},
		{
			name:       "escapes backticks",
			agentName:  "agent`name",
			sessionID:  "id",
			agentType:  "type",
			model:      "model",
			reasoning:  "reas`on",
			mcpServers: nil,
			want:       "🚀 **Session Started** • **Name:** `agent\\` name` • **ID:** `id` • **Model:** `model` • **Reasoning:** `reas\\` on` • **Type:** `type` • **MCP:** `none` ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAgentWelcomeMessage(tt.agentName, tt.sessionID, tt.agentType, tt.model, tt.reasoning, tt.mcpServers)
			if got != tt.want {
				t.Errorf("BuildAgentWelcomeMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
