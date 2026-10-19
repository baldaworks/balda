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
			opening, rest, ok := strings.Cut(got, "\n")
			if !ok || len(opening) < 3 || strings.Trim(opening, "`") != "" {
				t.Fatalf("invalid literal fence: %q", got)
			}
			body, ok := strings.CutSuffix(rest, "\n"+opening)
			if !ok {
				t.Fatalf("missing closing fence: %q", got)
			}
			if body != source {
				t.Fatalf("literal-region body = %q, want %q", body, source)
			}
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(strings.TrimLeft(line, " "), opening) {
					t.Fatalf("content can close the literal region: %q", line)
				}
			}
		})
	}
}
