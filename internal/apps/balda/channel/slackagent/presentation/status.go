package presentation

import (
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

// RenderStatus renders a status response as Slack mrkdwn.
func RenderStatus(body statusfmt.Response) string {
	lines := []string{
		"🤖 *Balda Status*", "",
		"• *Provider:* " + statusCode(body.ProviderID) + " (" + statusCode(body.ProviderType) + ")",
		"• *Model:* " + statusCode(body.Model),
		"• *Reasoning:* " + statusCode(body.ReasoningEffort),
		"• *Channel:* " + statusCode(body.ChannelType),
		"• *Session:* " + statusCode(body.SessionID),
		"• *Session state:* " + statusCode(body.SessionState),
		"• *Turn:* " + statusTurnState(body.TurnActive),
	}
	if !body.TurnActive {
		return strings.Join(lines, "\n")
	}
	lines = append(lines,
		"• *Started:* "+statusCode(formatStatusTime(body.TurnStartedAt)),
		"• *Queued turns:* "+strconv.Itoa(body.PendingTurns), "",
		"*User text"+statusTruncationSuffix(body.UserTextTruncated)+":*",
		"```\n"+escapeStatusCodeBlock(body.UserText)+"\n```",
	)
	return strings.Join(lines, "\n")
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
