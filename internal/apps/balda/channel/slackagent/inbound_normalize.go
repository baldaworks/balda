package slackagent

import (
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func NormalizeInbound(locator deliverycmd.Locator, event Event, receivedAt time.Time) turncmd.NormalizedInbound {
	return BuildChatRequest(locator, event, receivedAt).NormalizedInbound()
}

func BuildChatRequest(locator deliverycmd.Locator, event Event, receivedAt time.Time) chatapp.Request {
	eventID := strings.TrimSpace(event.EventID)
	providerMessageID := event.ProviderMessageID()
	replyToMessageID := event.ReplyToMessageID()
	if eventID == "" {
		eventID = providerMessageID
	}
	if providerMessageID == "" {
		providerMessageID = eventID
	}
	logicalID := turncmd.InboundID("")
	if eventID != "" {
		logicalID = turncmd.InboundID("slackagent:" + eventID)
	}
	return chatapp.Request{
		ID:                logicalID,
		Text:              strings.TrimSpace(event.Text),
		Locator:           locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(event.UserID),
		MessageID:         providerNumericMessageID(providerMessageID),
		ReplyToMessageID:  providerNumericMessageID(replyToMessageID),
		ReceivedAt:        receivedAt,
		DeliveryOptions: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
			ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: true, PlanUpdates: true},
		},
		Source:            turncmd.SourceSlackAgent,
	}
}

func providerNumericMessageID(value string) int {
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
