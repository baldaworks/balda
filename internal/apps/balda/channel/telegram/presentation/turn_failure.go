package presentation

import (
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// RenderTurnFailure preserves the failed-final text in the transport's literal carrier.
func RenderTurnFailure(body deliveryfmt.TurnFailure) string {
	return body.Text
}
