package presentation

import (
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

// RenderLocator renders a validated locator response as Telegram Rich Markdown.
func RenderLocator(body locatorfmt.Response) string {
	return fmt.Sprintf(
		"# Balda locator\n\n**Transport:** `%s`\n**Locator:** `%s`\n\n**Scheduler / webhook configuration**\n```yaml\ntarget: locator\nkey: %s\n```",
		strings.TrimSpace(body.Transport),
		strings.TrimSpace(body.Locator),
		strings.TrimSpace(body.Locator),
	)
}
