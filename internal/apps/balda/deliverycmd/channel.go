package deliverycmd

import "context"

// ChannelType identifies the transport backing a delivery locator.
type ChannelType string

const (
	ChannelTypeTelegram   ChannelType = "telegram"
	ChannelTypeZulip      ChannelType = "zulip"
	ChannelTypeSlackChat  ChannelType = "slack"
	ChannelTypeSlackAgent ChannelType = "slack_agent"
)

type OperationKind string

const (
	OperationPlain                 OperationKind = "plain"
	OperationMarkdown              OperationKind = "markdown"
	OperationAgentReply            OperationKind = "agent_reply"
	OperationDraft                 OperationKind = "draft"
	OperationTyping                OperationKind = "typing"
	OperationProgress              OperationKind = "progress"
	OperationClearQuestionControls OperationKind = "clear_question_controls"
	OperationPhoto                 OperationKind = "photo"
	OperationDocument              OperationKind = "document"
)

// Operation describes one transport-neutral delivery side effect.
type Operation struct {
	Kind      OperationKind
	Profile   Profile
	Text      string
	DraftID   int
	Progress  Progress
	Question  *Question
	MessageID string
	Handle    string
	Media     *Media
}

type Media struct {
	Kind    string `json:"kind,omitempty"`
	FileID  string `json:"file_id,omitempty"`
	Caption string `json:"caption,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Result contains transport metadata returned by a delivery.
type Result struct {
	ProviderMessageID string
}

// Adapter executes one semantic delivery operation.
type Adapter interface {
	Deliver(ctx context.Context, locator Locator, operation Operation) (Result, error)
}
