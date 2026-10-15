package telegram

import (
	"strings"
	"testing"

	"github.com/tgbotkit/client"
)

func TestInvitationExcludedFromQuotedContext(t *testing.T) {
	secret := "bind_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	message := MessageContext{Text: "continue", IsReply: true, IsForwarded: true, ReplyContent: "/start " + secret, ForwardedContent: secret}
	if got := NormalizeDMText(message); got != "continue" {
		t.Fatalf("safe quoted DM = %q", got)
	}
	message.IsReply, message.ReplyToIsBot, message.ReplyToUserID = true, true, 1001
	if got, ok := NormalizePublicText(message, 1001, "bot"); !ok || got != "continue" {
		t.Fatalf("safe quoted public input = %q, %t", got, ok)
	}
}

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
