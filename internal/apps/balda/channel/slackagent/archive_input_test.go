package slackagent

import (
	"context"
	"github.com/rs/zerolog"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

const archiveOriginalText = " original "

func TestOriginalInputClassifiesAudienceIndependentlyOfThread(t *testing.T) {
	for _, test := range []struct{ kind, audience string }{{"im", "personal"}, {"mpim", "group"}, {"channel", "group"}, {"group", "group"}, {"", "unknown"}} {
		t.Run(test.kind, func(t *testing.T) {
			got := NormalizeChatRequest(NewThreadLocator("T1", "C1", "1.1"), Event{EventID: "E1", UserID: "U1", Text: archiveOriginalText, ChannelType: test.kind, Message: &MessageRef{MessageID: "1.2", ThreadTS: "1.1"}, Files: []FileRef{{ID: "F1", Name: "current.pdf", MIMEType: "application/pdf", privateURL: "https://secret"}}}, time.Now())
			if got.OriginalInput == nil {
				t.Fatal("original input missing")
			}
			original := got.OriginalInput
			if original.Text != archiveOriginalText || original.Speaker.Principal != "U1" || original.Speaker.Name != "" {
				t.Fatalf("original = %+v", original)
			}
			if original.Location.Audience != test.audience || original.Location.Subdivision != "thread" {
				t.Fatalf("location = %+v", original.Location)
			}
			if len(original.Attachments) != 1 || original.Attachments[0].Name != "current.pdf" {
				t.Fatalf("attachments = %+v", original.Attachments)
			}
			got.Text = "hydrated history and augmented skill"
			normalized := got.NormalizedInbound()
			got.OriginalInput.Attachments[0].Name = "changed"
			if normalized.OriginalInput == nil || normalized.OriginalInput.Text != archiveOriginalText || normalized.OriginalInput.Attachments[0].Name != "current.pdf" {
				t.Fatalf("snapshot not isolated: %+v", normalized.OriginalInput)
			}
		})
	}
}

func TestOriginalInputSurvivesCurrentFileIngestion(t *testing.T) {
	event := Event{EventID: "E1", UserID: "U1", Text: "original", ChannelType: "im", Files: []FileRef{{ID: "F1", Name: "current.txt"}}}
	chat := &chatHandlerRecorder{result: chatapp.Result{Settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}}}
	processor := NewInboundProcessor(chat, processorLifecycleStub{}, nil, &currentFileIngestorStub{attachments: []attachment.Descriptor{{Kind: attachment.KindDocument, FileName: "downloaded.txt", Blob: &attachment.BlobRef{Path: "/local/private"}}}}, nil)
	_, err := processor.ProcessInbound(context.Background(), IngressEnvelope{Files: event.Files, Chat: NormalizeChatRequest(NewThreadLocator("T1", "D1", "1.1"), event, time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.requests) != 1 || chat.requests[0].OriginalInput == nil {
		t.Fatal("original input missing")
	}
	if got := chat.requests[0].OriginalInput.Attachments; len(got) != 1 || got[0].Name != "current.txt" {
		t.Fatalf("current input = %+v", got)
	}
}

type archiveHistoryReader struct{ snapshot ThreadSnapshot }

func (r archiveHistoryReader) ReadThreadBefore(context.Context, string, string, string) (ThreadSnapshot, error) {
	return r.snapshot, nil
}

func TestOriginalInputExcludesHydratedHistoricalTextAndAttachments(t *testing.T) {
	now := time.Now()
	env, err := BuildIngressEnvelope(EventEnvelope{Type: "event_callback", Event: Event{EventType: "app_mention", EventID: "E1", UserID: "U1", Text: archiveOriginalText, ChannelType: "channel", Conversation: ConversationRef{TeamID: "T1", ConversationID: "C1", ThreadID: "100.1"}, Message: &MessageRef{MessageID: "100.3", ThreadTS: "100.1"}, Files: []FileRef{{ID: "CURRENT", Name: "current.txt", MIMEType: "text/plain"}}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	chat := &chatHandlerRecorder{result: chatapp.Result{Settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}}}
	snapshot := historicalSnapshot(ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "historical human context", files: []FileRef{{ID: "HISTORY", Name: "history.txt", MIMEType: "text/plain", SizeBytes: 3}}})
	hydrator := NewHistoricalContextHydrator(&fileClientStub{bodies: map[string]string{"HISTORY": "old"}}, &blobStoreStub{enabled: true}, attachment.Limits{MaxFilesPerMessage: 3, MaxFileBytes: 10, MaxTotalBytes: 20}, zerolog.Nop())
	processor := NewInboundProcessor(chat, processorLifecycleStub{}, archiveHistoryReader{snapshot: snapshot}, &currentFileIngestorStub{attachments: []attachment.Descriptor{{Kind: attachment.KindDocument, FileID: "CURRENT", FileName: "current.txt", SizeBytes: 3, Blob: &attachment.BlobRef{Path: "/state/current"}}}}, hydrator)
	if _, err := processor.ProcessInbound(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if len(chat.requests) != 1 {
		t.Fatalf("chat requests = %d", len(chat.requests))
	}
	got := chat.requests[0]
	if !strings.Contains(got.Text, "historical human context") || len(got.Attachments) != 2 {
		t.Fatalf("normal model context missing: %+v", got)
	}
	if got.OriginalInput == nil || got.OriginalInput.Text != archiveOriginalText || len(got.OriginalInput.Attachments) != 1 || got.OriginalInput.Attachments[0].Name != "current.txt" {
		t.Fatalf("original input = %+v", got.OriginalInput)
	}
	if got.UserID == got.OriginalInput.Speaker.Principal || got.OriginalInput.Speaker.Principal != "U1" {
		t.Fatalf("requester/provider speaker = %q/%+v", got.UserID, got.OriginalInput.Speaker)
	}
}

func TestOriginalInputSlackChannelRootIsBoundToThread(t *testing.T) {
	got := NormalizeChatRequest(NewThreadLocator("T1", "C1", "100.1"), Event{EventID: "E1", EventType: "app_mention", UserID: "U1", Text: "@balda hello", Message: &MessageRef{MessageID: "100.1"}}, time.Now())
	if got.OriginalInput.Location.Audience != "group" || got.OriginalInput.Location.Subdivision != "thread" {
		t.Fatalf("bound location = %+v", got.OriginalInput.Location)
	}
}

func TestOriginalInputSlackWireTextPreservesWhitespaceAlongsideNormalizedEvent(t *testing.T) {
	body := []byte(`{"type":"event_callback","event_id":"E1","team_id":"T1","event":{"type":"message","channel":"D1","channel_type":"im","user":"U1","text":"  human text  ","ts":"100.1"}}`)
	event, err := DecodeEventEnvelope(body)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event.Text != "human text" {
		t.Fatalf("normalized event text = %q", event.Event.Text)
	}
	envelope, err := BuildIngressEnvelope(event, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Chat.Text != "human text" || envelope.Chat.OriginalInput == nil || envelope.Chat.OriginalInput.Text != "  human text  " {
		t.Fatalf("prompt/original = %q/%+v", envelope.Chat.Text, envelope.Chat.OriginalInput)
	}
}
