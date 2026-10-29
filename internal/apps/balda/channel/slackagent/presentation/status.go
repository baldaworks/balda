package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

// RenderStatus renders a status response as Slack mrkdwn.
func RenderStatus(body statusfmt.Response) string {
	return renderStatusModel(statusfmt.ModelFor(body))
}

// RenderLegacyStatus renders the bounded v1/v2 status contract.
func RenderLegacyStatus(body statusfmt.Response) string {
	return renderStatusModel(statusfmt.LegacyModelFor(body))
}

func renderStatusModel(model statusfmt.Model) string {
	lines := []string{"*" + escapeStatusInline(model.Headline) + "*", ""}
	for _, field := range model.Operational {
		lines = append(lines, "• *"+escapeStatusInline(field.Label)+":* "+statusCode(field.Value))
	}
	if model.Request != "" {
		lines = append(lines, "", "*Request"+statusTruncationSuffix(model.Truncated)+":*", "```\n"+escapeStatusCodeBlock(model.Request)+"\n```")
	}
	lines = append(lines, "")
	for _, field := range model.Diagnostics {
		lines = append(lines, "• *"+escapeStatusInline(field.Label)+":* "+statusCode(field.Value))
	}
	return strings.Join(lines, "\n")
}

func escapeStatusInline(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "*", "").Replace(strings.TrimSpace(value))
}

func statusCode(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "ˋ").Replace(value)
	return "`" + value + "`"
}

func escapeStatusCodeBlock(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "(empty)"
	}
	value = strings.ReplaceAll(value, "```", "`\u200b``")
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func statusTruncationSuffix(truncated bool) string {
	if truncated {
		return " (truncated)"
	}
	return ""
}
