package slackagent

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestNormalizeInboundPreservesIdentityAndCapabilities(t *testing.T) {
	t.Parallel()

	receivedAt := time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)
	locator := NewConversationLocator("T123", "C456")
	got := NormalizeInbound(locator, Event{
		EventID: " evt-123 ",
		UserID:  " slack:T123:U789 ",
		Text:    " answer ",
		Message: &MessageRef{
			Conversation: ConversationRef{TeamID: "T123", ConversationID: "C456"},
			MessageID:    "msg-456",
			ThreadTS:     "msg-100",
		},
	}, receivedAt)

	if got.ID != "slack_agent:evt-123" || got.ProviderMessageID != "msg-456" {
		t.Fatalf("normalized Slack Agent = %+v", got)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatMrkdwn || !got.ProgressPolicy.Thinking || !got.ProgressPolicy.Typing || !got.ProgressPolicy.PlanUpdates {
		t.Fatalf("capabilities = %+v", got)
	}
	if got.ReplyToMessageID == 0 {
		t.Fatalf("reply correlation lost: %+v", got)
	}
}
