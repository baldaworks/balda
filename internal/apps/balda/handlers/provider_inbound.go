package handlers

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func normalizeTelegramInbound(message TelegramMessageContext, text string, receivedAt time.Time) turncmd.NormalizedInbound {
	options := deliveryfmt.NormalizeOptions(message.DeliveryOptions)
	providerMessageID := strconv.Itoa(message.MessageID)
	inboundID := fmt.Sprintf("telegram:%d:%d:message:%d", message.ChatID, message.TopicID, message.MessageID)
	if mediaGroupID := strings.TrimSpace(message.MediaGroupID); mediaGroupID != "" {
		providerMessageID = mediaGroupID
		inboundID = fmt.Sprintf(
			"telegram:%d:%d:user:%d:media-group:%s",
			message.ChatID,
			message.TopicID,
			message.UserID,
			mediaGroupID,
		)
	}
	return turncmd.NormalizedInbound{
		ID:                turncmd.InboundID(inboundID),
		Text:              text,
		Attachments:       attachment.NormalizeList(message.Attachments),
		Locator:           message.Locator,
		ProviderMessageID: providerMessageID,
		UserID:            telegramref.UserID(message.UserID),
		MessageID:         message.MessageID,
		ReplyToMessageID:  message.ReplyToMessageID,
		TopicID:           message.TopicID,
		ReceivedAt:        receivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    options.DeliveryFormat,
		ProgressPolicy:    options.ProgressPolicy,
		Direct:            message.IsDM,
		Source:            turncmd.SourceTelegram,
	}
}

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
		logicalID = turncmd.InboundID("slack:" + providerMessageID)
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
		Source:            turncmd.SourceSlack,
	}
}

type zulipInboundMessage struct {
	Locator    deliverycmd.Locator
	MessageID  int
	UserID     string
	Text       string
	Direct     bool
	ReceivedAt time.Time
}

func normalizeZulipInbound(message zulipInboundMessage) turncmd.NormalizedInbound {
	providerMessageID := ""
	if message.MessageID > 0 {
		providerMessageID = strconv.Itoa(message.MessageID)
	}
	logicalID := turncmd.InboundID("")
	if providerMessageID != "" {
		logicalID = turncmd.InboundID("zulip:" + providerMessageID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              strings.TrimSpace(message.Text),
		Locator:           message.Locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(message.UserID),
		MessageID:         message.MessageID,
		ReceivedAt:        message.ReceivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMarkdown,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{Typing: true, PlanUpdates: true},
		Direct:            message.Direct,
		Source:            turncmd.SourceZulip,
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
	return fmt.Sprintf("slack:%s:%s", strings.TrimSpace(teamID), strings.TrimSpace(userID))
}

func zulipUserID(userID int) string {
	return fmt.Sprintf("zu-%d", userID)
}

func parseZulipUserID(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "zu-") {
		return 0, fmt.Errorf("zulip user id %q must start with %q", value, "zu-")
	}
	userID, err := strconv.Atoi(strings.TrimPrefix(trimmed, "zu-"))
	if err != nil || userID <= 0 {
		return 0, fmt.Errorf("parse zulip user id %q", value)
	}
	return userID, nil
}

func slackDMLocator(teamID, channelID string) deliverycmd.Locator {
	locator, _ := locatorref.NewSlackDMLocator(teamID, channelID)
	return locator
}

func slackThreadLocator(teamID, channelID, threadTS string) deliverycmd.Locator {
	locator, _ := locatorref.NewSlackThreadLocator(teamID, channelID, threadTS)
	return locator
}

func zulipStreamLocator(streamID int, topic string) deliverycmd.Locator {
	locator, _ := locatorref.NewZulipStreamLocator(streamID, topic)
	return locator
}

func zulipDMLocator(userID int) deliverycmd.Locator {
	locator, _ := locatorref.NewZulipDMLocator(userID)
	return locator
}
