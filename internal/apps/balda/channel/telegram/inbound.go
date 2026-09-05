package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func NormalizeInbound(message MessageContext, text string, receivedAt time.Time) turncmd.NormalizedInbound {
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
		Text:              strings.TrimSpace(text),
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
