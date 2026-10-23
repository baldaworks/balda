package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

// RenderStatus renders a status response as Mattermost Markdown.
func RenderStatus(body statusfmt.Response) string {
	model := statusfmt.ModelFor(body)
	lines := []string{"**" + escapeStatusMarkdown(model.Headline) + "**", ""}
	for _, field := range model.Operational {
		lines = append(lines, "- **"+escapeStatusMarkdown(field.Label)+":** "+escapeStatusMarkdown(field.Value))
	}
	if model.Request != "" {
		lines = append(lines, "", "**Request"+statusTruncationSuffix(model.Truncated)+":**", quoteStatusMarkdown(model.Request))
	}
	lines = append(lines, "")
	for _, field := range model.Diagnostics {
		lines = append(lines, "- **"+escapeStatusMarkdown(field.Label)+":** "+escapeStatusMarkdown(field.Value))
	}
	return strings.Join(lines, "\n")
}

func escapeStatusMarkdown(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", "_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`", ">", "\\>",
		"#", "\\#", "+", "\\+", "-", "\\-", "=", "\\=", "|", "\\|",
		"{", "\\{", "}", "\\}", ".", "\\.", "!", "\\!",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

func quoteStatusMarkdown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "(empty)"
	}
	parts := strings.Split(value, "\n")
	for i := range parts {
		parts[i] = "> " + escapeStatusMarkdown(parts[i])
	}
	return strings.Join(parts, "\n")
}

func statusTruncationSuffix(truncated bool) string {
	if truncated {
		return " (truncated)"
	}
	return ""
}
