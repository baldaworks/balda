package zulip

import (
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
)

// InboundMessage is one verified Zulip webhook message.
type InboundMessage struct {
	Locator    deliverycmd.Locator
	MessageID  int
	UserID     string
	Text       string
	Direct     bool
	ReceivedAt time.Time
}

// NormalizeInbound maps one Zulip webhook message to the shared ingress contract.
func NormalizeInbound(message InboundMessage) turncmd.NormalizedInbound {
	providerMessageID := ""
	if message.MessageID > 0 {
		providerMessageID = fmt.Sprintf("%d", message.MessageID)
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
