package presentation

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
)

func RenderQuestion(body questionfmt.Request) string {
	return strings.TrimSpace(body.Prompt)
}
