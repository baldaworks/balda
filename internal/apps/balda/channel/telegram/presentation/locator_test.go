package presentation

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

func TestRenderLocator(t *testing.T) {
	t.Parallel()

	body := locatorfmt.Response{Transport: "telegram", Locator: "telegram:-1001:77"}
	want := "📍 **Balda Locator** • **Transport:** `telegram` • **Locator:** `telegram:-1001:77`\n\n**Scheduler / webhook configuration**\n```yaml\ntarget: locator\nkey: telegram:-1001:77\n```"
	if got := RenderLocator(body); got != want {
		t.Fatalf("RenderLocator() = %q, want %q", got, want)
	}
	if got := RenderLocator(body); got != want {
		t.Fatalf("second RenderLocator() = %q, want deterministic output", got)
	}
}
