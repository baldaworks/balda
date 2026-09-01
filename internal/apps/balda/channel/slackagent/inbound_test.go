package slackagent

import (
	"testing"
	"time"
)

func TestDecodeEventEnvelopeNormalizesNativeMessageIMPayload(t *testing.T) {
	t.Parallel()

	env, err := DecodeEventEnvelope([]byte(`{
		"type":"event_callback",
		"event_id":"evt-123",
		"team_id":"T123",
		"event":{
			"type":"message",
			"user":"U456",
			"text":" hello ",
			"channel":"D456",
			"ts":"1782234987.693923",
			"thread_ts":"1782234671.392669",
			"channel_type":"im"
		}
	}`))
	if err != nil {
		t.Fatalf("DecodeEventEnvelope() error = %v", err)
	}
	if env.Type != "event_callback" {
		t.Fatalf("Type = %q, want event_callback", env.Type)
	}
	if env.Event.EventID != "evt-123" || env.Event.EventType != "message" || env.Event.UserID != "U456" {
		t.Fatalf("Event = %+v", env.Event)
	}
	if env.Event.Conversation.TeamID != "T123" || env.Event.Conversation.ConversationID != "D456" || env.Event.Conversation.ThreadID != testThreadTS {
		t.Fatalf("Conversation = %+v", env.Event.Conversation)
	}
	if env.Event.Message == nil || env.Event.Message.MessageID != testStreamMessageTS || env.Event.Message.ThreadTS != testThreadTS {
		t.Fatalf("Message = %+v", env.Event.Message)
	}
	if env.Event.Text != testHelloText {
		t.Fatalf("Text = %q, want hello", env.Event.Text)
	}
	if env.Event.ChannelType != "im" {
		t.Fatalf("ChannelType = %q, want im", env.Event.ChannelType)
	}
}

func TestBuildIngressEnvelopeClassifiesChannelEntryAndContinuation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		eventType           string
		conversationID      string
		messageID           string
		threadTS            string
		wantExistingSession bool
		wantIgnored         bool
	}{
		{name: "public mention", eventType: "app_mention", conversationID: "C123", messageID: "100.1"},
		{name: "private mention", eventType: "app_mention", conversationID: "G123", messageID: "100.2"},
		{name: "known thread candidate", eventType: "message", conversationID: "C123", messageID: "100.3", threadTS: "100.1", wantExistingSession: true},
		{name: "ambient top level channel message", eventType: "message", conversationID: "C123", messageID: "100.4", wantIgnored: true},
		{name: "mention outside channel", eventType: "app_mention", conversationID: "D123", messageID: "100.5", wantIgnored: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootTS := test.messageID
			if test.threadTS != "" {
				rootTS = test.threadTS
			}
			env, err := BuildIngressEnvelope(EventEnvelope{
				Type: "event_callback",
				Event: Event{
					EventID:   "Ev123",
					EventType: test.eventType,
					UserID:    "U456",
					Text:      "hello",
					Conversation: ConversationRef{
						TeamID:         "T123",
						ConversationID: test.conversationID,
						ThreadID:       rootTS,
					},
					Message: &MessageRef{MessageID: test.messageID, ThreadTS: test.threadTS},
				},
			}, time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("BuildIngressEnvelope() error = %v", err)
			}
			if env.IgnoreEvent != test.wantIgnored {
				t.Fatalf("IgnoreEvent = %v, want %v", env.IgnoreEvent, test.wantIgnored)
			}
			if env.RequireExistingSession != test.wantExistingSession {
				t.Fatalf("RequireExistingSession = %v, want %v", env.RequireExistingSession, test.wantExistingSession)
			}
		})
	}
}
