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
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/google/uuid"
)

const webhookHistoryPageSize = 20

// URL form encoding can turn each raw body byte into three transport bytes.
const webhookTestFormLimit = 3*webhookcmd.MaxBodyBytes + (4 << 10)

// WebhooksOperations is Backoffice's consuming port for webhook route policy.
type WebhooksOperations interface {
	Inventory(ctx context.Context, authority webhookroutecmd.Authority) ([]webhookroutecmd.Item, error)
	Get(ctx context.Context, name string, authority webhookroutecmd.Authority) (webhookroutecmd.Item, error)
	Create(ctx context.Context, request webhookroutecmd.Create) (webhookroutecmd.SecretResult, error)
	Update(ctx context.Context, request webhookroutecmd.Update) (webhookroutecmd.Item, error)
	SetEnabled(ctx context.Context, request webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error)
	Delete(ctx context.Context, request webhookroutecmd.Delete) (webhookroutecmd.Item, error)
	Rotate(ctx context.Context, request webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error)
	TestPost(ctx context.Context, request webhookroutecmd.TestPost) (webhookroutecmd.TestResult, error)
	History(ctx context.Context, name string, beforeAt time.Time, beforeJobID string, limit int,
		authority webhookroutecmd.Authority) ([]webhookroutecmd.HistoryItem, error)
	HistoryDetail(ctx context.Context, name, jobID string,
		authority webhookroutecmd.Authority) (webhookroutecmd.HistoryItem, error)
}

// ConfigureWebhooksOperations binds host policy before Backoffice starts.
func (r *Runtime) ConfigureWebhooksOperations(operations WebhooksOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("webhook operations must be configured before Backoffice start")
	}
	r.webhooks = operations
	return nil
}

func (a *httpApp) webhooksPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page, err := a.webhooksView(r, p)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateWebhooks, page)
}

func (a *httpApp) webhookBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Webhooks · Balda", ViewerUsername: p.User.Username,
		CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationWebhooks,
		Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationWebhooks)}
}

func (a *httpApp) webhooksView(r *http.Request, p security.Principal) (webui.Page, error) {
	page := a.webhookBasePage(r, p)
	if a.webhooks == nil {
		return page, webhookroutecmd.ErrUnavailable
	}
	name := r.PathValue("webhook_name")
	if name != "" {
		item, err := a.webhooks.Get(r.Context(), name, a.webhookAuthority(p))
		if err != nil {
			return page, err
		}
		page.Title = item.Definition.Name + " · Webhooks · Balda"
		page.Webhooks = &webui.WebhooksView{Editor: webui.ProjectWebhookEditor(item, false)}
		page.Webhooks.Editor.TestRequestKey = uuid.NewString()
		if r.Method == http.MethodGet {
			beforeAt, beforeJobID, err := webhookHistoryCursor(r.URL.Query())
			if err != nil {
				return page, err
			}
			history, err := a.webhooks.History(r.Context(), name, beforeAt, beforeJobID,
				webhookHistoryPageSize+1, a.webhookAuthority(p))
			if err != nil {
				return page, err
			}
			if len(history) > webhookHistoryPageSize {
				last := history[webhookHistoryPageSize-1]
				page.Webhooks.Editor.NextHistoryPath = page.Webhooks.Editor.Row.DetailPath +
					"?before_at=" + url.QueryEscape(last.CreatedAt.UTC().Format(time.RFC3339Nano)) +
					"&before_job_id=" + url.QueryEscape(last.JobID)
				history = history[:webhookHistoryPageSize]
			}
			for _, record := range history {
				page.Webhooks.Editor.History = append(page.Webhooks.Editor.History,
					webui.ProjectWebhookHistoryRow(record, page.Webhooks.Editor.Row.DetailPath))
			}
			if jobID := r.URL.Query().Get("job_id"); jobID != "" {
				if len(jobID) > 256 {
					return page, webhookroutecmd.ErrInvalid
				}
				record, err := a.webhooks.HistoryDetail(r.Context(), name, jobID, a.webhookAuthority(p))
				if err != nil {
					return page, err
				}
				page.Webhooks.Editor.HistoryDetail = webui.ProjectWebhookHistoryDetail(record)
			}
			page.Webhooks.Editor.HistoryLoaded = true
		}
		return page, nil
	}
	if r.Method == http.MethodPost || r.URL.Query().Get("new") == "1" {
		page.Title = "Add webhook · Balda"
		page.Webhooks = &webui.WebhooksView{Editor: webui.ProjectWebhookEditor(webhookroutecmd.Item{}, true)}
		return page, nil
	}
	items, err := a.webhooks.Inventory(r.Context(), a.webhookAuthority(p))
	if err != nil {
		return page, err
	}
	page.Webhooks = &webui.WebhooksView{}
	for _, item := range items {
		page.Webhooks.Rows = append(page.Webhooks.Rows, webui.ProjectWebhookRow(item))
	}
	return page, nil
}

