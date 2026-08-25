package deliveryfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/presentation"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	telegrampresentation "github.com/baldaworks/balda/internal/apps/balda/channel/telegram/presentation"
	baldazulip "github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
)

func TestMessageFormatRegistryProvidesCurrentPromptRoutes(t *testing.T) {
	t.Parallel()

	registry, err := newMessageFormatRegistry(promptRegistryParams{
		Contributions: []PromptRegistryContribution{
			telegramfxPromptContributionForTest(),
			zulipfxPromptContributionForTest(),
		},
	})
	if err != nil {
		t.Fatalf("newMessageFormatRegistry() error = %v", err)
	}

	for _, test := range []struct {
		transport      string
		deliveryFormat deliveryfmt.DeliveryFormat
		wantName       deliveryfmt.Name
		wantRule       string
	}{
		{deliveryfmt.TransportTelegram, deliveryfmt.DeliveryFormatRichMarkdown, deliveryfmt.NameTelegramRichMarkdown, "Telegram Rich Markdown"},
		{deliveryfmt.TransportTelegram, deliveryfmt.DeliveryFormatRichHTML, deliveryfmt.NameTelegramRichHTML, "Telegram Rich HTML"},
		{deliveryfmt.TransportTelegram, deliveryfmt.DeliveryFormatNone, deliveryfmt.NamePlainText, "plain text only"},
		{deliveryfmt.TransportSlack, deliveryfmt.DeliveryFormatMrkdwn, deliveryfmt.NameSlackMrkdwn, "Slack mrkdwn"},
		{deliveryfmt.TransportZulip, deliveryfmt.DeliveryFormatMarkdown, deliveryfmt.NameZulipMarkdown, "Zulip-compatible Markdown"},
	} {
		t.Run(test.transport+"/"+string(test.deliveryFormat), func(t *testing.T) {
			name, format, _, err := registry.Resolve(test.transport, test.deliveryFormat)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if name != test.wantName || !strings.Contains(format.Instructions, test.wantRule) {
				t.Fatalf("Resolve() = %q, %+v; want %q containing %q", name, format, test.wantName, test.wantRule)
			}
		})
	}
}

func TestMessageFormatRegistryFormatsCurrentRoutes(t *testing.T) {
	t.Parallel()

	registry, err := newMessageFormatRegistry(promptRegistryParams{
		Contributions: []PromptRegistryContribution{
			telegramfxPromptContributionForTest(),
			zulipfxPromptContributionForTest(),
		},
	})
	if err != nil {
		t.Fatalf("newMessageFormatRegistry() error = %v", err)
	}
	tests := []struct {
		name            string
		transport       string
		deliveryFormat  deliveryfmt.DeliveryFormat
		input           string
		wantText        string
		wantPlain       string
		wantMessageName deliveryfmt.Name
	}{
		{
			name:            "telegram rich markdown",
			transport:       deliveryfmt.TransportTelegram,
			deliveryFormat:  deliveryfmt.DeliveryFormatRichMarkdown,
			input:           "**Build:** passed",
			wantText:        "**Build:** passed",
			wantPlain:       "**Build:** passed",
			wantMessageName: deliveryfmt.NameTelegramRichMarkdown,
		},
		{
			name:            "telegram rich html",
			transport:       deliveryfmt.TransportTelegram,
			deliveryFormat:  deliveryfmt.DeliveryFormatRichHTML,
			input:           `<b>Build</b> <script>alert(1)</script> &amp; done`,
			wantText:        `<b>Build</b> &lt;script&gt;alert(1)&lt;/script&gt; &amp; done`,
			wantPlain:       "Build alert(1) & done",
			wantMessageName: deliveryfmt.NameTelegramRichHTML,
		},
		{
			name:            "telegram plain",
			transport:       deliveryfmt.TransportTelegram,
			deliveryFormat:  deliveryfmt.DeliveryFormatNone,
			input:           "<b>literal</b>",
			wantText:        "<b>literal</b>",
			wantPlain:       "<b>literal</b>",
			wantMessageName: deliveryfmt.NamePlainText,
		},
		{
			name:            "slack native",
			transport:       deliveryfmt.TransportSlack,
			deliveryFormat:  deliveryfmt.DeliveryFormatMrkdwn,
			input:           "*Build:* passed",
			wantText:        "*Build:* passed",
			wantPlain:       "*Build:* passed",
			wantMessageName: deliveryfmt.NameSlackMrkdwn,
		},
		{
			name:            "zulip native",
			transport:       deliveryfmt.TransportZulip,
			deliveryFormat:  deliveryfmt.DeliveryFormatMarkdown,
			input:           "**Build:** passed",
			wantText:        "**Build:** passed",
			wantPlain:       "Build: passed",
			wantMessageName: deliveryfmt.NameZulipMarkdown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, formatter, err := registry.Resolve(test.transport, test.deliveryFormat)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			message, err := formatter.Format(test.input)
			if err != nil {
				t.Fatalf("Format() error = %v", err)
			}
			if message.Name != test.wantMessageName || message.Text != test.wantText || message.PlainFallback != test.wantPlain {
				t.Fatalf("Format() = %+v, want name=%q text=%q plain=%q", message, test.wantMessageName, test.wantText, test.wantPlain)
			}
		})
	}
}

