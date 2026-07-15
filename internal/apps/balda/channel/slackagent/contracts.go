package slackagent

import "context"

// ConversationRef is the stable identity for a Slack AI Agents conversation.
type ConversationRef struct {
	TeamID         string `json:"team_id,omitempty"`
	EnterpriseID   string `json:"enterprise_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
}

// MessageRef identifies a provider-visible Slack agent message inside a conversation.
type MessageRef struct {
	Conversation ConversationRef `json:"conversation"`
	MessageID    string          `json:"message_id,omitempty"`
	ThreadTS     string          `json:"thread_ts,omitempty"`
}

// Event is the normalized inbound contract for slack_agent ingress.
type Event struct {
	EventID      string          `json:"event_id,omitempty"`
	EventType    string          `json:"event_type,omitempty"`
	UserID       string          `json:"user_id,omitempty"`
	Text         string          `json:"text,omitempty"`
	DedupeKey    string          `json:"dedupe_key,omitempty"`
	Conversation ConversationRef `json:"conversation"`
	Message      *MessageRef     `json:"message,omitempty"`
}

// Capabilities snapshots the enabled slack_agent affordances after config normalization.
type Capabilities struct {
	Enabled          bool `json:"enabled"`
	Status           bool `json:"status"`
	Questions        bool `json:"questions"`
	Wait             bool `json:"wait"`
	Streaming        bool `json:"streaming"`
	SuggestedPrompts bool `json:"suggested_prompts"`
}

// Responder owns slack_agent-specific UX signals and final reply delivery.
type Responder interface {
	SetThinking(ctx context.Context, conversation ConversationRef, active bool) error
	DeliverFinal(ctx context.Context, conversation ConversationRef, message *MessageRef, text string) error
}
