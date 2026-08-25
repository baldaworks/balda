package slackagent

import (
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

type IngressEnvelope struct {
	Type        string
	Challenge   string
	Locator     deliverycmd.Locator
	Subject     string
	Inbound     turncmd.NormalizedInbound
	Reply       questioncmd.InboundReply
	HasReply    bool
	IgnoreEvent bool
}

func DecodeIngressEnvelope(body []byte, receivedAt time.Time) (IngressEnvelope, error) {
	env, err := DecodeEventEnvelope(body)
	if err != nil {
		return IngressEnvelope{}, err
	}
	return BuildIngressEnvelope(env, receivedAt), nil
}

func BuildIngressEnvelope(env EventEnvelope, receivedAt time.Time) IngressEnvelope {
	out := IngressEnvelope{
		Type:      strings.TrimSpace(env.Type),
		Challenge: env.Challenge,
	}
	event := env.Event
	if strings.TrimSpace(event.Text) == "" || strings.TrimSpace(event.UserID) == "" {
		out.IgnoreEvent = true
		return out
	}
	out.Locator = locatorForConversation(event.Conversation)
	out.Subject = slackUserID(event.Conversation.TeamID, event.UserID)
	out.Inbound = NormalizeInbound(out.Locator, event, receivedAt)
	out.Inbound.UserID = out.Subject
	if reply, ok := BuildInboundReply(out.Locator, out.Subject, event, receivedAt); ok {
		out.Reply = reply
		out.HasReply = true
	}
	return out
}

func BuildInboundReply(locator deliverycmd.Locator, subject string, event Event, receivedAt time.Time) (questioncmd.InboundReply, bool) {
	replyToMessageID := event.ReplyToMessageID()
	messageID := event.ProviderMessageID()
	text := strings.TrimSpace(event.Text)
	if replyToMessageID == "" || text == "" {
		return questioncmd.InboundReply{}, false
	}
	return questioncmd.InboundReply{
		Provider:         ChannelType,
		SessionID:        locator.SessionID,
		ConversationKey:  locator.AddressKey,
		ReplyToMessageID: replyToMessageID,
		MessageID:        messageID,
		User:             questioncmd.UserRef{UserID: subject},
		Text:             text,
		ReceivedAt:       receivedAt.UTC(),
	}, true
}

func locatorForConversation(conversation ConversationRef) deliverycmd.Locator {
	if strings.TrimSpace(conversation.ThreadID) != "" {
		return NewThreadLocator(conversation.TeamID, conversation.ConversationID, conversation.ThreadID)
	}
	return NewConversationLocator(conversation.TeamID, conversation.ConversationID)
}

func slackUserID(teamID, userID string) string {
	return "slackagent:" + strings.TrimSpace(teamID) + ":" + strings.TrimSpace(userID)
}