func webhookHistoryCursor(query url.Values) (time.Time, string, error) {
	if query.Get("before_at") == "" && query.Get("before_job_id") == "" {
		return time.Time{}, "", nil
	}
	beforeAt, err := time.Parse(time.RFC3339Nano, query.Get("before_at"))
	jobID := query.Get("before_job_id")
	if err != nil || beforeAt.IsZero() || jobID == "" || len(jobID) > 256 {
		return time.Time{}, "", webhookroutecmd.ErrInvalid
	}
	return beforeAt.UTC(), jobID, nil
}

func (a *httpApp) webhookTestPost(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, webhookTestFormLimit)
	if !ok {
		return
	}
	page, err := a.webhooksView(r, p)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	editor := page.Webhooks.Editor
	if editor == nil || editor.Row.Deleted {
		a.webhookError(w, r, page, webhookroutecmd.ErrNotFound)
		return
	}
	version, err := webhookVersion(form)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	request := webhookroutecmd.TestPost{Name: r.PathValue("webhook_name"),
		Body: form.Get("body"), RequestKey: form.Get("request_key"),
		ExpectedVersion: version, ConfirmDisabled: form.Get("confirm_disabled") == checkedFormValue,
		Authority: a.webhookAuthority(p)}
	editor.TestBody = request.Body
	if len(request.Body) > webhookcmd.MaxBodyBytes {
		page.Error = &webui.ErrorView{Heading: "Test POST body is too large",
			Message: "The request body must be 1 MiB or smaller."}
		a.render(w, r, http.StatusBadRequest, webui.TemplateWebhooks, page)
		return
	}
	if _, keyErr := uuid.Parse(request.RequestKey); keyErr == nil {
		editor.TestRequestKey = request.RequestKey
	}
	if !editor.Row.Enabled && !request.ConfirmDisabled {
		page.Error = &webui.ErrorView{Heading: "Confirm disabled webhook test",
			Message: "Confirm that you want to send a Test POST through this disabled route."}
		a.render(w, r, http.StatusBadRequest, webui.TemplateWebhooks, page)
		return
	}
	result, err := a.webhooks.TestPost(r.Context(), request)
	if err != nil {
		if errors.Is(err, webhookroutecmd.ErrUnavailable) {
			page.Error = &webui.ErrorView{Heading: "Test POST state unavailable",
				Message: "The request may have been admitted. Retry with the same request key, or check request history before starting another test."}
			a.render(w, r, http.StatusServiceUnavailable, webui.TemplateWebhooks, page)
			return
		}
		a.webhookError(w, r, page, err)
		return
	}
	location := a.path(editor.Row.DetailPath) + "?job_id=" + url.QueryEscape(result.JobID)
	if err := webui.RespondMutationPath(w, r, location); err != nil {
		a.webhookError(w, r, page, webhookroutecmd.ErrUnavailable)
	}
}

func (a *httpApp) webhookAuthority(p security.Principal) webhookroutecmd.Authority {
	return webhookroutecmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version,
		CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion,
		SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
}

func (a *httpApp) webhookMutation(w http.ResponseWriter, r *http.Request) (url.Values, webui.Page, webhookroutecmd.Authority, bool) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return nil, webui.Page{}, webhookroutecmd.Authority{}, false
	}
	page, err := a.webhooksView(r, p)
	if err != nil {
		a.webhookError(w, r, page, err)
		return nil, page, webhookroutecmd.Authority{}, false
	}
	if editor := page.Webhooks.Editor; editor == nil || editor.Row.ReadOnly || editor.Row.Deleted {
		a.webhookError(w, r, page, webhookroutecmd.ErrForbidden)
		return nil, page, webhookroutecmd.Authority{}, false
	}
	return form, page, a.webhookAuthority(p), true
}

func webhookDefinition(form url.Values, name string) webhookroutecmd.Definition {
	return webhookroutecmd.Definition{Name: name, Path: form.Get("path"),
		PromptTemplate: form.Get("prompt_template"), ReportTo: form.Get("report_to"),
		AckOnDelivery: form.Get("ack_on_delivery") == checkedFormValue,
		DedupeSource:  form.Get("dedupe_source"), DedupeHeader: form.Get("dedupe_header")}
}

func webhookVersion(form url.Values) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version == 0 {
		return 0, webhookroutecmd.ErrInvalid
	}
	return version, nil
}

