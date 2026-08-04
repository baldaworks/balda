package slackagent

import (
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
)

// InboundMessage is one verified Slack Agent event.
type InboundMessage struct {
	Locator          deliverycmd.Locator
	EventID          string
	MessageID        string
	ReplyToMessageID string
	UserID           string
	Text             string
	ReceivedAt       time.Time
}

// NormalizeInbound maps one Slack Agent event to the shared ingress contract.
func NormalizeInbound(message InboundMessage) turncmd.NormalizedInbound {
	eventID := strings.TrimSpace(message.EventID)
	providerMessageID := strings.TrimSpace(message.MessageID)
	if eventID == "" {
		eventID = providerMessageID
	}
	if providerMessageID == "" {
		providerMessageID = eventID
	}
	logicalID := turncmd.InboundID("")
	if eventID != "" {
		logicalID = turncmd.InboundID("slack_agent:" + eventID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              strings.TrimSpace(message.Text),
		Locator:           message.Locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(message.UserID),
		MessageID:         messageID(providerMessageID),
		ReplyToMessageID:  messageID(message.ReplyToMessageID),
		ReceivedAt:        message.ReceivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMrkdwn,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{Typing: true, Thinking: true, PlanUpdates: true},
		Source:            turncmd.SourceSlackAgent,
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
