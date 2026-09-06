package presentation

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

func TestRenderLocator(t *testing.T) {
	t.Parallel()

	body := locatorfmt.Response{Transport: "zulip", Locator: "zulip:s:42:deploys"}
	want := "## Balda locator\n\n**Transport:** `zulip`\n**Locator:** `zulip:s:42:deploys`\n\n**Scheduler / webhook configuration**\n```yaml\ntarget: locator\nkey: zulip:s:42:deploys\n```"
	if got := RenderLocator(body); got != want {
		t.Fatalf("RenderLocator() = %q, want %q", got, want)
	}
	if got := RenderLocator(body); got != want {
		t.Fatalf("second RenderLocator() = %q, want deterministic output", got)
	}
}
