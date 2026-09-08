package welcome

import (
	"fmt"
	"strings"
)

const noneValue = "none"

// BuildAgentWelcomeMessage returns the markdown-formatted session welcome text.
func BuildAgentWelcomeMessage(name, sessionID, agentType, model, reasoning string, mcpServers []string) string {
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		cleanName = noneValue
	}

	cleanSessionID := strings.TrimSpace(sessionID)
	if cleanSessionID == "" {
		cleanSessionID = noneValue
	}

	cleanType := strings.TrimSpace(agentType)
	if cleanType == "" {
		cleanType = noneValue
	}

	cleanModel := strings.TrimSpace(model)
	if cleanModel == "" {
		cleanModel = noneValue
	}

	cleanReasoning := strings.TrimSpace(reasoning)
	if cleanReasoning == "" {
		cleanReasoning = noneValue
	}

	var cleanMCP []string
	if len(mcpServers) > 0 {
		seen := make(map[string]struct{}, len(mcpServers))
		cleanMCP = make([]string, 0, len(mcpServers))
		for _, serverID := range mcpServers {
			trimmed := strings.TrimSpace(serverID)
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			cleanMCP = append(cleanMCP, trimmed)
		}
	}
	mcpValue := strings.Join(cleanMCP, ", ")
	if mcpValue == "" {
		mcpValue = noneValue
	}

	return fmt.Sprintf(
		"🚀 **Session Started** • **Name:** `%s` • **ID:** `%s` • **Model:** `%s` • **Reasoning:** `%s` • **Type:** `%s` • **MCP:** `%s` ",
		strings.ReplaceAll(cleanName, "`", "\\` "),
		strings.ReplaceAll(cleanSessionID, "`", "\\` "),
		strings.ReplaceAll(cleanModel, "`", "\\` "),
		strings.ReplaceAll(cleanReasoning, "`", "\\` "),
		strings.ReplaceAll(cleanType, "`", "\\` "),
		strings.ReplaceAll(mcpValue, "`", "\\` "),
	)
}
