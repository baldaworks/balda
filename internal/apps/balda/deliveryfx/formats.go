package deliveryfx

import (
	"regexp"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"go.uber.org/fx"
)

var (
	markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

type StructuredRegistryRegistrar func(*deliveryfmt.StructuredRegistry) error

type PromptRegistryContribution struct {
	Formats    []deliveryfmt.Format
	Formatters []deliveryfmt.FormatterRegistration
	Routes     []deliveryfmt.Route
}

type ChannelAdapterBinding struct {
	ChannelType string
	Adapter     deliverycmd.Adapter
}

type structuredRegistryParams struct {
	fx.In

	Registrars []StructuredRegistryRegistrar `group:"balda_delivery_structured_registrar"`
}

type promptRegistryParams struct {
	fx.In

	Contributions []PromptRegistryContribution `group:"balda_delivery_prompt_contribution"`
}

type channelAdapterBindingsParams struct {
	fx.In

	Bindings []ChannelAdapterBinding `group:"balda_delivery_channel_adapter"`
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

func newMessageFormatRegistry(params promptRegistryParams) (*deliveryfmt.Registry, error) {
	formats := []deliveryfmt.Format{
		{Name: deliveryfmt.NameSlackMrkdwn, Instructions: "Use Slack mrkdwn for presentation. Prefer short sections, bullets, links, and fenced code blocks; do not emit Telegram-specific markup.", Example: "*Status:* shipped\n• Verify the deployment\n• Watch production"},
		{Name: deliveryfmt.NameZulipMarkdown, Instructions: "Use Zulip-compatible Markdown for presentation. Prefer short sections, bullets, links, and fenced code blocks; do not emit Telegram-specific markup.", Example: "**Status:** shipped\n\n- Verify the deployment\n- Watch production"},
		{Name: deliveryfmt.NamePlainText, Instructions: "Use plain text only. Do not use Markdown, HTML, or provider-specific presentation markup.", Example: "Status: shipped. Verify the deployment and watch production."},
	}
	formatters := make([]deliveryfmt.FormatterRegistration, 0, len(formats))
	for _, format := range formats {
		formatter := deliveryfmt.Formatter(identityFormatter{name: format.Name})
		if format.Name == deliveryfmt.NameZulipMarkdown {
			formatter = zulipMarkdownFormatter{}
		}
		formatters = append(formatters, deliveryfmt.FormatterRegistration{
			Name:      format.Name,
			Formatter: formatter,
		})
	}
	routes := deliveryfmt.BuiltinRoutes()
	for _, contribution := range params.Contributions {
		formats = append(formats, contribution.Formats...)
		formatters = append(formatters, contribution.Formatters...)
		routes = append(routes, contribution.Routes...)
	}
	return deliveryfmt.NewRegistry(formats, formatters, routes)
}

func newStructuredMessageRegistry(params structuredRegistryParams) (*deliveryfmt.StructuredRegistry, error) {
	reg := deliveryfmt.NewStructuredRegistry()
	for _, register := range params.Registrars {
		if err := register(reg); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

func NewStructuredRegistrar[T any](
	transport string,
	desc deliveryfmt.Descriptor[T],
	renderer deliveryfmt.StructuredRenderer[T],
) StructuredRegistryRegistrar {
	return func(reg *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(reg, transport, desc, renderer)
	}
}