func TestStructuredMessageRegistryProvidesCurrentStructuredRoutes(t *testing.T) {
	t.Parallel()

	registry, err := newStructuredMessageRegistry(structuredRegistryParams{
		Registrars: []StructuredRegistryRegistrar{
			permissionfmt.RegisterStructuredRenderers,
			progressfmt.RegisterStructuredRenderers,
			questionfmt.RegisterStructuredRenderers,
			telegramfxRegisterStructuredRenderersForTest,
			slackagentRegisterStructuredRenderersForTest,
		},
	})
	if err != nil {
		t.Fatalf("newStructuredMessageRegistry() error = %v", err)
	}

	for _, test := range []struct {
		name      string
		transport string
		render    func(context.Context, *deliveryfmt.StructuredRegistry) (deliveryfmt.StructuredPresentation, error)
	}{
		{
			name:      "permission telegram",
			transport: deliveryfmt.TransportTelegram,
			render: func(ctx context.Context, reg *deliveryfmt.StructuredRegistry) (deliveryfmt.StructuredPresentation, error) {
				return deliveryfmt.RenderStructured(ctx, reg, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[permissioncmd.Request]{
					Descriptor: permissionfmt.RequestDescriptor,
					Body: permissioncmd.Request{
						ToolCall: permissioncmd.ToolCall{Title: "Command approval"},
					},
				})
			},
		},
		{
			name:      "question slack agent",
			transport: deliveryfmt.TransportSlackAgent,
			render: func(ctx context.Context, reg *deliveryfmt.StructuredRegistry) (deliveryfmt.StructuredPresentation, error) {
				return deliveryfmt.RenderStructured(ctx, reg, deliveryfmt.TransportSlackAgent, deliveryfmt.StructuredEnvelope[questionfmt.Request]{
					Descriptor: questionfmt.RequestDescriptor,
					Body: questionfmt.Request{
						Prompt:  "Continue?",
						Options: []questioncmd.Option{{ID: "yes", Label: "Yes"}},
					},
				})
			},
		},
		{
			name:      "progress zulip",
			transport: deliveryfmt.TransportZulip,
			render: func(ctx context.Context, reg *deliveryfmt.StructuredRegistry) (deliveryfmt.StructuredPresentation, error) {
				return deliveryfmt.RenderStructured(ctx, reg, deliveryfmt.TransportZulip, deliveryfmt.StructuredEnvelope[progressfmt.Request]{
					Descriptor: progressfmt.RequestDescriptor,
					Body: progressfmt.Request{
						Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressPlanUpdate, Text: "plan", Visible: true},
					},
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			presentation, err := test.render(context.Background(), registry)
			if err != nil {
				t.Fatalf("RenderStructured() error = %v", err)
			}
			if strings.TrimSpace(presentation.Text) == "" {
				t.Fatalf("presentation = %+v, want non-empty text", presentation)
			}
		})
	}
}

func telegramfxPromptContributionForTest() PromptRegistryContribution {
	richMarkdownRule, richMarkdownExample := baldatelegram.FormattingPromptRuleAndExample(baldatelegram.FormattingModeRichMarkdown)
	richHTMLRule, richHTMLExample := baldatelegram.FormattingPromptRuleAndExample(baldatelegram.FormattingModeRichHTML)
	return PromptRegistryContribution{
		Formats: []deliveryfmt.Format{
			{Name: deliveryfmt.NameTelegramRichMarkdown, Instructions: richMarkdownRule, Example: richMarkdownExample},
			{Name: deliveryfmt.NameTelegramRichHTML, Instructions: richHTMLRule, Example: richHTMLExample},
		},
		Formatters: []deliveryfmt.FormatterRegistration{
			{Name: deliveryfmt.NameTelegramRichMarkdown, Formatter: identityFormatter{name: deliveryfmt.NameTelegramRichMarkdown}},
			{Name: deliveryfmt.NameTelegramRichHTML, Formatter: testTelegramHTMLFormatter{}},
		},
		Routes: []deliveryfmt.Route{
			{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown, RegisteredName: deliveryfmt.NameTelegramRichMarkdown},
			{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichHTML, RegisteredName: deliveryfmt.NameTelegramRichHTML},
		},
	}
}

func zulipfxPromptContributionForTest() PromptRegistryContribution {
	rule, example := baldazulip.FormattingPromptRuleAndExample()
	return PromptRegistryContribution{
		Formats: []deliveryfmt.Format{
			{Name: deliveryfmt.NameZulipMarkdown, Instructions: rule, Example: example},
		},
		Formatters: []deliveryfmt.FormatterRegistration{
			{Name: deliveryfmt.NameZulipMarkdown, Formatter: testZulipMarkdownFormatter{}},
		},
		Routes: []deliveryfmt.Route{
			{Transport: deliveryfmt.TransportZulip, DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown, RegisteredName: deliveryfmt.NameZulipMarkdown},
		},
	}
}

func telegramfxRegisterStructuredRenderersForTest(reg *deliveryfmt.StructuredRegistry) error {
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, questionfmt.RequestDescriptor, testTelegramQuestionRenderer{}); err != nil {
		return err
	}
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, permissionfmt.RequestDescriptor, testTelegramPermissionRenderer{}); err != nil {
		return err
	}
	return deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, progressfmt.RequestDescriptor, testTelegramProgressRenderer{})
}

