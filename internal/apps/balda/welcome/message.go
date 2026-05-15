package welcome

import (
	"fmt"
	"strings"
)

const noneValue = "none"

// BuildAgentWelcomeMessage returns the markdown-formatted session welcome text.
func BuildAgentWelcomeMessage(name, sessionID, agentType, model string, mcpServers []string) string {
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

	cleanMCP := normalizeMCPServers(mcpServers)
	mcpValue := strings.Join(cleanMCP, ", ")
	if mcpValue == "" {
		mcpValue = noneValue
	}

	return fmt.Sprintf(
		"🚀 **Session Started** • **Name:** `%s` • **ID:** `%s` • **Model:** `%s` • **Type:** `%s` • **MCP:** `%s` ",
		escapeMarkdownV2(cleanName),
		escapeMarkdownV2(cleanSessionID),
		escapeMarkdownV2(cleanModel),
		escapeMarkdownV2(cleanType),
		escapeMarkdownV2(mcpValue),
	)
}

func escapeMarkdownV2(s string) string {
	// Inside code blocks (backticks), only \ and ` need to be escaped.
	// Since we are using backticks for values, we escape backticks.
	return strings.ReplaceAll(s, "`", "\\` ")
}

func normalizeMCPServers(mcpServers []string) []string {
	if len(mcpServers) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(mcpServers))
	out := make([]string, 0, len(mcpServers))
	for _, serverID := range mcpServers {
		trimmed := canonicalMCPServerID(strings.TrimSpace(serverID))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func canonicalMCPServerID(serverID string) string {
	switch serverID {
	case "balda", "runtime.balda", "runtime.state", "runtime.workspace", "balda.state", "balda.workspace":
		return "balda"
	default:
		return serverID
	}
}
