package telegramfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
)

func NewPromptRegistryContribution() deliveryfx.PromptRegistryContribution {
	richMarkdownRule, richMarkdownExample := telegram.FormattingPromptRuleAndExample(telegram.FormattingModeRichMarkdown)
	richHTMLRule, richHTMLExample := telegram.FormattingPromptRuleAndExample(telegram.FormattingModeRichHTML)
	return deliveryfx.PromptRegistryContribution{
		Formats: []deliveryfmt.Format{
			{Name: deliveryfmt.NameTelegramRichMarkdown, Instructions: richMarkdownRule, Example: richMarkdownExample},
			{Name: deliveryfmt.NameTelegramRichHTML, Instructions: richHTMLRule, Example: richHTMLExample},
		},
		Formatters: []deliveryfmt.FormatterRegistration{
			{Name: deliveryfmt.NameTelegramRichMarkdown, Formatter: identityFormatter{name: deliveryfmt.NameTelegramRichMarkdown}},
			{Name: deliveryfmt.NameTelegramRichHTML, Formatter: htmlFormatter{}},
		},
		Routes: []deliveryfmt.Route{
			{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown, RegisteredName: deliveryfmt.NameTelegramRichMarkdown},
			{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichHTML, RegisteredName: deliveryfmt.NameTelegramRichHTML},
		},
	}
}

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

type htmlFormatter struct{}

func (htmlFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameTelegramRichHTML
}

func (htmlFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichHTML,
		Text:          telegram.EscapeHTML(text),
		PlainFallback: telegram.HTMLPlainText(text),
	}, nil
}
