package deliveryfx

import (
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/telegramfmt"
)

type passthroughFormatter struct {
	name deliveryfmt.Name
}

func (f passthroughFormatter) Name() deliveryfmt.Name {
	return f.name
}

func (f passthroughFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          f.name,
		Text:          text,
		PlainFallback: text,
	}, nil
}

func newMessageFormatRegistry() (*deliveryfmt.Registry, error) {
	richMarkdownRule, richMarkdownExample := telegramfmt.PromptRuleAndExample(telegramfmt.ModeRichMarkdown)
	richHTMLRule, richHTMLExample := telegramfmt.PromptRuleAndExample(telegramfmt.ModeRichHTML)
	formats := []deliveryfmt.Format{
		{Name: deliveryfmt.NameTelegramRichMarkdown, Instructions: richMarkdownRule, Example: richMarkdownExample},
		{Name: deliveryfmt.NameTelegramRichHTML, Instructions: richHTMLRule, Example: richHTMLExample},
		{Name: deliveryfmt.NameSlackMrkdwn, Instructions: "Use Slack mrkdwn for presentation. Prefer short sections, bullets, links, and fenced code blocks; do not emit Telegram-specific markup.", Example: "*Status:* shipped\n• Verify the deployment\n• Watch production"},
		{Name: deliveryfmt.NameZulipMarkdown, Instructions: "Use Zulip-compatible Markdown for presentation. Prefer short sections, bullets, links, and fenced code blocks; do not emit Telegram-specific markup.", Example: "**Status:** shipped\n\n- Verify the deployment\n- Watch production"},
		{Name: deliveryfmt.NamePlainText, Instructions: "Use plain text only. Do not use Markdown, HTML, or provider-specific presentation markup.", Example: "Status: shipped. Verify the deployment and watch production."},
	}
	formatters := make([]deliveryfmt.FormatterRegistration, 0, len(formats))
	for _, format := range formats {
		formatters = append(formatters, deliveryfmt.FormatterRegistration{
			Name:      format.Name,
			Formatter: passthroughFormatter{name: format.Name},
		})
	}
	return deliveryfmt.NewRegistry(formats, formatters, deliveryfmt.BuiltinRoutes())
}
