package zulipfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
)

func NewPromptRegistryContribution() deliveryfx.PromptRegistryContribution {
	rule, example := zulip.FormattingPromptRuleAndExample()
	return deliveryfx.PromptRegistryContribution{
		Formats: []deliveryfmt.Format{
			{Name: deliveryfmt.NameZulipMarkdown, Instructions: rule, Example: example},
		},
		Formatters: []deliveryfmt.FormatterRegistration{
			{Name: deliveryfmt.NameZulipMarkdown, Formatter: markdownFormatter{}},
		},
		Routes: []deliveryfmt.Route{
			{Transport: deliveryfmt.TransportZulip, DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown, RegisteredName: deliveryfmt.NameZulipMarkdown},
		},
	}
}

type markdownFormatter struct{}

func (markdownFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameZulipMarkdown
}

func (markdownFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameZulipMarkdown,
		Text:          text,
		PlainFallback: zulip.MarkdownPlainText(text),
	}, nil
}
