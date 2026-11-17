package backoffice

import (
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
)

var scheduleAliasQAEntries = []qaEntry{
	{name: "schedules", label: "Schedules · inventory", templateName: webui.TemplateSchedules, page: qaSchedules, gallery: true},
	{name: "schedules-empty", label: "Schedules · empty", templateName: webui.TemplateSchedules, page: func() webui.Page {
		p := qaSchedules()
		p.Schedules.Rows = nil
		return p
	}, gallery: true},
	{name: "schedules/new", label: "Schedules · create", templateName: webui.TemplateSchedules, page: func() webui.Page {
		return qaScheduleEditor(schedulecmd.Item{}, true)
	}, gallery: true},
	{name: "schedules/daily-summary", label: "Schedules · edit and run history", templateName: webui.TemplateSchedules, page: func() webui.Page {
		return qaScheduleEditor(qaManagedSchedule(), false)
	}, gallery: true},
	{name: "schedules-disabled", label: "Schedules · disabled and confirmed manual run", templateName: webui.TemplateSchedules, page: func() webui.Page {
		item := qaManagedSchedule()
		item.Enabled = false
		return qaScheduleEditor(item, false)
	}, gallery: true},
	{name: "schedules-deleted", label: "Schedules · deleted inventory filter", templateName: webui.TemplateSchedules, page: qaDeletedSchedules, gallery: true},
	{name: "schedules-all", label: "Schedules · active and deleted inventory", templateName: webui.TemplateSchedules, page: qaAllSchedules, gallery: true},
	{name: "schedules/deleted-summary", label: "Schedules · deleted detail with retained history", templateName: webui.TemplateSchedules, page: qaDeletedSchedule, gallery: true},
	{name: "schedules-config", label: "Schedules · configuration read-only", templateName: webui.TemplateSchedules, page: func() webui.Page {
		return qaScheduleEditor(qaConfiguredSchedule(), false)
	}, gallery: true},
	{name: "schedules-history", label: "Schedules · completed run detail", templateName: webui.TemplateSchedules, page: qaScheduleHistory, gallery: true},
	{name: "schedules-long", label: "Schedules · long locator and content", templateName: webui.TemplateSchedules, page: qaLongSchedule, gallery: true},
	{name: "schedules-invalid", label: "Schedules · invalid definition (400)", templateName: webui.TemplateSchedules, page: func() webui.Page {
		return qaScheduleFailure(schedulecmd.ErrInvalid, true)
	}, status: http.StatusBadRequest, gallery: true},
	{name: "schedules-conflict", label: "Schedules · conflicting change (409)", templateName: webui.TemplateSchedules, page: func() webui.Page {
		return qaScheduleFailure(schedulecmd.ErrConflict, false)
	}, status: http.StatusConflict, gallery: true},
	{name: "schedules-unavailable", label: "Schedules · unavailable management (503)", templateName: webui.TemplateSchedules, page: func() webui.Page {
		return qaScheduleFailure(schedulecmd.ErrUnavailable, false)
	}, status: http.StatusServiceUnavailable, gallery: true},

	{name: "aliases", label: "Aliases · inventory", templateName: webui.TemplateAliases, page: qaAliases, gallery: true},
	{name: "aliases-empty", label: "Aliases · empty", templateName: webui.TemplateAliases, page: func() webui.Page {
		p := qaAliases()
		p.Aliases.Rows = nil
		return p
	}, gallery: true},
	{name: "aliases/new", label: "Aliases · create", templateName: webui.TemplateAliases, page: func() webui.Page {
		return qaAliasEditor(aliascmd.Record{}, true)
	}, gallery: true},
	{name: "aliases/main_chat", label: "Aliases · edit and delete", templateName: webui.TemplateAliases, page: func() webui.Page {
		return qaAliasEditor(qaAlias(), false)
	}, gallery: true},
	{name: "aliases-long", label: "Aliases · long locator", templateName: webui.TemplateAliases, page: qaLongAlias, gallery: true},
	{name: "aliases-invalid", label: "Aliases · invalid mapping (400)", templateName: webui.TemplateAliases, page: func() webui.Page {
		return qaAliasFailure(aliascmd.ErrInvalid, true)
	}, status: http.StatusBadRequest, gallery: true},
	{name: "aliases-conflict", label: "Aliases · conflicting change (409)", templateName: webui.TemplateAliases, page: func() webui.Page {
		return qaAliasFailure(aliascmd.ErrConflict, false)
	}, status: http.StatusConflict, gallery: true},
	{name: "aliases-unavailable", label: "Aliases · unavailable management (503)", templateName: webui.TemplateAliases, page: func() webui.Page {
		return qaAliasFailure(aliascmd.ErrUnavailable, false)
	}, status: http.StatusServiceUnavailable, gallery: true},
}

