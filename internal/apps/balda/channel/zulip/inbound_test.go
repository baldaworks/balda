package zulip

import (
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
)

func TestNormalizeInboundPreservesZulipIdentityAndCapability(t *testing.T) {
	t.Parallel()

	got := NormalizeInbound(InboundMessage{
		Locator:    NewDMLocator(101),
		MessageID:  42,
		UserID:     UserID(101),
		Text:       " hello ",
		Direct:     true,
		ReceivedAt: time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC),
	})

	if got.ID != "zulip:42" || got.ProviderMessageID != "42" {
		t.Fatalf("identity = %+v, want stable Zulip message identity", got)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatMarkdown || !got.ProgressPolicy.Typing || !got.ProgressPolicy.PlanUpdates {
		t.Fatalf("capability = %+v, want Zulip Markdown and progress", got)
	}
	if !got.Direct {
		t.Fatalf("session policy = %+v, want direct", got)
	}
	if parsed, err := ParseUserID(got.UserID); err != nil || parsed != 101 {
		t.Fatalf("ParseUserID(%q) = %d, %v; want 101", got.UserID, parsed, err)
	}
}
