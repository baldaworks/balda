package telegram

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
)

const archiveSpeakerName = "Alice"

func TestOriginalInputPreservesHumanTextBeforeQuoteAndAttachmentEnrichment(t *testing.T) {
	for _, test := range []struct {
		name, text            string
		dm                    bool
		topic                 int
		audience, subdivision string
		chatType              string
	}{
		{"private topic", "  original caption  ", true, 9, "personal", "topic", "private"},
		{"group", "@balda hello", false, 0, "group", "root", "supergroup"},
		{"unknown", "hello", false, 0, "unknown", "root", ""},
		{"attachment only", "", true, 0, "personal", "root", "private"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
			message := MessageContext{Locator: NewLocator(11, test.topic), ChatID: 11, ChatType: test.chatType, ChatTitle: "Known title", TopicID: test.topic, MessageID: 12, UserID: 42, Username: "alice", FirstName: archiveSpeakerName, IsDM: test.dm, Text: test.text, ReplyContent: "historic quote", Attachments: []attachment.Descriptor{{Kind: attachment.KindDocument, FileName: "report.txt", Blob: &attachment.BlobRef{Path: "/secret/report.txt"}}}}
			got := NormalizeInbound(message, AppendAttachmentSummary(NormalizeDMText(message), message.Attachments), now)
			original := got.OriginalInput
			if original == nil {
				t.Fatal("original human input missing")
			}
			if original.Text != test.text || original.Source != string(got.ID) || !original.ReceivedAt.Equal(now) {
				t.Fatalf("original = %+v", original)
			}
			if original.Speaker.Name != archiveSpeakerName || original.Speaker.Username != "alice" || original.Speaker.Principal != got.UserID {
				t.Fatalf("speaker = %+v", original.Speaker)
			}
			if original.Location.Audience != test.audience || original.Location.Subdivision != test.subdivision || original.Location.Locator == "" {
				t.Fatalf("location = %+v", original.Location)
			}
			if len(original.Attachments) != 1 || original.Attachments[0].Name != "report.txt" {
				t.Fatalf("attachments = %+v", original.Attachments)
			}
			message.Attachments[0].FileName = "renamed.txt"
			if original.Attachments[0].Name != "report.txt" {
				t.Fatal("snapshot changed with provider data")
			}
		})
	}
}
