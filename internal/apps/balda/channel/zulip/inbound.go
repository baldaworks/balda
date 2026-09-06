package zulip

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// InboundMessage represents a normalized inbound message from Zulip.
type InboundMessage struct {
	Locator     deliverycmd.Locator
	MessageID   int
	SenderID    int
	SenderEmail string
	Text        string
	Direct      bool
	ReceivedAt  time.Time
}

// InboundCommand represents a command invocation from Zulip.
type InboundCommand struct {
	Locator   deliverycmd.Locator
	MessageID int
	SenderID  int
	Command   string
	Args      string
	Direct    bool
}

// InboundProcessor processes inbound Zulip messages and commands.
type InboundProcessor interface {
	ProcessInbound(ctx context.Context, msg InboundMessage) (turncmd.InboundSettlement, error)
	HandleCommand(ctx context.Context, cmd InboundCommand) error
	HandleUnsupportedCommand(ctx context.Context, cmd InboundCommand) error
}

var supportedCommands = []string{"start", "topic", "locator", "cancel", "goalkeeper", "user", "usage", "auto", "reset", "close"}

func SupportedCommands() []string { return append([]string(nil), supportedCommands...) }
func commandSupported(name string) bool {
	for _, supported := range supportedCommands {
		if name == supported {
			return true
		}
	}
	return false
}

const (
	messageTypeStream  = "stream"
	messageTypePrivate = "private"
	triggerMention     = "mention"
)

// WebhookPayload is the Zulip webhook contract used by ingress.
type WebhookPayload struct {
	BotEmail string         `json:"bot_email"`
	Data     string         `json:"data"`
	Trigger  string         `json:"trigger"`
	Token    string         `json:"token"`
	Message  WebhookMessage `json:"message"`
}

// WebhookMessage is the provider message object inside a Zulip webhook payload.
type WebhookMessage struct {
	ID          int    `json:"id"`
	SenderID    int    `json:"sender_id"`
	SenderEmail string `json:"sender_email"`
	Type        string `json:"type"`
	StreamID    int    `json:"stream_id"`
	Subject     string `json:"subject"`
	Content     string `json:"content"`
}

// ValidateWebhookPayload validates the transport-owned Zulip webhook shape.
func ValidateWebhookPayload(payload WebhookPayload) error {
	if payload.Message.SenderID <= 0 {
		return fmt.Errorf("message.sender_id is required")
	}
	if strings.TrimSpace(payload.Message.SenderEmail) == "" {
		return fmt.Errorf("message.sender_email is required")
	}
	switch strings.TrimSpace(payload.Message.Type) {
	case messageTypeStream:
		if payload.Message.StreamID <= 0 {
			return fmt.Errorf("message.stream_id is required for stream messages")
		}
	case messageTypePrivate:
	default:
		return fmt.Errorf("unsupported message.type %q", payload.Message.Type)
	}
	return nil
}

// VerifyWebhookToken reports whether the payload token matches the configured secret.
func VerifyWebhookToken(payload WebhookPayload, token string) bool {
	return subtle.ConstantTimeCompare([]byte(payload.Token), []byte(strings.TrimSpace(token))) == 1
}

// IsBotEcho reports whether the payload was sent by the configured Zulip bot itself.
func IsBotEcho(payload WebhookPayload) bool {
	botEmail := strings.TrimSpace(payload.BotEmail)
	if botEmail == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Message.SenderEmail), botEmail)
}

// NormalizeMessageText returns the logical inbound text after transport cleanup.
func NormalizeMessageText(payload WebhookPayload) string {
	text := firstNonEmptyText(payload.Data, payload.Message.Content)
	if strings.TrimSpace(payload.Trigger) != triggerMention {
		return text
	}
	return stripLeadingMentions(text)
}

// LocatorFromWebhookPayload builds the canonical Balda locator for one inbound payload.
func LocatorFromWebhookPayload(payload WebhookPayload) deliverycmd.Locator {
	if payload.Message.Type == messageTypePrivate {
		return NewDMLocator(payload.Message.SenderID)
	}
	return NewStreamLocator(payload.Message.StreamID, payload.Message.Subject)
}

// NormalizeInbound converts a Zulip message into the transport-neutral inbound contract.
func NormalizeInbound(locator deliverycmd.Locator, messageID int, userID, text string, direct bool, receivedAt time.Time) turncmd.NormalizedInbound {
	providerMessageID := ""
	if messageID > 0 {
		providerMessageID = strconv.Itoa(messageID)
	}
	logicalID := turncmd.InboundID("")
	if providerMessageID != "" {
		logicalID = turncmd.InboundID("zulip:" + providerMessageID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              strings.TrimSpace(text),
		Locator:           locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(userID),
		MessageID:         messageID,
		ReceivedAt:        receivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMarkdown,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{Typing: true, PlanUpdates: true},
		Direct:            direct,
		Source:            turncmd.SourceZulip,
	}
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text != "" {
			return text
		}
	}
	return ""
}

func stripLeadingMentions(text string) string {
	trimmed := strings.TrimSpace(text)
	for {
		next, ok := trimLeadingMention(trimmed)
		if !ok {
			return trimmed
		}
		trimmed = strings.TrimSpace(next)
	}
}

func trimLeadingMention(text string) (string, bool) {
	for _, prefix := range []string{"@**", "@_**"} {
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		rest := text[len(prefix):]
		end := strings.Index(rest, "**")
		if end < 0 {
			return text, false
		}
		return rest[end+len("**"):], true
	}
	return text, false
}
