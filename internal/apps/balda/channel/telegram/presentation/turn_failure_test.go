package presentation

import (
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
			if got != source {
				t.Fatalf("literal text = %q, want %q", got, source)
			}
		})
	}
}
