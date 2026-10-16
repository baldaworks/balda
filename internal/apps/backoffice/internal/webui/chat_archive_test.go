package webui

import (
	"net/url"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

func TestChatArchivePreservesInventoryWindow(t *testing.T) {
	const inventoryCursor = "older-inventory"
	query := chatarchivecmd.BrowseQuery{Query: "Planning", Channel: "telegram", Status: "ended", Cursor: inventoryCursor}
	summary := chatarchivecmd.Summary{Session: chatarchivecmd.Session{ID: "70f4bc27-399a-40f0-bdeb-9023f82bd2b0"}}
	inventory := ProjectChatInventory(chatarchivecmd.Inventory{Rows: []chatarchivecmd.Summary{summary}, Older: "next-inventory"}, query)
	detailURL, err := url.Parse(inventory.Rows[0].DetailPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := detailURL.Query().Get("list_cursor"); got != inventoryCursor {
		t.Fatalf("detail list cursor=%q", got)
	}
	query.Cursor = ""
	query.ListCursor = detailURL.Query().Get("list_cursor")
	detail := ProjectChatTranscript(chatarchivecmd.Transcript{Summary: summary, Older: "older-messages"}, query)
	backURL, err := url.Parse(detail.BackPath)
	if err != nil {
		t.Fatal(err)
	}
	olderURL, err := url.Parse(detail.OlderPath)
	if err != nil {
		t.Fatal(err)
	}
	if backURL.Query().Get("cursor") != inventoryCursor || backURL.Query().Get("q") != "Planning" || olderURL.Query().Get("cursor") != "older-messages" || olderURL.Query().Get("list_cursor") != inventoryCursor {
		t.Fatalf("back=%s older=%s", detail.BackPath, detail.OlderPath)
	}
}

func TestChatArchiveReadableLabels(t *testing.T) {
	view := ProjectChatInventory(chatarchivecmd.Inventory{Rows: []chatarchivecmd.Summary{{Session: chatarchivecmd.Session{ID: "synthetic", Status: chatarchivecmd.StatusActive, Location: chatarchivecmd.Location{Channel: "slackagent"}}}}}, chatarchivecmd.BrowseQuery{})
	if view.Rows[0].Channel != "Slack" || view.Rows[0].Status != "Active" {
		t.Fatalf("channel/status = %q/%q, want Slack/Active", view.Rows[0].Channel, view.Rows[0].Status)
	}
}
