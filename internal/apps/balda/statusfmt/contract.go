// Package statusfmt defines the transport-neutral structured contract for
// presenting Balda runtime status.
package statusfmt

import (
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// Response contains the semantic values shown by a successful status command.
// Transport renderers own all labels and presentation markup.
type Response struct {
	ConversationState string
	WaitingKind       string
	WaitingSince      time.Time
	ObservedAt        time.Time
	ProviderID        string
	ProviderType      string
	RequestedModel    string
	HasRequestedModel bool
	ActiveModel       string
	ActiveModelKind   string
	// Model is retained for v1/v2 status delivery compatibility. New producers
	// use ActiveModel and never project configured or recipe defaults here.
	Model             string
	ReasoningEffort   string
	ChannelType       string
	SessionID         string
	SessionState      string
	TurnActive        bool
	TurnStartedAt     time.Time
	PendingTurns      int
	UserText          string
	UserTextTruncated bool
	TurnState         string
	LastActivityAt    time.Time
	ActivityKind      string
	TurnCompletedAt   time.Time
	TurnOutcome       string
	RequestExcerpt    string
	RequestTruncated  bool
	DegradedSources   []string
}

// ResponseDescriptor identifies the versioned structured status response.
var ResponseDescriptor = deliveryfmt.Descriptor[Response]{
	Type: "balda.status.response.v3",
}

// LegacyV2ResponseDescriptor keeps the v2 renderer registration available.
var LegacyV2ResponseDescriptor = deliveryfmt.Descriptor[Response]{Type: "balda.status.response.v2"}

// LegacyResponseDescriptor keeps the v1 renderer registration available.
var LegacyResponseDescriptor = deliveryfmt.Descriptor[Response]{Type: "balda.status.response.v1"}
