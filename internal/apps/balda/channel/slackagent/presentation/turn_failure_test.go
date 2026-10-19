package presentation

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestRenderTurnFailurePreservesLiteralContent(t *testing.T) {
	t.Parallel()
	// Lossy Markdown conversion, missing escaping, or fixed fences would change these texts.
	for _, source := range []string{
		"Provider error:\n  Модель <busy> & retry **later** [link](https://example.com)\nnext line",
		"Provider error:\n`inline`\n```\n~~~\n````\nend",
		"Provider error:\n&amp; &lt; &gt; <@synthetic> <https://example.com|label>",
		"Provider error:\n  indented  text \t\n\nlast line\n",
	} {
		t.Run(source, func(t *testing.T) {
			got := RenderTurnFailure(deliveryfmt.TurnFailure{Text: source})
			// Slack decodes these three entities once, including those in plain messages.
			display := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(got)
			if display != source {
				t.Fatalf("displayed text = %q, want %q (wire %q)", display, source, got)
			}
			if strings.Contains(got, "<") || strings.Contains(got, ">") {
				t.Fatalf("unescaped Slack control string: %q", got)
			}
		})
	}
}
