package zulip

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog"
	"testing"
	"time"
)

func TestOriginalInputUsesZulipSenderAndIndependentLocation(t *testing.T) {
	got := NormalizeInbound(NewStreamLocator(7, "Design"), 12, "zulip-42", " original ", false, time.Now())
	if got.OriginalInput == nil {
		t.Fatal("original input missing")
	}
	if got.OriginalInput.Text != " original " || got.OriginalInput.Speaker.Principal != "zulip-42" || got.OriginalInput.Location.Audience != "group" || got.OriginalInput.Location.Subdivision != "topic" || got.OriginalInput.Location.SubdivisionLabel != "Design" {
		t.Fatalf("original = %+v", got.OriginalInput)
	}
}

func TestOriginalInputZulipWebhookMetadataSurvivesMentionCleanup(t *testing.T) {
	for _, test := range []struct{ name, kind, recipients, audience, subdivision, title string }{
		{"stream", "stream", `"Engineering"`, "group", "topic", "Engineering"},
		{"private pair", "private", `[{"id":1},{"id":2}]`, "personal", "root", ""},
		{"private group", "private", `[{"id":1},{"id":2},{"id":3}]`, "group", "root", ""},
		{"unknown private audience", "private", `null`, "unknown", "root", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &mockInboundProcessor{}
			server := &Server{processor: processor, logger: zerolog.Nop()}
			payload := WebhookPayload{Data: "@**Balda** hello", Trigger: "mention", Message: WebhookMessage{ID: 12, SenderID: 42, SenderEmail: "alice@example.test", SenderFullName: "Alice", Type: test.kind, StreamID: 7, Subject: "Design", Content: "@**Balda** hello", DisplayRecipient: json.RawMessage(test.recipients)}}
			if _, err := server.processMessage(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if len(processor.inboundCalls) != 1 {
				t.Fatalf("inbound calls = %d", len(processor.inboundCalls))
			}
			normalized := NormalizeInboundMessage(processor.inboundCalls[0], time.Now())
			original := normalized.OriginalInput
			if normalized.Text != "hello" || original == nil || original.Text != "@**Balda** hello" {
				t.Fatalf("original/normalized = %+v/%q", original, normalized.Text)
			}
			if original.Speaker.Name != "Alice" || original.Speaker.Username != "alice@example.test" || original.Speaker.Principal != UserID(42) {
				t.Fatalf("speaker = %+v", original.Speaker)
			}
			if original.Location.Audience != test.audience || original.Location.Subdivision != test.subdivision || original.Location.Title != test.title {
				t.Fatalf("location = %+v", original.Location)
			}
		})
	}
}

func TestOriginalInputZulipUsesRawDataWhenMessageContentIsRendered(t *testing.T) {
	payload := WebhookPayload{Data: "  **human text**  ", Message: WebhookMessage{ID: 1, SenderID: 2, Type: "private", Content: "<p><strong>human text</strong></p>"}}
	original := originalInputFromWebhook(payload)
	if original.Text != payload.Data {
		t.Fatalf("original text = %q, want raw input %q", original.Text, payload.Data)
	}
}
