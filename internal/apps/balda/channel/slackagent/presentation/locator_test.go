package presentation

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

func TestRenderLocator(t *testing.T) {
	t.Parallel()

	body := locatorfmt.Response{Transport: "slackagent", Locator: "slackagent:c:T123:C456"}
	want := "*Balda locator*\n\n*Transport:* `slackagent`\n*Locator:* `slackagent:c:T123:C456`\n\n*Scheduler / webhook configuration*\n```\ntarget: locator\nkey: slackagent:c:T123:C456\n```"
	if got := RenderLocator(body); got != want {
		t.Fatalf("RenderLocator() = %q, want %q", got, want)
	}
	if got := RenderLocator(body); got != want {
		t.Fatalf("second RenderLocator() = %q, want deterministic output", got)
	}
}
