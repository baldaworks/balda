package deliveryfx

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestMessageFormatRegistryProvidesCurrentPromptRoutes(t *testing.T) {
	t.Parallel()

	registry, err := newMessageFormatRegistry()
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

	registry, err := newMessageFormatRegistry()
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
