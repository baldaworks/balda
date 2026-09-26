package mattermost

// FormattingPromptRuleAndExample returns the model-facing presentation guidance
// for Mattermost and a matching example.
//
// Mattermost renders a Markdown subset natively. The rule keeps the model away
// from Telegram-specific markup and from constructs MM does not render.
func FormattingPromptRuleAndExample() (string, string) {
	return "Use Mattermost-compatible Markdown for presentation. " +
			"Prefer short sections, bullet lists, inline code, and fenced code blocks. " +
			"Do not emit Telegram-specific markup, HTML, or tables wider than the message view.",
		"**Status:** shipped\n\n- Verify the deployment\n- Watch production"
}
