package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

type slackInboundMessage struct {
	Locator           deliverycmd.Locator
	ProviderMessageID string
	UserID            string
	Text              string
	Direct            bool
	ReceivedAt        time.Time
}

func normalizeSlackInbound(message slackInboundMessage) turncmd.NormalizedInbound {
	providerMessageID := strings.TrimSpace(message.ProviderMessageID)
	logicalID := turncmd.InboundID("")
	if providerMessageID != "" {
		logicalID = turncmd.InboundID("slackagent:" + providerMessageID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              message.Text,
		Locator:           message.Locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(message.UserID),
		MessageID:         providerNumericMessageID(providerMessageID),
		ReceivedAt:        message.ReceivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMrkdwn,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{PlanUpdates: true},
		Direct:            message.Direct,
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

func slackUserID(teamID, userID string) string {
	return fmt.Sprintf("slackagent:%s:%s", strings.TrimSpace(teamID), strings.TrimSpace(userID))
}

func slackDMLocator(teamID, channelID string) deliverycmd.Locator {
	locator, _ := locatorref.NewSlackAgentConversationLocator(teamID, channelID)
	return locator
}
