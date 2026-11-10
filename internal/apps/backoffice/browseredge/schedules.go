package browseredge

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) schedulesPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Schedules(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/schedules/new") + `">Add schedule</a></p><ul>`
	for _, item := range inventory.Items {
		state := statusDisabled
		if item.Deleted {
			state = statusDeleted
		} else if item.Enabled {
			state = statusEnabled
		}
		body += `<li><a href="` + a.path("/schedules/") + url.PathEscape(item.Definition.ID) + `">` + escape(item.Definition.ID) + `</a> · ` +
			escape(item.Definition.Cron) + ` · ` + escape(item.Source) + ` · ` + state + `</li>`
	}
	a.page(w, http.StatusOK, "Schedules", body+`</ul>`)
}

func (a *App) newSchedulePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	a.page(w, http.StatusOK, "Add schedule", a.scheduleForm(r, managementv1.ScheduleItem{}, true))
}

func (a *App) schedulePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("schedule_id")
	item, err := service.Schedule(r.Context(), a.auth(r), id)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/schedules") + `">Schedules</a></p>` + a.scheduleForm(r, item, false)
	params, err := scheduleRunParams(r.URL.Query())
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	runs, err := service.ScheduleRuns(r.Context(), a.auth(r), id, params)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body += `<h2>Runs</h2><ul>`
	for _, run := range runs.Items {
		runID := run.ID.String()
		body += `<li><a href="` + a.path("/schedules/") + url.PathEscape(id) + `?run_id=` + url.QueryEscape(runID) + `">` + escape(runID) + `</a> · ` + escape(run.State) + ` · ` + escape(run.ReportLocatorRef) + `</li>`
	}
	body += htmlListEnd
	if runs.Next != nil {
		body += `<p><a href="` + a.path("/schedules/") + url.PathEscape(id) + `?before_at=` + url.QueryEscape(runs.Next.BeforeAt.UTC().Format(time.RFC3339Nano)) + `&before_id=` + url.QueryEscape(runs.Next.BeforeID.String()) + `">Older runs</a></p>`
	}
	if rawRunID := r.URL.Query().Get("run_id"); rawRunID != "" {
		var parsedRunID managementv1.ScheduleRunID
		if parseErr := parsedRunID.UnmarshalText([]byte(rawRunID)); parseErr != nil {
			a.failure(w, http.StatusBadRequest)
			return
		}
		detail, detailErr := service.ScheduleRun(r.Context(), a.auth(r), id, rawRunID)
		if detailErr != nil {
			a.serviceFailure(w, detailErr, false)
			return
		}
		body += `<h2>Run detail</h2><p>Frozen destination: ` + escape(detail.Run.ReportLocatorRef) + `</p><h3>Input</h3><pre>` + template.HTMLEscapeString(detail.Input) + `</pre><h3>Output</h3><pre>` + template.HTMLEscapeString(detail.Output) + `</pre>`
	}
	a.page(w, http.StatusOK, item.Definition.ID, body)
}

func (a *App) createSchedule(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	definition, err := scheduleDefinitionFromForm(form, strings.TrimSpace(form.Get("id")))
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	item, err := service.CreateSchedule(r.Context(), a.auth(r), managementv1.ScheduleCreateRequest{Definition: definition})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/schedules/"+url.PathEscape(item.Definition.ID))
}

