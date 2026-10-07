package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// SchedulesOperations is Backoffice's port to host-owned schedule policy.
type SchedulesOperations interface {
	Inventory(ctx context.Context, authority schedulecmd.Authority) ([]schedulecmd.Item, error)
	Get(ctx context.Context, id string, authority schedulecmd.Authority) (schedulecmd.Item, error)
	Create(ctx context.Context, request schedulecmd.Create) (schedulecmd.Item, error)
	Update(ctx context.Context, request schedulecmd.Update) (schedulecmd.Item, error)
	SetEnabled(ctx context.Context, request schedulecmd.ChangeSelection) (schedulecmd.Item, error)
	Delete(ctx context.Context, request schedulecmd.Delete) (schedulecmd.Item, error)
}

// ConfigureSchedulesOperations wires host policy before Backoffice starts.
func (r *Runtime) ConfigureSchedulesOperations(operations SchedulesOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("schedule operations must be configured before Backoffice start")
	}
	r.schedules = operations
	return nil
}

func (a *httpApp) schedulesPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page, err := a.schedulesView(r, p)
	if err != nil {
		a.scheduleError(w, r, page, err)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateSchedules, page)
}

func (a *httpApp) scheduleBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Schedules · Balda", ViewerUsername: p.User.Username,
		CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationSchedules,
		Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationSchedules)}
}

func (a *httpApp) schedulesView(r *http.Request, p security.Principal) (webui.Page, error) {
	page := a.scheduleBasePage(r, p)
	if a.schedules == nil {
		return page, schedulecmd.ErrUnavailable
	}
	if r.Method == http.MethodPost && r.PathValue("schedule_id") == "" ||
		r.Method == http.MethodGet && r.PathValue("schedule_id") == "" && r.URL.Query().Get("new") == "1" {
		page.Title = "Add schedule · Balda"
		page.Schedules = &webui.SchedulesView{Editor: webui.ProjectScheduleEditor(schedulecmd.Item{}, true)}
		return page, nil
	}
	id := r.PathValue("schedule_id")
	if id != "" {
		item, err := a.schedules.Get(r.Context(), id, a.scheduleAuthority(p))
		if err != nil {
			return page, err
		}
		page.Title = item.Definition.ID + " · Schedules · Balda"
		page.Schedules = &webui.SchedulesView{Editor: webui.ProjectScheduleEditor(item, false)}
		return page, nil
	}
	items, err := a.schedules.Inventory(r.Context(), a.scheduleAuthority(p))
	if err != nil {
		return page, err
	}
	page.Schedules = &webui.SchedulesView{}
	for _, item := range items {
		page.Schedules.Rows = append(page.Schedules.Rows, webui.ProjectScheduleRow(item))
	}
	return page, nil
}

func (a *httpApp) scheduleAuthority(p security.Principal) schedulecmd.Authority {
	return schedulecmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version,
		CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion,
		SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
}

func (a *httpApp) scheduleMutation(w http.ResponseWriter, r *http.Request) (url.Values, webui.Page, schedulecmd.Authority, bool) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return nil, webui.Page{}, schedulecmd.Authority{}, false
	}
	page, err := a.schedulesView(r, p)
	if err != nil {
		a.scheduleError(w, r, page, err)
		return nil, page, schedulecmd.Authority{}, false
	}
	if editor := page.Schedules.Editor; editor != nil && (editor.Row.ReadOnly || editor.Row.Deleted) {
		a.scheduleError(w, r, page, schedulecmd.ErrForbidden)
		return nil, page, schedulecmd.Authority{}, false
	}
	return form, page, a.scheduleAuthority(p), true
}

func (a *httpApp) scheduleCreate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.scheduleMutation(w, r)
	if !ok {
		return
	}
	item, err := a.schedules.Create(r.Context(), schedulecmd.Create{Definition: scheduleDefinition(form, form.Get("id")), Authority: authority})
	a.scheduleResult(w, r, page, item, err, false)
}