func slackagentRegisterStructuredRenderersForTest(reg *deliveryfmt.StructuredRegistry) error {
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportSlackAgent, questionfmt.RequestDescriptor, testSlackAgentQuestionRenderer{}); err != nil {
		return err
	}
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportSlackAgent, permissionfmt.RequestDescriptor, testSlackAgentPermissionRenderer{}); err != nil {
		return err
	}
	return deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportSlackAgent, progressfmt.RequestDescriptor, testSlackAgentProgressRenderer{})
}

type testTelegramHTMLFormatter struct{}

type testZulipMarkdownFormatter struct{}

func (testZulipMarkdownFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameZulipMarkdown
}

func (testZulipMarkdownFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameZulipMarkdown,
		Text:          text,
		PlainFallback: baldazulip.MarkdownPlainText(text),
	}, nil
}

func (testTelegramHTMLFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameTelegramRichHTML
}

func (testTelegramHTMLFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichHTML,
		Text:          baldatelegram.EscapeHTML(text),
		PlainFallback: baldatelegram.HTMLPlainText(text),
	}, nil
}

type testTelegramQuestionRenderer struct{}

func (testTelegramQuestionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[questionfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           telegrampresentation.RenderQuestion(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type testTelegramPermissionRenderer struct{}

func (testTelegramPermissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           telegrampresentation.RenderPermission(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type testTelegramProgressRenderer struct{}

func (testTelegramProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[progressfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           telegrampresentation.RenderProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type testSlackAgentQuestionRenderer struct{}

func (testSlackAgentQuestionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[questionfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderQuestion(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

type testSlackAgentPermissionRenderer struct{}

func (testSlackAgentPermissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderPermission(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

type testSlackAgentProgressRenderer struct{}

func (testSlackAgentProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[progressfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}, nil
}
