package webui

import (
	"strconv"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
)

// FactMemoryView presents the global fact inventory or one fact editor.
type FactMemoryView struct {
	Enabled    bool
	Query      string
	TotalFacts int
	Rows       []FactRow
	Editor     *FactEditor
}

// FactRow retains complete fact text and its immutable edit address.
type FactRow struct {
	ID         int64
	Fact       string
	DetailPath string
}

// FactEditor binds submitted fact text to its loaded optimistic version.
type FactEditor struct {
	ID              int64
	Fact            string
	ExpectedVersion int64
	New             bool
	Action          string
	ReopenPath      string
}

// ProjectFactInventory preserves source order and complete multiline facts.
func ProjectFactInventory(inv memorycmd.Inventory, query string) *FactMemoryView {
	view := &FactMemoryView{Enabled: inv.Enabled, Query: query, TotalFacts: inv.TotalFacts}
	for _, fact := range inv.Facts {
		view.Rows = append(view.Rows, FactRow{ID: fact.ID, Fact: fact.Fact, DetailPath: factDetailPath(fact.ID)})
	}
	return view
}

// ProjectFactEditor keeps the global version separate from the stable fact ID.
func ProjectFactEditor(ed memorycmd.Editor, create bool) *FactEditor {
	editor := &FactEditor{ID: ed.Fact.ID, Fact: ed.Fact.Fact, ExpectedVersion: ed.ExpectedVersion, New: create, Action: factDetailPath(ed.Fact.ID), ReopenPath: factDetailPath(ed.Fact.ID)}
	if create {
		editor.Action = "/memory"
		editor.ReopenPath = "/memory?new=1"
	}
	return editor
}

func factDetailPath(id int64) string { return "/memory/" + strconv.FormatInt(id, 10) }
