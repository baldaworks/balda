package slack

import (
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
)

// InboundMessage is the provider-specific Slack chat input after Events API grouping.
type InboundMessage struct {
	Locator           deliverycmd.Locator
	ProviderMessageID string
	UserID            string
	Text              string
	Direct            bool
	ReceivedAt        time.Time
}

// NormalizeInbound maps one Slack chat event to the shared ingress contract.
func NormalizeInbound(message InboundMessage) turncmd.NormalizedInbound {
	providerMessageID := strings.TrimSpace(message.ProviderMessageID)
	logicalID := turncmd.InboundID("")
	if providerMessageID != "" {
		logicalID = turncmd.InboundID("slack:" + providerMessageID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              message.Text,
		Locator:           message.Locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(message.UserID),
		MessageID:         messageID(providerMessageID),
		ReceivedAt:        message.ReceivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMrkdwn,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{PlanUpdates: true},
		Direct:            message.Direct,
		Source:            turncmd.SourceSlack,
	}
}

func messageID(value string) int {
	var out int
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			continue
		}
		out = out*10 + int(r-'0')
		if out > 1_000_000_000 {
			return out
		}
	}
	return out
}
