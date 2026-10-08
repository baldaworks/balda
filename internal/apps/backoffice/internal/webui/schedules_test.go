package webui

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
)

func TestScheduleViewsShowConfiguredAliasReference(t *testing.T) {
	item := schedulecmd.Item{Definition: schedulecmd.Definition{
		ID: "daily", Cron: "0 9 * * *", Alias: "main_chat", Content: "review",
	}, Source: "managed", Enabled: true}
	row := ProjectScheduleRow(item)
	if row.Locator != "Alias · main_chat" {
		t.Fatalf("inventory destination = %q", row.Locator)
	}
	editor := ProjectScheduleEditor(item, false)
	if editor.Alias != "main_chat" || editor.ReportKind != "managed_alias" {
		t.Fatalf("editor destination = %+v", editor)
	}
}
