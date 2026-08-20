package deliveryfx

import (
	"regexp"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramfmt"
)

var (
	markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

type identityFormatter struct {
	name deliveryfmt.Name
}

func (f identityFormatter) Name() deliveryfmt.Name {
	return f.name
}

func (f identityFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          f.name,
		Text:          text,
		PlainFallback: text,
	}, nil
}

type telegramHTMLFormatter struct{}

func (telegramHTMLFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameTelegramRichHTML
}

type zulipMarkdownFormatter struct{}

func (zulipMarkdownFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameZulipMarkdown
}

func (zulipMarkdownFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameZulipMarkdown,
		Text:          text,
		PlainFallback: markdownPlainText(text),
	}, nil
}

func markdownPlainText(text string) string {
	plain := markdownImagePattern.ReplaceAllString(text, "$1: $2")
	plain = markdownLinkPattern.ReplaceAllString(plain, "$1 ($2)")
	plain = strings.NewReplacer("**", "", "__", "", "`", "").Replace(plain)
	return strings.TrimSpace(plain)
}

func (telegramHTMLFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichHTML,
		Text:          telegramfmt.HTML(text),
		PlainFallback: telegramfmt.HTMLPlainText(text),
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
		formatter := deliveryfmt.Formatter(identityFormatter{name: format.Name})
		switch format.Name {
		case deliveryfmt.NameTelegramRichHTML:
			formatter = telegramHTMLFormatter{}
		case deliveryfmt.NameZulipMarkdown:
			formatter = zulipMarkdownFormatter{}
		}
		formatters = append(formatters, deliveryfmt.FormatterRegistration{
			Name:      format.Name,
			Formatter: formatter,
		})
	}
	return deliveryfmt.NewRegistry(formats, formatters, deliveryfmt.BuiltinRoutes())
}