func (a *httpApp) scheduleUpdate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.scheduleMutation(w, r)
	if !ok {
		return
	}
	version, err := scheduleVersion(form)
	if err != nil {
		a.scheduleError(w, r, page, err)
		return
	}
	id := r.PathValue("schedule_id")
	item, err := a.schedules.Update(r.Context(), schedulecmd.Update{ID: id, ExpectedVersion: version,
		Definition: scheduleDefinition(form, id), Authority: authority})
	a.scheduleResult(w, r, page, item, err, false)
}

func (a *httpApp) scheduleSelection(w http.ResponseWriter, r *http.Request) {
	a.scheduleChange(w, r, false)
}

func (a *httpApp) scheduleDelete(w http.ResponseWriter, r *http.Request) {
	a.scheduleChange(w, r, true)
}

func (a *httpApp) scheduleChange(w http.ResponseWriter, r *http.Request, remove bool) {
	form, page, authority, ok := a.scheduleMutation(w, r)
	if !ok {
		return
	}
	version, err := scheduleVersion(form)
	if err != nil || form.Get("confirm") != checkedFormValue {
		a.scheduleError(w, r, page, schedulecmd.ErrInvalid)
		return
	}
	id := r.PathValue("schedule_id")
	var item schedulecmd.Item
	if remove {
		item, err = a.schedules.Delete(r.Context(), schedulecmd.Delete{ID: id, ExpectedVersion: version, Authority: authority})
	} else {
		item, err = a.schedules.SetEnabled(r.Context(), schedulecmd.ChangeSelection{ID: id,
			ExpectedVersion: version, Enabled: form.Get("enabled") == checkedFormValue, Authority: authority})
	}
	a.scheduleResult(w, r, page, item, err, remove)
}

func scheduleDefinition(form url.Values, id string) schedulecmd.Definition {
	d := schedulecmd.Definition{ID: id, Cron: form.Get("cron"), Content: form.Get("content"),
		Target: schedulecmd.Target{Kind: form.Get("target_kind"), Key: form.Get("target_key")}}
	if form.Get("report_to_kind") != "" || form.Get("report_to_key") != "" {
		d.ReportTo = &schedulecmd.Target{Kind: form.Get("report_to_kind"), Key: form.Get("report_to_key")}
	}
	return d
}

func scheduleVersion(form url.Values) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version == 0 {
		return 0, schedulecmd.ErrInvalid
	}
	return version, nil
}

func (a *httpApp) scheduleResult(w http.ResponseWriter, r *http.Request, page webui.Page, item schedulecmd.Item, err error, remove bool) {
	if err != nil {
		a.scheduleError(w, r, page, err)
		return
	}
	location := a.path("/schedules/" + url.PathEscape(item.Definition.ID))
	if remove {
		location = a.path("/schedules")
	}
	if err := webui.RespondMutationPath(w, r, location); err != nil {
		a.scheduleError(w, r, page, schedulecmd.ErrUnavailable)
	}
}

func (a *httpApp) scheduleError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := scheduleOperationFailure(err)
	page.Error = &webui.ErrorView{Heading: "Schedule operation could not complete", Message: message}
	templateName := webui.TemplateSchedules
	if page.Schedules == nil {
		page.Title = "Schedules unavailable · Balda"
		templateName = webui.TemplateError
	}
	a.render(w, r, status, templateName, page)
}

func scheduleOperationFailure(err error) (int, string) {
	switch {
	case errors.Is(err, schedulecmd.ErrInvalid):
		return http.StatusBadRequest, "Review the schedule ID, five-field UTC cron, destination and content. The destination must already exist."
	case errors.Is(err, schedulecmd.ErrForbidden):
		return http.StatusForbidden, "This schedule is read-only or your administrator authority changed."
	case errors.Is(err, schedulecmd.ErrNotFound):
		return http.StatusNotFound, "This schedule is no longer available. Reopen Schedules."
	case errors.Is(err, schedulecmd.ErrConflict):
		return http.StatusConflict, "The schedule changed. Reopen it before trying again."
	default:
		return http.StatusServiceUnavailable, "The operation could not be completed. Reopen Schedules and try again."
	}
}