func qaManagedSchedule() schedulecmd.Item {
	return schedulecmd.Item{
		Definition: schedulecmd.Definition{ID: "daily-summary", Cron: "0 9 * * *", Alias: "main_chat", Content: "Summarize yesterday's work."},
		Source:     "managed", Enabled: true, Version: 4, Status: "Scheduled",
		NextRunAt: qaNow.AddDate(0, 0, 1), LastRunAt: qaNow.Add(-2 * time.Hour),
	}
}

func qaConfiguredSchedule() schedulecmd.Item {
	return schedulecmd.Item{
		Definition: schedulecmd.Definition{ID: "config:morning", Cron: "30 8 * * 1-5", Locator: "telegram:9001:0", Content: "Prepare the configured morning briefing."},
		Source:     "config", Enabled: true, Version: 1, Status: "Scheduled", NextRunAt: qaNow.AddDate(0, 0, 1),
	}
}

func qaSchedules() webui.Page {
	managed, configured := qaManagedSchedule(), qaConfiguredSchedule()
	return webui.Page{
		Title: "Schedules · QA", Current: webui.LocationSchedules, Navigation: qaAdminNavigation(webui.LocationSchedules), CSRFToken: "qa-csrf",
		Schedules: &webui.SchedulesView{
			Rows:      []webui.ScheduleRow{webui.ProjectScheduleRow(managed), webui.ProjectScheduleRow(configured)},
			Lifecycle: lifecycleView("/schedules", lifecycleActive), EmptyMessage: "No active schedules.",
		},
	}
}

func qaScheduleEditor(item schedulecmd.Item, create bool) webui.Page {
	p := qaSchedules()
	p.Schedules.Rows = nil
	p.Schedules.Editor = webui.ProjectScheduleEditor(item, create)
	if create {
		p.Title = "Add schedule · QA"
		return p
	}
	p.Title = item.Definition.ID + " · Schedules · QA"
	p.Schedules.Editor.RunRequestKey = "qa-run-request"
	p.Schedules.Editor.HistoryLoaded = true
	p.Schedules.Editor.Runs = qaScheduleRuns(item.Definition.ID)
	return p
}

func qaScheduleRuns(scheduleID string) []webui.ScheduleRunView {
	runs := []schedulecmd.RunItem{
		{ID: "qa-run-succeeded", Trigger: "manual", RequestedAt: qaNow.Add(-2 * time.Hour), DueAt: qaNow.Add(-2 * time.Hour), CompletedAt: qaNow.Add(-2*time.Hour + time.Minute), State: "succeeded", ReportLocatorRef: "telegram:9001:0"},
		{ID: "qa-run-failed", Trigger: "cron", RequestedAt: qaNow.Add(-26 * time.Hour), DueAt: qaNow.Add(-26 * time.Hour), CompletedAt: qaNow.Add(-26*time.Hour + time.Minute), State: "failed", SafeFailureCode: "delivery_failed", ReportLocatorRef: "telegram:9001:0"},
	}
	views := make([]webui.ScheduleRunView, 0, len(runs))
	for _, run := range runs {
		view := webui.ProjectScheduleRun(run)
		view.DetailPath = "/schedules/" + scheduleID + "?run_id=" + run.ID
		views = append(views, view)
	}
	return views
}

func qaDeletedSchedule() webui.Page {
	item := qaDeletedScheduleItem()
	p := qaScheduleEditor(item, false)
	p.Schedules.Lifecycle = lifecycleView("/schedules", lifecycleDeleted)
	p.Schedules.EmptyMessage = "No deleted schedules."
	return p
}

func qaDeletedScheduleItem() schedulecmd.Item {
	item := qaManagedSchedule()
	item.Definition.ID = "deleted-summary"
	item.Enabled, item.Deleted, item.Status = false, true, deletedLabel
	return item
}

