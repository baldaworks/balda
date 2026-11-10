package browseredge

import (
	"crypto/rand"
	"encoding/hex"
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

const (
	webhookMutationFormLimit = 1 << 20
	webhookTestFormLimit     = 3*(1<<20) + (4 << 10)
)

func (a *App) webhooksPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(WebhookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Webhooks(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/webhooks/new") + `">Add webhook</a></p><ul>`
	for _, item := range inventory.Items {
		state := statusDisabled
		if item.Deleted {
			state = statusDeleted
		} else if item.Enabled {
			state = statusEnabled
		}
		body += `<li><a href="` + a.path("/webhooks/") + url.PathEscape(item.Definition.Name) + `">` + escape(item.Definition.Name) + `</a> · ` + escape(string(item.Source)) + ` · ` + state + ` · <code>` + escape(a.webhookURLPrefix+item.Definition.Name) + `</code></li>`
	}
	a.page(w, http.StatusOK, "Webhooks", body+`</ul>`)
}

func (a *App) newWebhookPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	a.page(w, http.StatusOK, "Add webhook", a.webhookForm(r, managementv1.WebhookItem{}, true))
}

func (a *App) webhookPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(WebhookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("webhook_name")
	item, err := service.Webhook(r.Context(), a.auth(r), name)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/webhooks") + `">Webhooks</a></p>` + a.webhookForm(r, item, false)
	params, err := webhookHistoryParams(r.URL.Query())
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	history, err := service.WebhookHistory(r.Context(), a.auth(r), name, params)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body += `<h2>History</h2><ul>`
	for _, row := range history.Items {
		body += `<li><a href="` + a.path("/webhooks/") + url.PathEscape(name) + `?job_id=` + url.QueryEscape(row.JobID) + `">` + escape(row.JobID) + `</a> · ` + escape(row.Source) + ` · ` + escape(row.JobStatus) + ` · ` + escape(row.DeliveryStatus) + `</li>`
	}
	body += `</ul>`
	if history.Next != nil {
		body += `<p><a href="` + a.path("/webhooks/") + url.PathEscape(name) + `?before_at=` + url.QueryEscape(history.Next.BeforeAt.UTC().Format(time.RFC3339Nano)) + `&before_job_id=` + url.QueryEscape(history.Next.BeforeJobID) + `">Older requests</a></p>`
	}
	if jobID := r.URL.Query().Get("job_id"); jobID != "" {
		if len(jobID) > 256 {
			a.failure(w, http.StatusBadRequest)
			return
		}
		detail, detailErr := service.WebhookHistoryDetail(r.Context(), a.auth(r), name, jobID)
		if detailErr != nil {
			a.serviceFailure(w, detailErr, false)
			return
		}
		body += `<h2>Request detail</h2><h3>Input</h3><pre>` + template.HTMLEscapeString(detail.Input) + `</pre><h3>Output</h3><pre>` + template.HTMLEscapeString(detail.Output) + `</pre><h3>Delivery</h3><pre>` + template.HTMLEscapeString(detail.DeliveryPayload) + `</pre>`
	}
	a.page(w, http.StatusOK, item.Definition.Name, body)
}

func (a *App) createWebhook(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationFormLimit(w, r, webhookMutationFormLimit)
	if !ok {
		return
	}
	definition, err := webhookDefinitionFromForm(form, strings.TrimSpace(form.Get("name")))
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(WebhookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	result, err := service.CreateWebhook(r.Context(), a.auth(r), managementv1.WebhookCreateRequest{Definition: definition})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.webhookSecretPage(w, r, result)
}

func (a *App) updateWebhook(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationFormLimit(w, r, webhookMutationFormLimit)
	if !ok {
		return
	}
	service, ok := a.service.(WebhookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("webhook_name")
	definition, err := webhookDefinitionFromForm(form, name)
	version, versionErr := webhookVersionFromForm(form)
	if err != nil || versionErr != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	if _, err := service.UpdateWebhook(r.Context(), a.auth(r), name, managementv1.WebhookUpdateRequest{ExpectedVersion: version, Definition: definition}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/webhooks/"+url.PathEscape(name))
}

func (a *App) selectWebhook(w http.ResponseWriter, r *http.Request) {
	a.webhookVersionMutation(w, r, "selection")
}
func (a *App) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	a.webhookVersionMutation(w, r, "delete")
}
func (a *App) rotateWebhook(w http.ResponseWriter, r *http.Request) {
	a.webhookVersionMutation(w, r, "rotate")
}

func (a *App) webhookVersionMutation(w http.ResponseWriter, r *http.Request, action string) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := webhookVersionFromForm(form)
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(WebhookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("webhook_name")
	switch action {
	case "selection":
		_, err = service.SelectWebhook(r.Context(), a.auth(r), name, managementv1.WebhookSelectionRequest{ExpectedVersion: version, Enabled: form.Get("enabled") == formYesValue})
	case "delete":
		_, err = service.DeleteWebhook(r.Context(), a.auth(r), name, managementv1.WebhookVersionRequest{ExpectedVersion: version})
	case "rotate":
		var result managementv1.WebhookSecretResult
		result, err = service.RotateWebhook(r.Context(), a.auth(r), name, managementv1.WebhookVersionRequest{ExpectedVersion: version})
		if err == nil {
			a.webhookSecretPage(w, r, result)
			return
		}
	}
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if action == "delete" {
		a.redirect(w, "/webhooks")
		return
	}
	a.redirect(w, "/webhooks/"+url.PathEscape(name))
}

func (a *App) testWebhook(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationFormLimit(w, r, webhookTestFormLimit)
	if !ok {
		return
	}
	version, err := webhookVersionFromForm(form)
	requestKey := form.Get("request_key")
	var parsedKey managementv1.ScheduleRunID
	if err != nil || parsedKey.UnmarshalText([]byte(requestKey)) != nil || len(form.Get("body")) > 1<<20 {
		a.failure(w, http.StatusBadRequest)
		return
	}
	request := managementv1.WebhookTestRequest{ExpectedVersion: version, RequestKey: parsedKey, ConfirmDisabled: form.Get("confirm_disabled") == formYesValue, Body: form.Get("body")}
	service, ok := a.service.(WebhookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("webhook_name")
	result, err := service.TestWebhook(r.Context(), a.auth(r), name, request)
	if err != nil {
		if errors.Is(err, managementclient.ErrUnavailable) {
			a.page(w, http.StatusServiceUnavailable, "Test POST state unavailable", `<p>The request may have been admitted. Retry with the same request key.</p>`+a.webhookTestForm(r, name, version, requestKey, request.Body, request.ConfirmDisabled, request.ConfirmDisabled))
			return
		}
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/webhooks/"+url.PathEscape(name)+"?job_id="+url.QueryEscape(result.JobID))
}

func (a *App) webhookSecretPage(w http.ResponseWriter, r *http.Request, result managementv1.WebhookSecretResult) {
	w.Header().Set("Cache-Control", "no-store")
	body := `<p>Copy this secret now. It will not be shown again.</p><pre>` + escape(result.Secret) + `</pre><p><a href="` + a.path("/webhooks/") + url.PathEscape(result.Item.Definition.Name) + `">Continue</a></p>`
	a.page(w, http.StatusOK, result.Item.Definition.Name, body)
}

func (a *App) webhookForm(r *http.Request, item managementv1.WebhookItem, create bool) string {
	action := a.path("/webhooks")
	if !create {
		action += "/" + url.PathEscape(item.Definition.Name)
	}
	if !create && (item.Source != managementv1.WebhookItemSourceManaged || item.Deleted) {
		body := `<p>This webhook is read-only.</p><p>URL: <code>` + escape(a.webhookURLPrefix+item.Definition.Name) + `</code></p><pre>` + escape(item.Definition.PromptTemplate) + `</pre>`
		if !item.Deleted {
			key, _ := newWebhookRequestKey()
			body += a.webhookTestForm(r, item.Definition.Name, item.Version, key, "", !item.Enabled, false)
		}
		return body
	}
	body := `<p>URL prefix: <code>` + escape(a.webhookURLPrefix) + `</code></p><form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `">`
	if create {
		body += `<label>Name <input name="name" maxlength="64" required></label>`
	} else {
		body += `<input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `">`
	}
	body += `<label>Prompt template <textarea name="prompt_template" maxlength="131072" required>` + escape(item.Definition.PromptTemplate) + `</textarea></label><label>Report to <input name="report_to" value="` + escape(item.Definition.ReportTo) + `" maxlength="1024"></label><label><input type="checkbox" name="ack_on_delivery" value="yes"` + checked(item.Definition.AckOnDelivery) + `> Acknowledge on delivery</label><label>Dedupe <select name="dedupe_source">` + webhookDedupeOptions(item.Definition.DedupeSource) + `</select></label><label>Dedupe header <input name="dedupe_header" value="` + escape(item.Definition.DedupeHeader) + `" maxlength="256"></label><button>Save</button></form>`
	if create {
		return body
	}
	enabled := ""
	if !item.Enabled {
		enabled = enabledHiddenInput
	}
	base := `<input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `">`
	body += `<form method="post" action="` + action + `/selection">` + base + `<input type="hidden" name="confirm" value="yes">` + enabled + `<button>` + map[bool]string{true: "Disable", false: "Enable"}[item.Enabled] + `</button></form>`
	body += `<form method="post" action="` + action + `/rotate">` + base + `<label><input type="checkbox" name="confirm" value="yes" required> Confirm rotation</label><button>Rotate secret</button></form>`
	body += `<form method="post" action="` + action + `/delete">` + base + `<label><input type="checkbox" name="confirm" value="yes" required> Confirm deletion</label><button>Delete</button></form>`
	key, _ := newWebhookRequestKey()
	body += a.webhookTestForm(r, item.Definition.Name, item.Version, key, "", !item.Enabled, false)
	return body
}

func (a *App) webhookTestForm(r *http.Request, name string, version uint64, key, body string, showDisabled, confirmed bool) string {
	result := `<form method="post" action="` + a.path("/webhooks/") + url.PathEscape(name) + `/test"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(version, 10) + `"><input type="hidden" name="request_key" value="` + escape(key) + `"><textarea name="body" maxlength="1048576">` + escape(body) + `</textarea>`
	if showDisabled {
		result += `<label><input type="checkbox" name="confirm_disabled" value="yes" required` + checked(confirmed) + `> Test disabled webhook</label>`
	}
	return result + `<button>Test POST</button></form>`
}

func webhookDefinitionFromForm(form url.Values, name string) (managementv1.WebhookDefinition, error) {
	prompt := form.Get("prompt_template")
	source := form.Get("dedupe_source")
	if name == "" || prompt == "" || (source != "request_id" && source != "header" && source != "body_sha256") {
		return managementv1.WebhookDefinition{}, errors.New("invalid webhook")
	}
	return managementv1.WebhookDefinition{Name: name, PromptTemplate: prompt, ReportTo: strings.TrimSpace(form.Get("report_to")), AckOnDelivery: form.Get("ack_on_delivery") == formYesValue, DedupeSource: managementv1.WebhookDefinitionDedupeSource(source), DedupeHeader: strings.TrimSpace(form.Get("dedupe_header"))}, nil
}

func webhookVersionFromForm(form url.Values) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New("invalid version")
	}
	return value, nil
}
func webhookDedupeOptions(selected managementv1.WebhookDefinitionDedupeSource) string {
	var result string
	for _, value := range []string{"request_id", "header", "body_sha256"} {
		mark := ""
		if string(selected) == value {
			mark = " selected"
		}
		result += `<option value="` + value + `"` + mark + `>` + value + `</option>`
	}
	return result
}
func webhookHistoryParams(query url.Values) (*managementv1.ListWebhookHistoryParams, error) {
	at, jobID := query.Get("before_at"), query.Get("before_job_id")
	if at == "" && jobID == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || parsed.IsZero() || jobID == "" || len(jobID) > 256 {
		return nil, errors.New("invalid cursor")
	}
	parsed = parsed.UTC()
	return &managementv1.ListWebhookHistoryParams{BeforeAt: &parsed, BeforeJobID: &jobID}, nil
}

func newWebhookRequestKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
