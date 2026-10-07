package webui

import (
	"net/url"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
)

// SchedulesView is the administrator inventory or one schedule editor.
type SchedulesView struct {
	Rows   []ScheduleRow
	Editor *ScheduleEditor
}

// ScheduleRow is a content-free inventory projection.
type ScheduleRow struct {
	ID, DetailPath, Source, Cron, Target, ReportTo, Status, NextRun, LastRun string
	Enabled, ReadOnly, Deleted                                               bool
	Version                                                                  uint64
}

// ScheduleEditor contains the guarded definition detail for one schedule.
type ScheduleEditor struct {
	Row                       ScheduleRow
	New                       bool
	ID, Cron, Content         string
	TargetKind, TargetKey     string
	ReportToKind, ReportToKey string
	Action                    string
}

// ProjectScheduleRow omits instruction content from the inventory.
func ProjectScheduleRow(item schedulecmd.Item) ScheduleRow {
	d := item.Definition
	row := ScheduleRow{ID: d.ID, DetailPath: "/schedules/" + url.PathEscape(d.ID), Cron: d.Cron,
		Target: scheduleTarget(d.Target), Status: item.Status, NextRun: scheduleTime(item.NextRunAt),
		LastRun: scheduleTime(item.LastRunAt), Enabled: item.Enabled, Deleted: item.Deleted,
		ReadOnly: item.Source != "managed", Version: item.Version, Source: "Backoffice"}
	if row.ReadOnly {
		row.Source = "Configuration"
	}
	if d.ReportTo != nil {
		row.ReportTo = scheduleTarget(*d.ReportTo)
	}
	return row
}

// ProjectScheduleEditor retains content only on a guarded detail page.
func ProjectScheduleEditor(item schedulecmd.Item, create bool) *ScheduleEditor {
	d := item.Definition
	e := &ScheduleEditor{Row: ProjectScheduleRow(item), New: create, ID: d.ID, Cron: d.Cron,
		Content: d.Content, TargetKind: d.Target.Kind, TargetKey: d.Target.Key,
		Action: "/schedules/" + url.PathEscape(d.ID)}
	if d.ReportTo != nil {
		e.ReportToKind, e.ReportToKey = d.ReportTo.Kind, d.ReportTo.Key
	}
	if create {
		e.Action = "/schedules"
		e.Row.ReadOnly = false
		e.TargetKind = "alias"
		e.TargetKey = "owner"
	}
	return e
}

func scheduleTarget(target schedulecmd.Target) string {
	if target.Kind == "" {
		return ""
	}
	return target.Kind + ": " + target.Key
}

func scheduleTime(at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	return at.UTC().Format("2006-01-02 15:04 UTC")
}
