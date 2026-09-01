package slackagent

import (
	"bytes"
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
	Type               string          `json:"type"`
	User               string          `json:"user"`
	Text               string          `json:"text"`
	Channel            string          `json:"channel"`
	TS                 string          `json:"ts"`
	ThreadTS           string          `json:"thread_ts"`
	EventTS            string          `json:"event_ts"`
	ChannelType        string          `json:"channel_type"`
	Subtype            string          `json:"subtype"`
	BotID              string          `json:"bot_id"`
	BotProfile         json.RawMessage `json:"bot_profile"`
	StreamingMessageTS []string        `json:"streaming_message_ts"`
}

func DecodeEventEnvelope(body []byte) (EventEnvelope, error) {
	var raw rawEnvelope
	if err := json.Unmarshal(body, &raw); err != nil {
		return EventEnvelope{}, err
	}
	return normalizeRawEnvelope(raw), nil
}

func normalizeRawEnvelope(raw rawEnvelope) EventEnvelope {
	teamID := strings.TrimSpace(raw.TeamID)
	rootTS := firstNonEmpty(raw.Event.ThreadTS, raw.Event.TS)
	conversation := ConversationRef{
		TeamID:         strings.TrimSpace(teamID),
		ConversationID: strings.TrimSpace(raw.Event.Channel),
		ThreadID:       strings.TrimSpace(rootTS),
	}
	messageID := strings.TrimSpace(raw.Event.TS)
	dedupeKey := firstNonEmpty(strings.TrimSpace(raw.EventID), messageID, strings.TrimSpace(raw.Event.EventTS))
	if teamID != "" && conversation.ConversationID != "" && messageID != "" {
		dedupeKey = "message:" + teamID + ":" + conversation.ConversationID + ":" + messageID
	}
	replyToMessageID := strings.TrimSpace(raw.Event.ThreadTS)
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
			EventID:            strings.TrimSpace(raw.EventID),
			EventType:          strings.TrimSpace(raw.Event.Type),
			UserID:             strings.TrimSpace(raw.Event.User),
			Text:               strings.TrimSpace(raw.Event.Text),
			ChannelType:        strings.TrimSpace(raw.Event.ChannelType),
			Subtype:            strings.TrimSpace(raw.Event.Subtype),
			BotID:              strings.TrimSpace(raw.Event.BotID),
			HasBotProfile:      hasJSONObject(raw.Event.BotProfile),
			StreamingMessageTS: trimNonEmpty(raw.Event.StreamingMessageTS),
			DedupeKey:          dedupeKey,
			Conversation:       conversation,
			Message:            message,
		},
	}
}

func hasJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func trimNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
