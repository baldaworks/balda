package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
)

func RenderProgress(body progressfmt.Request) string {
	return strings.TrimSpace(body.Progress.Text)
}
