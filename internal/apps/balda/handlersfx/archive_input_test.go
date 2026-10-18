package handlersfx

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
)

func TestOriginalInputMattermostDoesNotInferPersonalFromDirectFlag(t *testing.T) {
	got := mattermostChatRequest(mattermost.InboundMessage{Locator: mattermost.NewGroupDMLocator("G1"), PostID: "post-1", SenderID: "U2", SenderName: "Alice", Text: " original ", Direct: true}, time.Now())
	if got.OriginalInput == nil {
		t.Fatal("original input missing")
	}
	if got.OriginalInput.Text != " original " || got.OriginalInput.Speaker.Principal != "U2" || got.OriginalInput.Speaker.Name != "Alice" || got.OriginalInput.Location.Audience != "group" || got.OriginalInput.Location.Subdivision != "root" {
		t.Fatalf("original = %+v", got.OriginalInput)
	}
}
