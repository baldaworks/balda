package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
)

// RenderSessionNotice renders one validated lifecycle notice as Slack mrkdwn.
func RenderSessionNotice(body sessionfmt.Notice) (string, error) {
	if err := body.Validate(); err != nil {
		return "", err
	}
	var headline string
	switch body.Kind {
	case sessioncmd.ActivationCreated:
		headline = "🚀 *Session Created*"
	case sessioncmd.ActivationReplaced:
		headline = "♻️ *Session Replaced*"
	default:
		return "", sessionfmt.ErrInvalid
	}
	parts := []string{headline}
	appendSessionFields(&parts, body)
	return strings.Join(parts, " • "), nil
}

// RenderSessionWarning renders one validated warning as Slack mrkdwn.
func RenderSessionWarning(body sessionfmt.Warning) (string, error) {
	if err := body.Validate(); err != nil {
		return "", err
	}
	if body.Code != sessioncmd.WarningWorkspaceSyncSkipped {
		return "", sessionfmt.ErrInvalid
	}
	return "*Workspace sync skipped*\n\nWorkspace synchronization was skipped for this session.\n\n• *Session:* " + escapeStatusInline(body.SessionID), nil
}

func appendSessionFields(parts *[]string, body sessionfmt.Notice) {
	appendSessionField(parts, "Name", body.Label)
	appendSessionField(parts, "ID", body.SessionID)
	appendSessionField(parts, "Provider", body.ProviderID())
	modelLabel, model := body.ModelDisplay()
	appendSessionField(parts, modelLabel, model)
	appendSessionField(parts, "Reasoning", body.ReasoningEffort())
	if servers := body.MCPServers(); len(servers) > 0 {
		appendSessionField(parts, "MCP", strings.Join(servers, ", "))
	}
}

func appendSessionField(parts *[]string, label, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*parts = append(*parts, "*"+label+":* "+statusCode(value))
	}
}
