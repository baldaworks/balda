package deliveryfx

import (
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
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
