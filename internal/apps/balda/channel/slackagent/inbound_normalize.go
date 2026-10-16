package slackagent

import (
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

const eventTypeAppMention = "app_mention"

func NormalizeChatRequest(locator deliverycmd.Locator, event Event, receivedAt time.Time) chatapp.Request {
	eventID := strings.TrimSpace(event.EventID)
	providerMessageID := event.ProviderMessageID()
	replyToMessageID := event.ReplyToMessageID()
	if eventID == "" {
		eventID = providerMessageID
	}
	if providerMessageID == "" {
		providerMessageID = eventID
	}
	stableID := strings.TrimSpace(event.DedupeKey)
	if stableID == "" {
		stableID = eventID
	}
	logicalID := turncmd.InboundID("")
	if stableID != "" {
		logicalID = turncmd.InboundID("slackagent:" + stableID)
	}
	options := deliveryfmt.NormalizeOptions(deliveryfmt.Options{
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
		ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: true, PlanUpdates: true},
	})
	return chatapp.Request{
		OriginalInput:     originalSlackInput(locator, event, string(logicalID), receivedAt),
		ID:                logicalID,
		Text:              strings.TrimSpace(event.Text),
		Locator:           locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(event.UserID),
		MessageID:         providerNumericMessageID(providerMessageID),
		ReplyToMessageID:  providerNumericMessageID(replyToMessageID),
		ReceivedAt:        receivedAt.UTC(),
		DeliveryOptions:   options,
		Direct:            strings.TrimSpace(event.ChannelType) == "im",
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

func originalSlackInput(locator deliverycmd.Locator, event Event, source string, receivedAt time.Time) *chatarchivecmd.Input {
	audience := chatarchivecmd.AudienceUnknown
	switch event.ChannelType {
	case "im":
		audience = chatarchivecmd.AudiencePersonal
	case "mpim", "group", "channel":
		audience = chatarchivecmd.AudienceGroup
	}
	if audience == chatarchivecmd.AudienceUnknown && event.EventType == eventTypeAppMention {
		// Accepted mentions originate in channels even when channel_type is absent.
		audience = chatarchivecmd.AudienceGroup
	}
	subdivision := chatarchivecmd.SubdivisionUnknown
	if address, ok, err := DecodeLocator(locator); ok && err == nil {
		subdivision = chatarchivecmd.SubdivisionRoot
		if address.ThreadID != "" {
			subdivision = chatarchivecmd.SubdivisionThread
		}
	}
	text := event.Text
	if event.OriginalText != nil {
		text = *event.OriginalText
	}
	input := &chatarchivecmd.Input{Source: source, Text: text, ReceivedAt: receivedAt.UTC(),
		Speaker:  chatarchivecmd.Speaker{Principal: strings.TrimSpace(event.UserID)},
		Location: chatarchivecmd.Location{Channel: locator.ChannelType, AddressKey: locator.AddressKey, Locator: locatorref.Format(locator), Audience: audience, Subdivision: subdivision},
	}
	for _, file := range event.Files {
		input.Attachments = append(input.Attachments, chatarchivecmd.AttachmentLabel(file.Name, file.MIMEType))
	}
	return input
}
