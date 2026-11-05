package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
)

// RenderSessionNotice renders one validated lifecycle notice as Telegram Rich Markdown.
func RenderSessionNotice(body sessionfmt.Notice) (string, error) {
	if err := body.Validate(); err != nil {
		return "", err
	}
	var headline string
	switch body.Kind {
	case sessioncmd.ActivationCreated:
		headline = "Session created"
	case sessioncmd.ActivationReplaced:
		headline = "Session replaced after restore fallback"
	default:
		return "", sessionfmt.ErrInvalid
	}
	lines := []string{"**" + headline + "**", "", "- **Session:** " + escapeStatusMarkdown(body.SessionID)}
	appendSessionFields(&lines, body)
	return strings.Join(lines, "\n"), nil
}

// RenderSessionWarning renders one validated warning as Telegram Rich Markdown.
func RenderSessionWarning(body sessionfmt.Warning) (string, error) {
	if err := body.Validate(); err != nil {
		return "", err
	}
	if body.Code != sessioncmd.WarningWorkspaceSyncSkipped {
		return "", sessionfmt.ErrInvalid
	}
	return "**Workspace sync skipped**\n\nWorkspace synchronization was skipped for this session\\.\n\n- **Session:** " + escapeStatusMarkdown(body.SessionID), nil
}

func appendSessionFields(lines *[]string, body sessionfmt.Notice) {
	appendSessionField(lines, "Name", body.Label)
	appendSessionField(lines, "Provider", body.Runtime.ProviderID)
	appendSessionField(lines, "Provider type", body.Runtime.ProviderType)
	appendSessionField(lines, "Model", body.Runtime.EffectiveModel)
	appendSessionField(lines, "Reasoning", body.Runtime.ReasoningEffort)
	if len(body.Runtime.MCPServers) > 0 {
		servers := make([]string, len(body.Runtime.MCPServers))
		for i, server := range body.Runtime.MCPServers {
			servers[i] = escapeStatusMarkdown(server)
		}
		*lines = append(*lines, "- **MCP:** "+strings.Join(servers, ", "))
	}
}

func appendSessionField(lines *[]string, label, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*lines = append(*lines, "- **"+label+":** "+escapeStatusMarkdown(value))
	}
}
