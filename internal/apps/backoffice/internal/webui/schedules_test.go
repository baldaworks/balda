package webui

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
)

func TestScheduleViewsShowConfiguredAliasReference(t *testing.T) {
	const alias = "main_chat"
	item := schedulecmd.Item{Definition: schedulecmd.Definition{
		ID: "daily", Cron: "0 9 * * *", Alias: alias, Content: "review",
	}, Source: "managed", Enabled: true}
	row := ProjectScheduleRow(item)
	if row.Locator != alias {
		t.Fatalf("inventory report recipient = %q", row.Locator)
	}
	editor := ProjectScheduleEditor(item, false)
	if editor.ReportTo != alias {
		t.Fatalf("editor report recipient = %+v", editor)
	}
}
