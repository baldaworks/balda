package slackagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

type IngressEnvelope struct {
	Type            string
	Challenge       string
	Locator         deliverycmd.Locator
	Subject         string
	InitiatorUserID string
	// RequireExistingSession prevents ambient channel messages from creating
	// Balda sessions. Explicit app mentions remain the only channel entrypoint.
	RequireExistingSession bool
	Inbound                turncmd.NormalizedInbound
	Reply                  questioncmd.InboundReply
	HasReply               bool
	Stopped                *SessionStopped
	IgnoreEvent            bool
}

type SessionStopped struct {
	Locator            deliverycmd.Locator
	RequestedBy        string
	StreamingMessageTS []string
}

func DecodeIngressEnvelope(body []byte, receivedAt time.Time) (IngressEnvelope, error) {
	env, err := DecodeEventEnvelope(body)
	if err != nil {
		return IngressEnvelope{}, err
	}
	return BuildIngressEnvelope(env, receivedAt)
}

func BuildIngressEnvelope(env EventEnvelope, receivedAt time.Time) (IngressEnvelope, error) {
	out := IngressEnvelope{
		Type:      strings.TrimSpace(env.Type),
		Challenge: env.Challenge,
	}
	if out.Type == "url_verification" {
		if strings.TrimSpace(out.Challenge) == "" {
			return IngressEnvelope{}, fmt.Errorf("slack url_verification challenge is required")
		}
		return out, nil
	}
	if out.Type != "event_callback" {
		out.IgnoreEvent = true
		return out, nil
	}
	event := env.Event
	switch strings.TrimSpace(event.EventType) {
	case "message":
		switch {
		case eligibleHumanIM(event):
		case eligibleHumanChannelThreadMessage(event):
			out.RequireExistingSession = true
		default:
			out.IgnoreEvent = true
			return out, nil
		}
	case "app_mention":
		if !eligibleHumanChannelMention(event) {
			out.IgnoreEvent = true
			return out, nil
		}
	case "agent_session_stopped":
		if err := validateThreadEvent(event); err != nil {
			return IngressEnvelope{}, err
		}
		out.Locator = locatorForConversation(event.Conversation)
		out.Subject = slackUserID(event.Conversation.TeamID, event.UserID)
		out.InitiatorUserID = strings.TrimSpace(event.UserID)
		out.Stopped = &SessionStopped{
			Locator:            out.Locator,
			RequestedBy:        out.Subject,
			StreamingMessageTS: append([]string(nil), event.StreamingMessageTS...),
		}
		return out, nil
	default:
		out.IgnoreEvent = true
		return out, nil
	}
	out.Locator = locatorForConversation(event.Conversation)
	out.Subject = slackUserID(event.Conversation.TeamID, event.UserID)
	out.InitiatorUserID = strings.TrimSpace(event.UserID)
	out.Inbound = NormalizeInbound(out.Locator, event, receivedAt)
	out.Inbound.UserID = out.Subject
	if reply, ok := BuildInboundReply(out.Locator, out.Subject, event, receivedAt); ok {
		out.Reply = reply
		out.HasReply = true
	}
	return out, nil
}

func eligibleHumanIM(event Event) bool {
	if strings.TrimSpace(event.ChannelType) != "im" || !eligibleHumanMessage(event) {
		return false
	}
	return true
}

func eligibleHumanChannelMention(event Event) bool {
	return isChannelConversation(event.Conversation.ConversationID) && eligibleHumanMessage(event)
}

func eligibleHumanChannelThreadMessage(event Event) bool {
	if strings.TrimSpace(event.ReplyToMessageID()) == "" || !isChannelConversation(event.Conversation.ConversationID) {
		return false
	}
	return eligibleHumanMessage(event)
}

func eligibleHumanMessage(event Event) bool {
	if strings.TrimSpace(event.Subtype) != "" || strings.TrimSpace(event.BotID) != "" || event.HasBotProfile || strings.TrimSpace(event.Text) == "" {
		return false
	}
	return validateThreadEvent(event) == nil
}

func isChannelConversation(conversationID string) bool {
	scope, err := classifyConversation(conversationID)
	return err == nil && scope == deliverycmd.LocatorScopeGroup
}

func validateThreadEvent(event Event) error {
	if strings.TrimSpace(event.Conversation.TeamID) == "" {
		return fmt.Errorf("slack event team_id is required")
	}
	if strings.TrimSpace(event.Conversation.ConversationID) == "" {
		return fmt.Errorf("slack event channel is required")
	}
	if strings.TrimSpace(event.Conversation.ThreadID) == "" {
		return fmt.Errorf("slack event thread timestamp is required")
	}
	if strings.TrimSpace(event.UserID) == "" {
		return fmt.Errorf("slack event user is required")
	}
	return nil
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
	return NewThreadLocator(conversation.TeamID, conversation.ConversationID, conversation.ThreadID)
}

func slackUserID(teamID, userID string) string {
	return "slackagent:" + strings.TrimSpace(teamID) + ":" + strings.TrimSpace(userID)
}
