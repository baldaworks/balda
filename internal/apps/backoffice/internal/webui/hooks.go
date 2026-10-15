package webui

import (
	"encoding/json"
	"strconv"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

// HooksView contains only administrator-authorized configuration projections.
type HooksView struct {
	Rows   []HookRow
	Editor *HookEditor
	Events []hookcmd.EventName
}

// HookRow is a content-free inventory link to one executable definition.
type HookRow struct {
	Name       string
	Event      hookcmd.EventName
	Provider   string
	Enabled    bool
	DetailPath string
}

// HookEditor preserves submitted configuration and its optimistic version.
type HookEditor struct {
	Definition      hookcmd.Definition
	ExpectedVersion int64
	New             bool
	Action          string
	ReopenPath      string
	ArgsJSON        string
	TimeoutValue    string
}

// ProjectHooks exposes names and applicability without command arguments in inventory.
func ProjectHooks(record hookcmd.Record) *HooksView {
	view := &HooksView{Events: hookcmd.Events()}
	for _, d := range record.Definitions {
		view.Rows = append(view.Rows, HookRow{Name: d.Name, Event: d.Event, Provider: d.Provider, Enabled: d.Enabled, DetailPath: "/hooks/" + d.Name})
	}
	return view
}

// ProjectHookEditor retains the complete argv without shell parsing.
func ProjectHookEditor(d hookcmd.Definition, version int64, create bool) *HookEditor {
	args := d.Args
	if args == nil {
		args = []string{}
	}
	encoded, _ := json.Marshal(args)
	editor := &HookEditor{Definition: d, ExpectedVersion: version, New: create, Action: "/hooks/" + d.Name, ReopenPath: "/hooks/" + d.Name, ArgsJSON: string(encoded), TimeoutValue: strconv.Itoa(d.TimeoutSeconds)}
	if create {
		editor.Action, editor.ReopenPath = "/hooks", "/hooks?new=1"
	}
	return editor
}
