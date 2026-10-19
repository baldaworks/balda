package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// RenderTurnFailure preserves the failed-final text in the transport's literal carrier.
func RenderTurnFailure(body deliveryfmt.TurnFailure) string {
	// A longer fence keeps provider-supplied code fences inside the literal block.
	width, run := 3, 0
	for _, char := range body.Text {
		if char != '`' {
			run = 0
			continue
		}
		run++
		if run >= width {
			width = run + 1
		}
	}
	fence := strings.Repeat("`", width)
	return fence + "\n" + body.Text + "\n" + fence
}
