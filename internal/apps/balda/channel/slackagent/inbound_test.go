package slackagent

import "testing"

func TestDecodeEventEnvelopeNormalizesSlackAgentPayload(t *testing.T) {
	t.Parallel()

	env, err := DecodeEventEnvelope([]byte(`{
		"type":"event_callback",
		"event_id":"evt-123",
		"team_id":"T123",
		"event":{
			"type":"message",
			"user_id":"U456",
			"text":" hello ",
			"conversation_id":"C456",
			"thread_id":"thread-789",
			"message_id":"msg-123",
			"reply_to_message_id":"msg-100"
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
	if env.Event.Conversation.TeamID != "T123" || env.Event.Conversation.ConversationID != "C456" || env.Event.Conversation.ThreadID != "thread-789" {
		t.Fatalf("Conversation = %+v", env.Event.Conversation)
	}
	if env.Event.Message == nil || env.Event.Message.MessageID != "msg-123" || env.Event.Message.ThreadTS != "msg-100" {
		t.Fatalf("Message = %+v", env.Event.Message)
	}
	if env.Event.Text != "hello" {
		t.Fatalf("Text = %q, want hello", env.Event.Text)
	}
}
