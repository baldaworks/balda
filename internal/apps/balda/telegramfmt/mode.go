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

	RichMessagesDocsURL = "https://core.telegram.org/bots/api#rich-messages"
)

const (
	richMarkdownPromptRule = "Use Telegram Rich Markdown, following " + RichMessagesDocsURL + ". Balda sends it through Telegram rich messages. Do not write Telegram MarkdownV2 syntax. Do not pre-escape Telegram MarkdownV2 reserved characters."
	richHTMLPromptRule     = "Use Telegram Rich HTML, following " + RichMessagesDocsURL + ". Balda sends it through Telegram rich messages. Balda escapes unsafe raw <, >, & while preserving supported rich HTML tags."
	richMarkdownExample    = "" +
		"# Release notes\n\n" +
		"**Status:** shipped, _verified_, ~~obsolete path removed~~, ==highlighted==, ||internal note hidden||.\n\n" +
		"Read [the runbook](https://example.com/runbook) or contact @owner.\n\n" +
		"```bash\n" +
		"go test ./...\n" +
		"go tool golangci-lint run\n" +
		"```\n\n" +
		"- [x] Update dependencies\n" +
		"- [ ] Watch production\n" +
		"1. Deploy\n" +
		"2. Verify\n\n" +
		"> Keep this summary short.\n" +
		"> Quote blocks can span lines.\n\n" +
		"| Area | Result |\n" +
		"| --- | --- |\n" +
		"| API | OK |\n" +
		"| Bot | OK |\n\n" +
		"Use footnotes for details.[^1]\n\n" +
		"Inline math: $p95 < 250ms$.\n\n" +
		"<details>\n" +
		"<summary>More context</summary>\n\n" +
		"The retry path stayed enabled.\n\n" +
		"</details>\n\n" +
		"![diagram](https://example.com/diagram.png)\n\n" +
		"---\n\n" +
		"[^1]: Include only details that help the operator."
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
		return richMarkdownPromptRule, richMarkdownExample
	case ModeRichHTML:
		return richHTMLPromptRule, "<h2>Build</h2><p><b>Status:</b> success</p><p>Run <code>balda start</code>.</p>"
	case ModeMarkdownV2:
		return "Write normal Markdown or plain text. Balda converts it to Telegram MarkdownV2; use Markdown blank lines or lists for structure, and do not pre-escape Telegram MarkdownV2 reserved characters.", "**Build:** success. Run `balda start`."
	case ModeHTML:
		return "Use Telegram HTML parse mode. Supported tags: b/strong, i/em, u/ins, s/strike/del, tg-spoiler or span class=\"tg-spoiler\", a href, code, pre with nested code class=\"language-...\", blockquote expandable, tg-emoji emoji-id, tg-time unix/format. Balda escapes unsafe raw <, >, & while preserving supported Telegram HTML tags.", "<b>Build:</b> success. Run <code>balda start</code>."
	case ModeNone:
		return "Use plain text only. Do not use Markdown or HTML markup.", "Build: success. Run balda start."
	default:
		return richMarkdownPromptRule, richMarkdownExample
	}
}
