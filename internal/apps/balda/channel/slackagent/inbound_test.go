package slackagent

import (
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
)

func TestNormalizeInboundPreservesSlackAgentIdentityAndCapability(t *testing.T) {
	t.Parallel()

	got := NormalizeInbound(InboundMessage{
		Locator:          NewConversationLocator("T123", "C456"),
		EventID:          " evt-123 ",
		MessageID:        "msg-456",
		ReplyToMessageID: "msg-100",
		UserID:           "slack:T123:U789",
		Text:             " answer ",
		ReceivedAt:       time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC),
	})

	if got.ID != "slack_agent:evt-123" || got.ProviderMessageID != "msg-456" {
		t.Fatalf("identity = %+v, want stable Slack Agent event identity", got)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatMrkdwn || !got.ProgressPolicy.Typing || !got.ProgressPolicy.Thinking {
		t.Fatalf("capability = %+v, want Slack Agent mrkdwn and progress", got)
	}
}
