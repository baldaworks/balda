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
	ID, DetailPath, Source, Cron, Locator, Status, NextRun, LastRun string
	Enabled, ReadOnly, Deleted                                      bool
	Version                                                         uint64
}

// ScheduleEditor contains the guarded definition detail for one schedule.
type ScheduleEditor struct {
	Row                            ScheduleRow
	New                            bool
	ID, Cron, Content, ReportTo    string
	Action                         string
	RunRequestKey, NextHistoryPath string
	Runs                           []ScheduleRunView
	RunDetail                      *ScheduleRunDetailView
	HistoryLoaded                  bool
}

// ScheduleRunView contains bounded, content-free execution status.
type ScheduleRunView struct {
	Trigger, State, Requested, Due, Completed, Failure, DetailPath string
}

// ScheduleRunDetailView carries only one run's input and confirmed output.
type ScheduleRunDetailView struct {
	Input, Output, State, Failure, ReportLocatorRef string
}

func ProjectScheduleRunDetail(detail schedulecmd.RunDetail) *ScheduleRunDetailView {
	status := ProjectScheduleRun(detail.Run)
	return &ScheduleRunDetailView{Input: detail.Input, Output: detail.Output,
		ReportLocatorRef: detail.Run.ReportLocatorRef,
		State:            status.State, Failure: status.Failure}
}

// ProjectScheduleRun maps durable execution state to safe operator labels.
func ProjectScheduleRun(run schedulecmd.RunItem) ScheduleRunView {
	view := ScheduleRunView{Requested: scheduleTime(run.RequestedAt),
		Due: scheduleTime(run.DueAt), Completed: scheduleTime(run.CompletedAt)}
	switch run.Trigger {
	case "manual":
		view.Trigger = "Manual"
	case "cron":
		view.Trigger = "Scheduled"
	default:
		view.Trigger = "Unknown"
	}
	switch run.State {
	case "queued", "pending":
		view.State = "Queued"
	case "retrying":
		view.State = "Retrying"
	case "publishing":
		view.State = "Starting"
	case "dispatched":
		view.State = "Waiting for execution"
	case "running":
		view.State = "Running"
	case "report_pending":
		view.State = "Delivering report"
	case "succeeded":
		view.State = "Succeeded"
	case "failed":
		view.State = "Failed"
	case "canceled":
		view.State = "Canceled"
	default:
		view.State = "Status unavailable"
	}
	if run.SafeFailureCode != "" {
		view.Failure = "Execution could not complete."
		switch run.SafeFailureCode {
		case "selection_changed":
			view.Failure = "Schedule selection changed before execution."
		case "execution_failed":
			view.Failure = "Execution failed."
		case "delivery_failed":
			view.Failure = "Report delivery failed."
		case "report_alias_unavailable":
			view.Failure = "Report destination unavailable for this run."
		}
	}
	return view
}

// ProjectScheduleRow omits instruction content from the inventory.
func ProjectScheduleRow(item schedulecmd.Item) ScheduleRow {
	d := item.Definition
	row := ScheduleRow{ID: d.ID, DetailPath: "/schedules/" + url.PathEscape(d.ID), Cron: d.Cron,
		Locator: d.Locator, Status: item.Status, NextRun: scheduleTime(item.NextRunAt),
		LastRun: scheduleTime(item.LastRunAt), Enabled: item.Enabled, Deleted: item.Deleted,
		ReadOnly: item.Source != "managed", Version: item.Version, Source: "Backoffice"}
	if row.ReadOnly {
		row.Source = "Configuration"
	}
	if d.Alias != "" {
		row.Locator = d.Alias
	}
	if row.Locator == "" {
		row.Locator = "None"
	}
	return row
}

// ProjectScheduleEditor retains content only on a guarded detail page.
func ProjectScheduleEditor(item schedulecmd.Item, create bool) *ScheduleEditor {
	d := item.Definition
	e := &ScheduleEditor{Row: ProjectScheduleRow(item), New: create, ID: d.ID, Cron: d.Cron,
		Content: d.Content, ReportTo: d.Locator,
		Action: "/schedules/" + url.PathEscape(d.ID)}
	if d.Alias != "" {
		e.ReportTo = d.Alias
	}
	if create {
		e.Action = "/schedules"
		e.Row.ReadOnly = false
	}
	return e
}

func scheduleTime(at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	return at.UTC().Format("2006-01-02 15:04 UTC")
}
