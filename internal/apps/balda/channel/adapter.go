package channel

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

type OperationKind string

const (
	OperationPlain      OperationKind = "plain"
	OperationMarkdown   OperationKind = "markdown"
	OperationAgentReply OperationKind = "agent_reply"
	OperationDraft      OperationKind = "draft"
	OperationTyping     OperationKind = "typing"
	OperationProgress   OperationKind = "progress"
)

// Operation describes one transport-neutral delivery side effect.
type Operation struct {
	Kind     OperationKind
	Profile  deliverycmd.Profile
	Text     string
	DraftID  int
	Progress deliverycmd.Progress
}

// Result contains transport metadata returned by a delivery.
type Result struct {
	ProviderMessageID string
}

// ChannelAdapter executes one semantic delivery operation.
type ChannelAdapter interface {
	Deliver(ctx context.Context, locator baldasession.SessionLocator, operation Operation) (Result, error)
}
