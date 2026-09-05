package chatapp

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// Handler owns transport-neutral conversational ingress semantics.
type Handler interface {
	HandleChat(ctx context.Context, request Request) (Result, error)
}

// Result reports whether an inbound item was settled and activated a turn.
type Result struct {
	Settlement turncmd.InboundSettlement
	Activated  bool
}

// Request is the transport-neutral chat ingress contract.
// Concrete transports map local inbound events into this request before
// invoking shared conversational ingress semantics.
type Request struct {
	ID                turncmd.InboundID
	Text              string
	Attachments       []attachment.Descriptor
	Locator           deliverycmd.Locator
	ProviderMessageID string
	UserID            string
	MessageID         int
	ReplyToMessageID  int
	TopicID           int
	ReceivedAt        time.Time
	DeliveryOptions   deliveryfmt.Options
	QuestionReply     *questioncmd.InboundReply
	Direct            bool
	Source            string
}

func (r Request) NormalizedInbound() turncmd.NormalizedInbound {
	options := deliveryfmt.NormalizeOptions(r.DeliveryOptions)
	return turncmd.NormalizedInbound{
		ID:                r.ID,
		Text:              strings.TrimSpace(r.Text),
		Attachments:       attachment.NormalizeList(r.Attachments),
		Locator:           r.Locator,
		ProviderMessageID: strings.TrimSpace(r.ProviderMessageID),
		UserID:            strings.TrimSpace(r.UserID),
		MessageID:         r.MessageID,
		ReplyToMessageID:  r.ReplyToMessageID,
		TopicID:           r.TopicID,
		ReceivedAt:        r.ReceivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    options.DeliveryFormat,
		ProgressPolicy:    options.ProgressPolicy,
		Direct:            r.Direct,
		Source:            strings.TrimSpace(r.Source),
	}
}
