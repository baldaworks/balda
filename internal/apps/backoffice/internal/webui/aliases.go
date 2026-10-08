package webui

import (
	"net/url"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
)

// AliasesView is the administrator inventory or one mapping editor.
type AliasesView struct {
	Rows   []AliasRow
	Editor *AliasEditor
}

// AliasRow keeps the managed name distinct from its concrete locator.
type AliasRow struct {
	Name       string
	LocatorRef string
	DetailPath string
	Version    uint64
}

// AliasEditor contains one guarded mapping revision.
type AliasEditor struct {
	Row    AliasRow
	New    bool
	Action string
}

func ProjectAliasRow(record aliascmd.Record) AliasRow {
	return AliasRow{Name: record.Name, LocatorRef: record.LocatorRef,
		DetailPath: "/aliases/" + url.PathEscape(record.Name), Version: record.Version}
}

func ProjectAliasEditor(record aliascmd.Record, create bool) *AliasEditor {
	editor := &AliasEditor{Row: ProjectAliasRow(record), New: create,
		Action: "/aliases/" + url.PathEscape(record.Name)}
	if create {
		editor.Action = "/aliases"
	}
	return editor
}
