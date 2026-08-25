package slackagent

import (
	"encoding/json"
	"strings"
)

// EventEnvelope is the normalized slackagent webhook contract used by ingress.
type EventEnvelope struct {
	Type      string `json:"type,omitempty"`
	Challenge string `json:"challenge,omitempty"`
	Event     Event  `json:"event"`
}

type rawEnvelope struct {
	Type      string   `json:"type"`
	Challenge string   `json:"challenge"`
	EventID   string   `json:"event_id"`
	TeamID    string   `json:"team_id"`
	Event     rawEvent `json:"event"`
}

type rawEvent struct {
	Type             string `json:"type"`
	UserID           string `json:"user_id"`
	Text             string `json:"text"`
	ConversationID   string `json:"conversation_id"`
	ThreadID         string `json:"thread_id"`
	MessageID        string `json:"message_id"`
	ReplyToMessageID string `json:"reply_to_message_id"`
	TeamID           string `json:"team_id"`
}

func DecodeEventEnvelope(body []byte) (EventEnvelope, error) {
	var raw rawEnvelope
	if err := json.Unmarshal(body, &raw); err != nil {
		return EventEnvelope{}, err
	}
	return normalizeRawEnvelope(raw), nil
}

func normalizeRawEnvelope(raw rawEnvelope) EventEnvelope {
	teamID := firstNonEmpty(raw.Event.TeamID, raw.TeamID)
	conversation := ConversationRef{
		TeamID:         strings.TrimSpace(teamID),
		ConversationID: strings.TrimSpace(raw.Event.ConversationID),
		ThreadID:       strings.TrimSpace(raw.Event.ThreadID),
	}
	messageID := strings.TrimSpace(raw.Event.MessageID)
	replyToMessageID := strings.TrimSpace(raw.Event.ReplyToMessageID)
	var message *MessageRef
	if messageID != "" || replyToMessageID != "" {
		message = &MessageRef{
			Conversation: conversation,
			MessageID:    messageID,
			ThreadTS:     replyToMessageID,
		}
	}
	return EventEnvelope{
		Type:      strings.TrimSpace(raw.Type),
		Challenge: raw.Challenge,
		Event: Event{
			EventID:      strings.TrimSpace(raw.EventID),
			EventType:    strings.TrimSpace(raw.Event.Type),
			UserID:       strings.TrimSpace(raw.Event.UserID),
			Text:         strings.TrimSpace(raw.Event.Text),
			DedupeKey:    firstNonEmpty(strings.TrimSpace(raw.EventID), messageID),
			Conversation: conversation,
			Message:      message,
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
