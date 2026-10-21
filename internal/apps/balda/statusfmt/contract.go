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
	ProviderID        string
	ProviderType      string
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
}

// ResponseDescriptor identifies the versioned structured status response.
var ResponseDescriptor = deliveryfmt.Descriptor[Response]{
	Type: "balda.status.response.v1",
}
