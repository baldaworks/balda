package slack

import (
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
)

func TestNormalizeInboundPreservesSlackIdentityAndCapability(t *testing.T) {
	t.Parallel()

	got := NormalizeInbound(InboundMessage{
		Locator:           NewDMLocator("T123", "D456"),
		ProviderMessageID: " 1712345678.1234 ",
		UserID:            UserID("T123", "U789"),
		Text:              "hello",
		Direct:            true,
		ReceivedAt:        time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC),
	})

	if got.ID != "slack:1712345678.1234" || got.ProviderMessageID != "1712345678.1234" {
		t.Fatalf("identity = %+v, want stable Slack message identity", got)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatMrkdwn || !got.ProgressPolicy.PlanUpdates {
		t.Fatalf("capability = %+v, want Slack mrkdwn", got)
	}
	if !got.Direct {
		t.Fatalf("session policy = %+v, want direct", got)
	}
}
