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
		headline = "🚀 **Session Created**"
	case sessioncmd.ActivationReplaced:
		headline = "♻️ **Session Replaced**"
	default:
		return "", sessionfmt.ErrInvalid
	}
	parts := []string{headline}
	appendSessionFields(&parts, body)
	return strings.Join(parts, " • "), nil
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

func appendSessionFields(parts *[]string, body sessionfmt.Notice) {
	appendSessionField(parts, "Name", body.Label)
	appendSessionField(parts, "ID", body.SessionID)
	appendSessionField(parts, "Provider", body.Runtime.ProviderID)
	appendSessionField(parts, "Model", body.Runtime.EffectiveModel)
	appendSessionField(parts, "Reasoning", body.Runtime.ReasoningEffort)
	if len(body.Runtime.MCPServers) > 0 {
		appendSessionField(parts, "MCP", strings.Join(body.Runtime.MCPServers, ", "))
	}
}

func appendSessionField(parts *[]string, label, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*parts = append(*parts, "**"+label+":** "+sessionCode(value))
	}
}

func sessionCode(value string) string {
	value = strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(strings.TrimSpace(value))
	return "`" + value + "`"
}
