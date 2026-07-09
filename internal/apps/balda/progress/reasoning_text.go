package progress

import (
	"strings"

	adksession "google.golang.org/adk/v2/session"
)

// ReasoningText returns thought-part text and whether the event carried any thought parts.
func ReasoningText(ev *adksession.Event) (string, bool) {
	if ev == nil || ev.Content == nil {
		return "", false
	}
	var parts []string
	hasThought := false
	for _, part := range ev.Content.Parts {
		if part == nil || !part.Thought {
			continue
		}
		hasThought = true
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), hasThought
}
