package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
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
	original := message.OriginalInput.Clone()
	if original == nil {
		original = originalInput(message)
	}
	original.Source = inboundID
	original.ReceivedAt = receivedAt.UTC()
	return turncmd.NormalizedInbound{
		OriginalInput:     original,
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

func AppendAttachmentSummary(text string, attachments []attachment.Descriptor) string {
	attachments = attachment.NormalizeList(attachments)
	if len(attachments) == 0 {
		return text
	}
	var b strings.Builder
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString("Attachment manifest:\n")
	for i, item := range attachments {
		b.WriteString("- attachment_")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(":\n")
		b.WriteString("  kind: ")
		b.WriteString(string(item.Kind))
		b.WriteString("\n")
		if item.FileName != "" {
			b.WriteString("  file_name: ")
			b.WriteString(item.FileName)
			b.WriteString("\n")
		}
		if item.MIMEType != "" {
			b.WriteString("  mime_type: ")
			b.WriteString(item.MIMEType)
			b.WriteString("\n")
		}
		if item.SizeBytes > 0 {
			b.WriteString("  size_bytes: ")
			b.WriteString(strconv.FormatInt(item.SizeBytes, 10))
			b.WriteString("\n")
		}
		if item.Caption != "" {
			b.WriteString("  caption: ")
			b.WriteString(item.Caption)
			b.WriteString("\n")
		}
		if item.Blob != nil {
			if item.Blob.Store != "" {
				b.WriteString("  blob_store: ")
				b.WriteString(item.Blob.Store)
				b.WriteString("\n")
			}
			if item.Blob.Path != "" {
				b.WriteString("  local_path: ")
				b.WriteString(item.Blob.Path)
				b.WriteString("\n")
			}
			if item.Blob.Key != "" {
				b.WriteString("  blob_key: ")
				b.WriteString(item.Blob.Key)
				b.WriteString("\n")
			}
			if item.Blob.SHA256 != "" {
				b.WriteString("  sha256: ")
				b.WriteString(item.Blob.SHA256)
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// originalInput snapshots only current transport content, before prompt context.
func originalInput(message MessageContext) *chatarchivecmd.Input {
	audience := chatarchivecmd.AudienceUnknown
	switch message.ChatType {
	case "private":
		audience = chatarchivecmd.AudiencePersonal
	case "group", "supergroup":
		audience = chatarchivecmd.AudienceGroup
	default:
		if message.IsDM {
			audience = chatarchivecmd.AudiencePersonal
		}
	}
	subdivision := chatarchivecmd.SubdivisionRoot
	if message.TopicID != 0 {
		subdivision = chatarchivecmd.SubdivisionTopic
	}
	input := &chatarchivecmd.Input{
		Text:     message.Text,
		Speaker:  chatarchivecmd.Speaker{Principal: telegramref.UserID(message.UserID), Name: message.FirstName, Username: message.Username},
		Location: chatarchivecmd.Location{Channel: message.Locator.ChannelType, AddressKey: message.Locator.AddressKey, Locator: locatorref.Format(message.Locator), Title: message.ChatTitle, Audience: audience, Subdivision: subdivision},
	}
	for _, item := range message.Attachments {
		input.Attachments = append(input.Attachments, chatarchivecmd.AttachmentLabel(item.FileName, string(item.Kind)))
	}
	return input
}

func telegramChatTitle(title *string) string {
	if title == nil {
		return ""
	}
	return *title
}
