package presentation

import (
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

// RenderStatus renders a status response as Zulip Markdown.
func RenderStatus(body statusfmt.Response) string {
	lines := []string{
		"🤖 **Balda Status**", "",
		"- **Provider:** " + escapeStatusMarkdown(body.ProviderID) + " (" + escapeStatusMarkdown(body.ProviderType) + ")",
		"- **Model:** " + escapeStatusMarkdown(body.Model),
		"- **Reasoning:** " + escapeStatusMarkdown(body.ReasoningEffort),
		"- **Channel:** " + escapeStatusMarkdown(body.ChannelType),
		"- **Session:** " + escapeStatusMarkdown(body.SessionID),
		"- **Session state:** " + escapeStatusMarkdown(body.SessionState),
		"- **Turn:** " + statusTurnState(body.TurnActive),
	}
	if !body.TurnActive {
		return strings.Join(lines, "\n")
	}
	lines = append(lines,
		"- **Started:** "+escapeStatusMarkdown(formatStatusTime(body.TurnStartedAt)),
		"- **Queued turns:** "+strconv.Itoa(body.PendingTurns), "",
		"**User text"+statusTruncationSuffix(body.UserTextTruncated)+":**",
		quoteStatusMarkdown(body.UserText),
	)
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

func statusTurnState(active bool) string {
	if active {
		return "active"
	}
	return "idle"
}

func statusTruncationSuffix(truncated bool) string {
	if truncated {
		return " (truncated)"
	}
	return ""
}

func formatStatusTime(value time.Time) string {
	if value.IsZero() {
		return "unavailable"
	}
	return value.UTC().Format(time.RFC3339)
}