func (a *App) updateSchedule(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	id := r.PathValue("schedule_id")
	definition, err := scheduleDefinitionFromForm(form, id)
	version, versionErr := scheduleVersionFromForm(form)
	if err != nil || versionErr != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if _, err := service.UpdateSchedule(r.Context(), a.auth(r), id, managementv1.ScheduleUpdateRequest{Definition: definition, ExpectedVersion: version}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/schedules/"+url.PathEscape(id))
}

func (a *App) selectSchedule(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := scheduleVersionFromForm(form)
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("schedule_id")
	if _, err := service.SelectSchedule(r.Context(), a.auth(r), id, managementv1.ScheduleSelectionRequest{ExpectedVersion: version, Enabled: form.Get("enabled") == formYesValue}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/schedules/"+url.PathEscape(id))
}

func (a *App) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := scheduleVersionFromForm(form)
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if _, err := service.DeleteSchedule(r.Context(), a.auth(r), r.PathValue("schedule_id"), managementv1.ScheduleDeleteRequest{ExpectedVersion: version}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/schedules")
}

func (a *App) runSchedule(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	request := managementv1.ScheduleRunNowRequest{RequestKey: form.Get("request_key"), ConfirmDisabled: form.Get("confirm_disabled") == formYesValue}
	if !validScheduleRequestKey(request.RequestKey) {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(ScheduleService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("schedule_id")
	if _, err := service.RunSchedule(r.Context(), a.auth(r), id, request); err != nil {
		if errors.Is(err, managementclient.ErrUnavailable) {
			a.page(w, http.StatusServiceUnavailable, "Run state unavailable", `<p>The run may have been queued. Retry with the same request key.</p>`+a.scheduleRunForm(r, id, request.RequestKey, request.ConfirmDisabled))
			return
		}
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/schedules/"+url.PathEscape(id))
}

func (a *App) scheduleForm(r *http.Request, item managementv1.ScheduleItem, create bool) string {
	action := a.path("/schedules")
	if !create {
		action += "/" + url.PathEscape(item.Definition.ID)
	}
	if !create && (item.Source != "managed" || item.Deleted) {
		body := `<p>This schedule is read-only.</p><dl><dt>Cron</dt><dd>` + escape(item.Definition.Cron) + `</dd><dt>Destination</dt><dd>` + escape(scheduleDestination(item.Definition)) + `</dd><dt>Content</dt><dd><pre>` + escape(item.Definition.Content) + `</pre></dd></dl>`
		if !item.Deleted {
			if requestKey, err := randomToken(24); err == nil {
				body += a.scheduleRunForm(r, item.Definition.ID, requestKey, !item.Enabled)
			}
		}
		return body
	}
	body := `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `">`
	if create {
		body += `<label>ID <input name="id" maxlength="128" required></label>`
	} else {
		body += `<input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `">`
	}
	destination := scheduleDestination(item.Definition)
	body += `<label>Cron <input name="cron" value="` + escape(item.Definition.Cron) + `" maxlength="256" required></label><label>Destination <input name="locator" value="` + escape(destination) + `" maxlength="1024"></label><label>Content <textarea name="content" maxlength="131072" required>` + escape(item.Definition.Content) + `</textarea></label><button>Save</button></form>`
	if create {
		return body
	}
	enabled := ""
	if !item.Enabled {
		enabled = enabledHiddenInput
	}
	body += `<form method="post" action="` + action + `/selection"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `"><input type="hidden" name="confirm" value="yes">` + enabled + `<button>` + map[bool]string{true: "Disable", false: "Enable"}[item.Enabled] + `</button></form>`
	body += `<form method="post" action="` + action + `/delete"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm deletion</label><button>Delete</button></form>`
	if requestKey, err := randomToken(24); err == nil {
		body += a.scheduleRunForm(r, item.Definition.ID, requestKey, !item.Enabled)
	}
	return body
}

func scheduleDestination(definition managementv1.ScheduleDefinition) string {
	if definition.Locator != "" {
		return definition.Locator
	}
	return definition.Alias
}

func (a *App) scheduleRunForm(r *http.Request, id, requestKey string, disabled bool) string {
	body := `<form method="post" action="` + a.path("/schedules/") + url.PathEscape(id) + `/runs"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="request_key" value="` + escape(requestKey) + `">`
	if disabled {
		body += `<label><input type="checkbox" name="confirm_disabled" value="yes" required> Run disabled schedule</label>`
	}
	return body + `<button>Run now</button></form>`
}

func scheduleDefinitionFromForm(form url.Values, id string) (managementv1.ScheduleDefinition, error) {
	cron := strings.TrimSpace(form.Get("cron"))
	content := form.Get("content")
	if id == "" || cron == "" || content == "" {
		return managementv1.ScheduleDefinition{}, errors.New("invalid schedule")
	}
	definition := managementv1.ScheduleDefinition{ID: id, Cron: cron, Content: content}
	destination := strings.TrimSpace(form.Get("locator"))
	if strings.Contains(destination, ":") {
		definition.Locator = destination
	} else {
		definition.Alias = destination
	}
	return definition, nil
}

func scheduleVersionFromForm(form url.Values) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version == 0 {
		return 0, errors.New("invalid schedule version")
	}
	return version, nil
}

func scheduleRunParams(query url.Values) (*managementv1.ListScheduleRunsParams, error) {
	beforeAt, beforeID := query.Get("before_at"), query.Get("before_id")
	if beforeAt == "" && beforeID == "" {
		return nil, nil
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, beforeAt)
	var parsedID managementv1.ScheduleRunID
	idErr := parsedID.UnmarshalText([]byte(beforeID))
	if err != nil || idErr != nil || parsedAt.IsZero() {
		return nil, errors.New("invalid run cursor")
	}
	parsedAt = parsedAt.UTC()
	return &managementv1.ListScheduleRunsParams{BeforeAt: &parsedAt, BeforeID: &parsedID}, nil
}

func validScheduleRequestKey(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
