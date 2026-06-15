package telegramfmt

import (
	"fmt"
	"strings"
)

const (
	ModeRichMarkdown = "rich_markdown"
	ModeRichHTML     = "rich_html"
	ModeMarkdownV2   = "markdownv2"
	ModeHTML         = "html"
	ModeNone         = "none"
)

// NormalizeMode normalizes balda.telegram.formatting_mode.
// Empty input falls back to the default mode.
func NormalizeMode(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return ModeRichMarkdown
	}
	return trimmed
}

// ValidateMode normalizes and validates balda.telegram.formatting_mode.
func ValidateMode(raw string) (string, error) {
	mode := NormalizeMode(raw)
	switch mode {
	case ModeRichMarkdown, ModeRichHTML, ModeMarkdownV2, ModeHTML, ModeNone:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"invalid balda.telegram.formatting_mode %q: allowed values are %q, %q, %q, %q, %q",
			strings.TrimSpace(raw),
			ModeRichMarkdown,
			ModeRichHTML,
			ModeMarkdownV2,
			ModeHTML,
			ModeNone,
		)
	}
}

// TelegramParseMode returns the Telegram parse_mode value for normalized mode.
// Empty string means parse_mode should be omitted.
func TelegramParseMode(mode string) string {
	switch NormalizeMode(mode) {
	case ModeHTML:
		return "HTML"
	case ModeNone, ModeRichMarkdown, ModeRichHTML:
		return ""
	default:
		return "MarkdownV2"
	}
}

// PromptRuleAndExample returns concise mode-specific instruction text.
func PromptRuleAndExample(mode string) (rule string, example string) {
	switch NormalizeMode(mode) {
	case ModeRichMarkdown:
		return "Write rich-message Markdown or plain text. Balda sends it through Telegram rich messages; use Markdown headings, blank lines, lists, links, blockquotes, fenced code, and tables when they make the answer easier to scan. Do not pre-escape Telegram MarkdownV2 reserved characters.", "## Build\n\n**Status:** success\n\nRun `balda start`."
	case ModeRichHTML:
		return "Use Telegram rich-message HTML. Supported rich blocks include paragraphs, headings h1-h6, pre/code, blockquote, aside, details/summary, lists, tables, footer, hr, and rich inline tags such as b/strong, i/em, u/ins, s/strike/del, code, a href, tg-spoiler, sub, sup, mark, tg-math, tg-emoji, and tg-time. Balda escapes unsafe raw <, >, & while preserving supported rich HTML tags.", "<h2>Build</h2><p><b>Status:</b> success</p><p>Run <code>balda start</code>.</p>"
	case ModeMarkdownV2:
		return "Write normal Markdown or plain text. Balda converts it to Telegram MarkdownV2; use Markdown blank lines or lists for structure, and do not pre-escape Telegram MarkdownV2 reserved characters.", "**Build:** success. Run `balda start`."
	case ModeHTML:
		return "Use Telegram HTML parse mode. Supported tags: b/strong, i/em, u/ins, s/strike/del, tg-spoiler or span class=\"tg-spoiler\", a href, code, pre with nested code class=\"language-...\", blockquote expandable, tg-emoji emoji-id, tg-time unix/format. Balda escapes unsafe raw <, >, & while preserving supported Telegram HTML tags.", "<b>Build:</b> success. Run <code>balda start</code>."
	case ModeNone:
		return "Use plain text only. Do not use Markdown or HTML markup.", "Build: success. Run balda start."
	default:
		return "Write rich-message Markdown or plain text. Balda sends it through Telegram rich messages; use Markdown headings, blank lines, lists, links, blockquotes, fenced code, and tables when they make the answer easier to scan. Do not pre-escape Telegram MarkdownV2 reserved characters.", "## Build\n\n**Status:** success\n\nRun `balda start`."
	}
}
