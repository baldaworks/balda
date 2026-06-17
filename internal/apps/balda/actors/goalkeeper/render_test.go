package goalkeeper

import (
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
)

func TestRenderGoalStartedMessagePlainMatchesLegacyText(t *testing.T) {
	t.Parallel()

	got := renderGoalStartedMessage(deliverycmd.Profile{}, 25, "count total go lines")
	want := "Goal run started. Max iterations: 25.\n\nObjective: count total go lines"
	if got != want {
		t.Fatalf("renderGoalStartedMessage() = %q, want %q", got, want)
	}
}

func TestRenderGoalStepMessageMarkdownFormatsHeaderAndPreservesBody(t *testing.T) {
	t.Parallel()

	body := "worker update\n---\n![bad](http://invalid/image.png)"
	got := renderGoalStepMessage(
		deliverycmd.Profile{FormattingMode: "rich_markdown"},
		1,
		25,
		"worker",
		"update",
		body,
	)
	wantPrefix := "**Goal iteration 1/25:** worker update."
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("renderGoalStepMessage() = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.Contains(got, "\n\n"+body) {
		t.Fatalf("renderGoalStepMessage() = %q, want unchanged body %q", got, body)
	}
}

func TestRenderGoalStartedMessageHTMLEscapesSystemFields(t *testing.T) {
	t.Parallel()

	got := renderGoalStartedMessage(
		deliverycmd.Profile{FormattingMode: "rich_html"},
		3,
		"ship <release> & verify",
	)
	for _, want := range []string{
		"<b>Goal run started</b>",
		"<b>Objective:</b> ship &lt;release&gt; &amp; verify",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderGoalStartedMessage() = %q, want %q", got, want)
		}
	}
}

func TestRenderGoalStepMessageHTMLPreservesBody(t *testing.T) {
	t.Parallel()

	body := "<b>validator</b>\n---\nplain"
	got := renderGoalStepMessage(
		deliverycmd.Profile{FormattingMode: "html"},
		2,
		5,
		"validator",
		"completed",
		body,
	)
	if !strings.HasPrefix(got, "<b>Goal iteration 2/5:</b> validator completed.") {
		t.Fatalf("renderGoalStepMessage() = %q, want HTML header", got)
	}
	if !strings.Contains(got, "\n\n"+body) {
		t.Fatalf("renderGoalStepMessage() = %q, want unchanged body %q", got, body)
	}
}

func TestRenderGoalStatusMessageUnknownModeFallsBackToPlain(t *testing.T) {
	t.Parallel()

	got := renderGoalStatusMessage(deliverycmd.Profile{FormattingMode: "unknown"}, "Goal run canceled.")
	if got != "Goal run canceled." {
		t.Fatalf("renderGoalStatusMessage() = %q, want plain fallback", got)
	}
}
