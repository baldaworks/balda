package slackagent

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

const normalizedSlackAgentEventID = "slackagent:evt-123"

func TestNormalizeInboundPreservesIdentityAndCapabilities(t *testing.T) {
	t.Parallel()

	receivedAt := time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)
	locator := NewConversationLocator("T123", "C456")
	got := NormalizeInbound(locator, Event{
		EventID: " evt-123 ",
		UserID:  " slackagent:T123:U789 ",
		Text:    " answer ",
		Message: &MessageRef{
			Conversation: ConversationRef{TeamID: "T123", ConversationID: "C456"},
			MessageID:    "msg-456",
			ThreadTS:     "msg-100",
		},
	}, receivedAt)

	if got.ID != normalizedSlackAgentEventID || got.ProviderMessageID != "msg-456" {
		t.Fatalf("normalized slackagent = %+v", got)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatMrkdwn || !got.ProgressPolicy.Thinking || !got.ProgressPolicy.Typing || !got.ProgressPolicy.PlanUpdates {
		t.Fatalf("capabilities = %+v", got)
	}
	if got.ReplyToMessageID == 0 {
		t.Fatalf("reply correlation lost: %+v", got)
	}
}

func TestNormalizeInboundDeduplicatesSlackCallbacksForSameMessage(t *testing.T) {
	t.Parallel()
	const (
		mentionBody = `{"type":"event_callback","event_id":"EvMention","team_id":"T123","event":{"type":"app_mention","user":"U456","channel":"C456","text":"<@UBOT> hello","ts":"100.2","thread_ts":"100.1"}}`
		messageBody = `{"type":"event_callback","event_id":"EvMessage","team_id":"T123","event":{"type":"message","user":"U456","channel":"C456","channel_type":"channel","text":"<@UBOT> hello","ts":"100.2","thread_ts":"100.1"}}`
	)
	mention, err := DecodeIngressEnvelope([]byte(mentionBody), time.Time{})
	if err != nil {
		t.Fatalf("DecodeIngressEnvelope(mention) error = %v", err)
	}
	message, err := DecodeIngressEnvelope([]byte(messageBody), time.Time{})
	if err != nil {
		t.Fatalf("DecodeIngressEnvelope(message) error = %v", err)
	}
	if mention.IgnoreEvent || message.IgnoreEvent || mention.Inbound.ID != message.Inbound.ID {
		t.Fatalf("inbound IDs differ: mention=%q message=%q", mention.Inbound.ID, message.Inbound.ID)
	}
	if got, want := string(mention.Inbound.ID), "slackagent:message:T123:C456:100.2"; got != want {
		t.Fatalf("inbound ID = %q, want %q", got, want)
	}
}
