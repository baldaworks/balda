package mattermost

import (
	"context"
	"github.com/rs/zerolog"
	"testing"
)

func TestOriginalInputMattermostPreservesSenderLocationAndCurrentFileCount(t *testing.T) {
	for _, test := range []struct{ name, kind, root, audience, subdivision string }{
		{"direct root", "D", "", "personal", "root"},
		{"group root", "G", "", "group", "root"},
		{"direct reply", "D", "root1", "personal", "root"},
		{"channel root starts thread", "O", "", "group", "thread"},
		{"channel reply", "P", "root1", "group", "thread"},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &recordingProcessor{}
			ingress := &Ingress{processor: processor, botUsername: "balda", logger: zerolog.Nop()}
			post := Post{ID: "post1", UserID: "U2", ChannelID: "C1", RootID: test.root, Message: "  @balda original  ", FileIDs: []string{"file1", "file2"}, CreateAt: 1000}
			ingress.processPosted(context.Background(), PostedData{ChannelType: test.kind, SenderName: "Alice", ChannelDisplayName: "Known channel", TeamID: "T1"}, post)
			messages := processor.inboundMessages()
			if len(messages) != 1 {
				t.Fatalf("messages = %d", len(messages))
			}
			original := NormalizeInboundMessage(messages[0], messages[0].ReceivedAt).OriginalInput
			if original == nil || original.Text != post.Message || original.Source != "mattermost:post1" || original.Speaker.Principal != "U2" || original.Speaker.Name != "Alice" {
				t.Fatalf("original = %+v", original)
			}
			if original.Location.Audience != test.audience || original.Location.Subdivision != test.subdivision || original.Location.Title != "Known channel" {
				t.Fatalf("location = %+v", original.Location)
			}
			if len(original.Attachments) != 2 || original.Attachments[0].Kind != "file" || original.Attachments[0].Name != "" {
				t.Fatalf("attachment metadata = %+v", original.Attachments)
			}
		})
	}
}