func qaDeletedSchedules() webui.Page {
	p := qaSchedules()
	p.Schedules.Rows = []webui.ScheduleRow{webui.ProjectScheduleRow(qaDeletedScheduleItem())}
	p.Schedules.Lifecycle = lifecycleView("/schedules", lifecycleDeleted)
	p.Schedules.EmptyMessage = "No deleted schedules."
	return p
}

func qaAllSchedules() webui.Page {
	p := qaSchedules()
	p.Schedules.Rows = append(p.Schedules.Rows, webui.ProjectScheduleRow(qaDeletedScheduleItem()))
	p.Schedules.Lifecycle = lifecycleView("/schedules", lifecycleAll)
	p.Schedules.EmptyMessage = "No schedules."
	return p
}

func qaScheduleHistory() webui.Page {
	p := qaScheduleEditor(qaManagedSchedule(), false)
	p.Schedules.Editor.RunDetail = webui.ProjectScheduleRunDetail(schedulecmd.RunDetail{
		Run:   schedulecmd.RunItem{ID: "qa-run-succeeded", Trigger: "manual", RequestedAt: qaNow.Add(-2 * time.Hour), State: "succeeded", CompletedAt: qaNow.Add(-2*time.Hour + time.Minute), ReportLocatorRef: "telegram:9001:0"},
		Input: "Summarize yesterday's work.", Output: "Synthetic summary output.",
	})
	return p
}

func qaLongSchedule() webui.Page {
	item := qaManagedSchedule()
	item.Definition.Locator, item.Definition.Alias = "telegram:"+strings.Repeat("1234567890", 35)+":0", ""
	item.Definition.Content = strings.Repeat("Synthetic schedule content remains readable. ", 18)
	return qaScheduleEditor(item, false)
}

func qaScheduleFailure(err error, create bool) webui.Page {
	p := qaScheduleEditor(qaManagedSchedule(), create)
	switch err {
	case schedulecmd.ErrInvalid:
		p.Error = &webui.ErrorView{Heading: "Schedule operation could not complete", Message: "Review the schedule ID, cron expression, destination and content before trying again."}
	case schedulecmd.ErrConflict:
		p.Error = &webui.ErrorView{Heading: "Schedule operation could not complete", Message: "The schedule or administrator authority changed. Reopen the schedule before trying again."}
	default:
		p.Error = &webui.ErrorView{Heading: "Schedule management unavailable", Message: "Reopen Schedules after the service recovers."}
	}
	return p
}

func qaAlias() aliascmd.Record {
	return aliascmd.Record{Name: "main_chat", LocatorRef: "telegram:9001:0", Version: 3}
}

func qaAliases() webui.Page {
	return webui.Page{
		Title: "Aliases · QA", Current: webui.LocationAliases, Navigation: qaAdminNavigation(webui.LocationAliases), CSRFToken: "qa-csrf",
		Aliases: &webui.AliasesView{Rows: []webui.AliasRow{
			webui.ProjectAliasRow(qaAlias()),
			webui.ProjectAliasRow(aliascmd.Record{Name: "alerts", LocatorRef: "slackagent:T1:C1", Version: 1}),
		}},
	}
}

func qaAliasEditor(record aliascmd.Record, create bool) webui.Page {
	p := qaAliases()
	p.Aliases.Rows = nil
	p.Aliases.Editor = webui.ProjectAliasEditor(record, create)
	if create {
		p.Title = "Add alias · QA"
	} else {
		p.Title = record.Name + " · Aliases · QA"
	}
	return p
}

func qaLongAlias() webui.Page {
	record := qaAlias()
	record.LocatorRef = "telegram:" + strings.Repeat("1234567890", 35) + ":0"
	return qaAliasEditor(record, false)
}

func qaAliasFailure(err error, create bool) webui.Page {
	p := qaAliasEditor(qaAlias(), create)
	switch err {
	case aliascmd.ErrInvalid:
		p.Error = &webui.ErrorView{Heading: "Alias operation could not complete", Message: "Review the alias name and public locator before trying again."}
	case aliascmd.ErrConflict:
		p.Error = &webui.ErrorView{Heading: "Alias operation could not complete", Message: "The alias or administrator authority changed. Reopen the alias before trying again."}
	default:
		p.Error = &webui.ErrorView{Heading: "Alias management unavailable", Message: "Reopen Aliases after the service recovers."}
	}
	return p
}
