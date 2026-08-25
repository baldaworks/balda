package zulip

import "strings"

func FormattingPromptRuleAndExample() (string, string) {
	return "Use Zulip-compatible Markdown for presentation. Prefer short sections, bullets, links, and fenced code blocks; do not emit Telegram-specific markup.", "**Status:** shipped\n\n- Verify the deployment\n- Watch production"
}

func MarkdownPlainText(text string) string {
	plain := markdownImagePattern.ReplaceAllString(text, "$1: $2")
	plain = markdownLinkPattern.ReplaceAllString(plain, "$1 ($2)")
	plain = strings.NewReplacer("**", "", "__", "", "`", "").Replace(plain)
	return strings.TrimSpace(plain)
}
