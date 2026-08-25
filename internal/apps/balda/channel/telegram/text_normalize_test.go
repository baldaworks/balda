package telegram

import (
	"strings"
	"testing"

	"github.com/tgbotkit/client"
)

func TestBotMentionEntityRanges_SupportsUTF16Offsets(t *testing.T) {
	text := "hi 😀 @testbot now"
	ranges := botMentionEntityRanges(text, []client.MessageEntity{
		{Type: "mention", Offset: 6, Length: len("@testbot")},
	}, "testbot")

	if len(ranges) != 1 {
		t.Fatalf("ranges len = %d, want 1", len(ranges))
	}
	if got := strings.TrimSpace(removeTextByUTF16Ranges(text, ranges)); got != "hi 😀  now" {
		t.Fatalf("text after mention removal = %q, want %q", got, "hi 😀  now")
	}
}
