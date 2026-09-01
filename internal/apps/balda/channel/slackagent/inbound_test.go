package slackagent

import "testing"

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
	if env.Event.Conversation.TeamID != "T123" || env.Event.Conversation.ConversationID != "D456" || env.Event.Conversation.ThreadID != "1782234671.392669" {
		t.Fatalf("Conversation = %+v", env.Event.Conversation)
	}
	if env.Event.Message == nil || env.Event.Message.MessageID != "1782234987.693923" || env.Event.Message.ThreadTS != "1782234671.392669" {
		t.Fatalf("Message = %+v", env.Event.Message)
	}
	if env.Event.Text != "hello" {
		t.Fatalf("Text = %q, want hello", env.Event.Text)
	}
	if env.Event.ChannelType != "im" {
		t.Fatalf("ChannelType = %q, want im", env.Event.ChannelType)
	}
}
