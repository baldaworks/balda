package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// RenderTurnFailure preserves the failed-final text in the transport's literal carrier.
func RenderTurnFailure(body deliveryfmt.TurnFailure) string {
	// Slack still decodes control entities in plain messages. Replace once so
	// literal source entities are preserved after the display decoding pass.
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body.Text)
}
