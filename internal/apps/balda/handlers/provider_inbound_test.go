package handlers

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	baldaslackagent "github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	baldazulip "github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

const (
	testSlackInboundID      = "slackagent:1712345678.1234"
	testSlackAgentInboundID = "slackagent:evt-123"
	testZulipInboundID      = "zulip:42"
)

func TestProviderInboundNormalizationPreservesIdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	receivedAt := time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)

	t.Run("telegram media group", func(t *testing.T) {
		message := baldatelegram.MessageContext{
			Locator:          telegramref.NewLocator(-1001, 77),
			ChatID:           -1001,
			TopicID:          77,
			MessageID:        101,
			ReplyToMessageID: 90,
			UserID:           22,
			MediaGroupID:     " album-42 ",
			Attachments: []attachment.Descriptor{
				{Kind: attachment.KindPhoto, FileID: "first"},
				{Kind: attachment.KindDocument, FileID: "second"},
			},
			DeliveryOptions: deliveryfmt.Options{
				DeliveryFormat: deliveryfmt.DeliveryFormatRichHTML,
				ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, PlanUpdates: true},
			},
		}
		first := baldatelegram.NormalizeInbound(message, "one caption", receivedAt)
		message.MessageID = 102
		redelivered := baldatelegram.NormalizeInbound(message, "one caption", receivedAt)
		const wantID = "telegram:-1001:77:user:22:media-group:album-42"
		if string(first.ID) != wantID || redelivered.ID != first.ID || first.ProviderMessageID != "album-42" {
			t.Fatalf("identity = %+v redelivered = %+v", first, redelivered)
		}
		if first.DeliveryFormat != deliveryfmt.DeliveryFormatRichHTML || len(first.Attachments) != 2 {
			t.Fatalf("capability = %+v", first)
		}
	})

	t.Run("slackagent direct", func(t *testing.T) {
		got := normalizeSlackInbound(slackInboundMessage{
			Locator: slackDMLocator("T123", "D456"), ProviderMessageID: " 1712345678.1234 ",
			UserID: slackUserID("T123", "U789"), Text: "hello", Direct: true, ReceivedAt: receivedAt,
		})
		if got.ID != testSlackInboundID || got.DeliveryFormat != deliveryfmt.DeliveryFormatMrkdwn || !got.Direct {
			t.Fatalf("normalized slackagent = %+v", got)
		}
	})

	t.Run("slackagent event", func(t *testing.T) {
		got := baldaslackagent.NormalizeInbound(
			baldaslackagent.NewConversationLocator("T123", "C456"),
			baldaslackagent.Event{
				EventID: " evt-123 ",
				UserID:  slackUserID("T123", "U789"),
				Text:    " answer ",
				Message: &baldaslackagent.MessageRef{
					Conversation: baldaslackagent.ConversationRef{TeamID: "T123", ConversationID: "C456"},
					MessageID:    "msg-456",
					ThreadTS:     "msg-100",
				},
			},
			receivedAt,
		)
		if got.ID != testSlackAgentInboundID || got.ProviderMessageID != "msg-456" || !got.ProgressPolicy.Thinking {
			t.Fatalf("normalized slackagent event = %+v", got)
		}
	})

	t.Run("zulip", func(t *testing.T) {
		got := baldazulip.NormalizeInbound(
			baldazulip.NewDMLocator(101),
			42,
			baldazulip.UserID(101),
			" hello ",
			true,
			receivedAt,
		)
		if got.ID != testZulipInboundID || got.DeliveryFormat != deliveryfmt.DeliveryFormatMarkdown || !got.ProgressPolicy.PlanUpdates || !got.Direct {
			t.Fatalf("normalized Zulip = %+v", got)
		}
	})
}