func (a *httpApp) webhookCreate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.webhookMutation(w, r)
	if !ok {
		return
	}
	result, err := a.webhooks.Create(r.Context(), webhookroutecmd.Create{
		Definition: webhookDefinition(form, form.Get("name")), Authority: authority})
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	a.webhookSecretResult(w, r, page, result)
}

func (a *httpApp) webhookUpdate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.webhookMutation(w, r)
	if !ok {
		return
	}
	version, err := webhookVersion(form)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	name := r.PathValue("webhook_name")
	item, err := a.webhooks.Update(r.Context(), webhookroutecmd.Update{
		Name: name, ExpectedVersion: version, Definition: webhookDefinition(form, name), Authority: authority})
	a.webhookResult(w, r, page, item, err, false)
}

func (a *httpApp) webhookSelection(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.webhookMutation(w, r)
	if !ok {
		return
	}
	version, err := webhookConfirmedVersion(form)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	item, err := a.webhooks.SetEnabled(r.Context(), webhookroutecmd.ChangeSelection{
		Name: r.PathValue("webhook_name"), ExpectedVersion: version,
		Enabled: form.Get("enabled") == checkedFormValue, Authority: authority})
	a.webhookResult(w, r, page, item, err, false)
}

func (a *httpApp) webhookDelete(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.webhookMutation(w, r)
	if !ok {
		return
	}
	version, err := webhookConfirmedVersion(form)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	item, err := a.webhooks.Delete(r.Context(), webhookroutecmd.Delete{
		Name: r.PathValue("webhook_name"), ExpectedVersion: version, Authority: authority})
	a.webhookResult(w, r, page, item, err, true)
}

func (a *httpApp) webhookRotate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.webhookMutation(w, r)
	if !ok {
		return
	}
	version, err := webhookConfirmedVersion(form)
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	result, err := a.webhooks.Rotate(r.Context(), webhookroutecmd.Rotate{
		Name: r.PathValue("webhook_name"), ExpectedVersion: version, Authority: authority})
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	a.webhookSecretResult(w, r, page, result)
}

func webhookConfirmedVersion(form url.Values) (uint64, error) {
	version, err := webhookVersion(form)
	if err != nil || form.Get("confirm") != checkedFormValue {
		return 0, webhookroutecmd.ErrInvalid
	}
	return version, nil
}

func (a *httpApp) webhookSecretResult(w http.ResponseWriter, r *http.Request, page webui.Page, result webhookroutecmd.SecretResult) {
	page.Title = result.Item.Definition.Name + " · Webhooks · Balda"
	page.Webhooks = &webui.WebhooksView{Editor: webui.ProjectWebhookEditor(result.Item, false)}
	page.Webhooks.Editor.Secret = result.Secret
	// The secret exists only in this POST response. A subsequent GET uses Get,
	// whose Item cannot carry a secret.
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, r, http.StatusOK, webui.TemplateWebhooks, page)
}

func (a *httpApp) webhookResult(w http.ResponseWriter, r *http.Request, page webui.Page, item webhookroutecmd.Item, err error, remove bool) {
	if err != nil {
		a.webhookError(w, r, page, err)
		return
	}
	location := a.path("/webhooks/" + url.PathEscape(item.Definition.Name))
	if remove {
		location = a.path("/webhooks")
	}
	if err := webui.RespondMutationPath(w, r, location); err != nil {
		a.webhookError(w, r, page, webhookroutecmd.ErrUnavailable)
	}
}

func (a *httpApp) webhookError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := webhookOperationFailure(err)
	page.Error = &webui.ErrorView{Heading: "Webhook operation could not complete", Message: message}
	templateName := webui.TemplateWebhooks
	if page.Webhooks == nil {
		page.Title = "Webhooks unavailable · Balda"
		templateName = webui.TemplateError
	}
	a.render(w, r, status, templateName, page)
}

func webhookOperationFailure(err error) (int, string) {
	switch {
	case errors.Is(err, webhookroutecmd.ErrInvalid):
		return http.StatusBadRequest, "Review the route name, path, prompt template, Report to and deduplication fields."
	case errors.Is(err, webhookroutecmd.ErrForbidden):
		return http.StatusForbidden, "This route is read-only or your administrator authority changed."
	case errors.Is(err, webhookroutecmd.ErrNotFound):
		return http.StatusNotFound, "This webhook route is no longer available. Reopen Webhooks."
	case errors.Is(err, webhookroutecmd.ErrConflict):
		return http.StatusConflict, "The route name or path is in use, or this route changed. Reopen Webhooks before trying again."
	default:
		return http.StatusServiceUnavailable, "The operation could not be completed. Reopen Webhooks and check its current state before retrying."
	}
}
