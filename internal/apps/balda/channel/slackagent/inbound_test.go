package slackagent

import (
	"encoding/json"
	"strings"
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

func TestDecodeEventEnvelopePreservesOrderedSafeFileMetadata(t *testing.T) {
	t.Parallel()

	env, err := DecodeEventEnvelope([]byte(`{
		"type":"event_callback",
		"event_id":"evt-files",
		"team_id":"T123",
		"event":{
			"type":"message",
			"user":"U456",
			"channel":"D456",
			"ts":"1782234987.693923",
			"channel_type":"im",
			"subtype":"file_share",
			"files":[
				{"id":"F1","name":" photo.png ","mimetype":"image/png","size":7,"url_private":"https://files.slack.com/files-pri/secret-one"},
				{"id":"F2","title":" report ","mimetype":"application/pdf","size":11,"file_access":"check_file_info","url_private_download":"https://files.slack.com/files-pri/secret-two"}
			]
		}
	}`))
	if err != nil {
		t.Fatalf("DecodeEventEnvelope() error = %v", err)
	}
	files := env.Event.Files
	if len(files) != 2 || files[0].ID != "F1" || files[1].ID != "F2" {
		t.Fatalf("Files = %+v, want ordered [F1 F2]", files)
	}
	if files[0].Name != "photo.png" || files[0].MIMEType != "image/png" || files[0].SizeBytes != 7 {
		t.Fatalf("first file = %+v", files[0])
	}
	if files[1].Title != "report" || files[1].FileAccess != "check_file_info" {
		t.Fatalf("second file = %+v", files[1])
	}
	encoded, err := json.Marshal(env.Event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "secret-one") || strings.Contains(string(encoded), "secret-two") {
		t.Fatalf("serialized event leaked private URL: %s", encoded)
	}
}

func TestBuildIngressEnvelopeClassifiesAddressedInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		eventType      string
		conversationID string
		messageID      string
		threadTS       string
		wantContext    bool
		wantIgnored    bool
	}{
		{name: "public mention", eventType: "app_mention", conversationID: "C123", messageID: "100.1"},
		{name: "private thread mention", eventType: "app_mention", conversationID: "G123", messageID: "100.2", threadTS: "100.1", wantContext: true},
		{name: "ambient channel reply", eventType: "message", conversationID: "C123", messageID: "100.3", threadTS: "100.1", wantIgnored: true},
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
			if (env.ThreadContext != nil) != test.wantContext {
				t.Fatalf("ThreadContext = %+v, wantContext %v", env.ThreadContext, test.wantContext)
			}
			if env.ThreadContext != nil && (env.ThreadContext.ConversationID != test.conversationID || env.ThreadContext.RootTS != test.threadTS || env.ThreadContext.BeforeTS != test.messageID) {
				t.Fatalf("ThreadContext = %+v", env.ThreadContext)
			}
		})
	}
}

func TestBuildIngressEnvelopeClassifiesFileBearingInputs(t *testing.T) {
	t.Parallel()
	file := FileRef{ID: "F123", Name: "report.pdf", MIMEType: "application/pdf", SizeBytes: 12}
	tests := []struct {
		name        string
		eventType   string
		channelID   string
		channelType string
		subtype     string
		text        string
		files       []FileRef
		botID       string
		hidden      bool
		wantIgnored bool
	}{
		{name: "attachment only DM", eventType: "message", channelID: "D123", channelType: "im", files: []FileRef{file}},
		{name: "DM file share", eventType: "message", channelID: "D123", channelType: "im", subtype: "file_share", files: []FileRef{file}},
		{name: "file bearing channel mention", eventType: "app_mention", channelID: "C123", channelType: "channel", files: []FileRef{file}},
		{name: "ordinary channel file share", eventType: "message", channelID: "C123", channelType: "channel", subtype: "file_share", files: []FileRef{file}, wantIgnored: true},
		{name: "empty file share", eventType: "message", channelID: "D123", channelType: "im", subtype: "file_share", wantIgnored: true},
		{name: "edited message", eventType: "message", channelID: "D123", channelType: "im", subtype: "message_changed", files: []FileRef{file}, wantIgnored: true},
		{name: "bot file", eventType: "message", channelID: "D123", channelType: "im", files: []FileRef{file}, botID: "B123", wantIgnored: true},
		{name: "hidden file", eventType: "message", channelID: "D123", channelType: "im", files: []FileRef{file}, hidden: true, wantIgnored: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := BuildIngressEnvelope(EventEnvelope{
				Type: "event_callback",
				Event: Event{
					EventID:     "EvFile",
					EventType:   test.eventType,
					UserID:      "U456",
					Text:        test.text,
					ChannelType: test.channelType,
					Subtype:     test.subtype,
					BotID:       test.botID,
					Hidden:      test.hidden,
					Files:       test.files,
					Conversation: ConversationRef{
						TeamID:         "T123",
						ConversationID: test.channelID,
						ThreadID:       "100.1",
					},
					Message: &MessageRef{MessageID: "100.1"},
				},
			}, time.Time{})
			if err != nil {
				t.Fatalf("BuildIngressEnvelope() error = %v", err)
			}
			if envelope.IgnoreEvent != test.wantIgnored {
				t.Fatalf("IgnoreEvent = %v, want %v", envelope.IgnoreEvent, test.wantIgnored)
			}
			if !test.wantIgnored {
				if len(envelope.Files) != 1 || envelope.Files[0].ID != file.ID {
					t.Fatalf("Files = %+v, want one copied file", envelope.Files)
				}
				if len(envelope.Chat.Attachments) != 0 {
					t.Fatalf("Chat attachments were populated before persistence: %+v", envelope.Chat.Attachments)
				}
			}
		})
	}
}
