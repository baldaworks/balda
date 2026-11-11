package browseredge

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) auditPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(AuditService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	params, err := auditParams(r.URL.Query())
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	page, err := service.AuditEvents(r.Context(), a.auth(r), params)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a></p>` + auditFilterForm(a, r.URL.Query()) + `<ul>`
	for _, event := range page.Events {
		body += `<li><time>` + escape(event.OccurredAt.UTC().Format(time.RFC3339)) + `</time> · <strong>` + escape(event.Action) + `</strong> · ` + escape(event.Outcome) + ` · ` + escape(event.TargetType)
		if event.TargetID != nil {
			body += ` <code>` + escape(*event.TargetID) + `</code>`
		}
		if event.ActorUserID != nil {
			body += ` · actor <code>` + escape(*event.ActorUserID) + `</code>`
		}
		body += `</li>`
	}
	body += htmlListEnd
	if page.NextAfterID != nil {
		query := r.URL.Query()
		query.Set("after_id", *page.NextAfterID)
		body += `<p><a href="` + a.path("/audit?") + escape(query.Encode()) + `">Next</a></p>`
	}
	a.page(w, http.StatusOK, "Audit", body)
}

func auditParams(query url.Values) (*managementv1.ListAuditEventsParams, error) {
	params := &managementv1.ListAuditEventsParams{}
	setOptionalString := func(key string, target **string) {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			*target = &value
		}
	}
	setOptionalString("after_id", &params.AfterID)
	setOptionalString("action", &params.Action)
	setOptionalString("outcome", &params.Outcome)
	setOptionalString("target_type", &params.TargetType)
	setOptionalString("actor_user_id", &params.ActorUserID)
	if value := strings.TrimSpace(query.Get("limit")); value != "" {
		limit, err := strconv.ParseInt(value, 10, 32)
		if err != nil || limit < 1 || limit > 100 {
			return nil, strconv.ErrSyntax
		}
		converted := int32(limit)
		params.Limit = &converted
	}
	for key, target := range map[string]**time.Time{"from": &params.From, "before": &params.Before} {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return nil, err
			}
			*target = &parsed
		}
	}
	return params, nil
}

func auditFilterForm(a *App, query url.Values) string {
	return `<form method="get" action="` + a.path("/audit") + `"><label>Action <input name="action" value="` + escape(query.Get("action")) + `" maxlength="128"></label><label>Outcome <input name="outcome" value="` + escape(query.Get("outcome")) + `" maxlength="32"></label><label>Target <input name="target_type" value="` + escape(query.Get("target_type")) + `" maxlength="64"></label><label>Actor <input name="actor_user_id" value="` + escape(query.Get("actor_user_id")) + `" maxlength="128"></label><label>From (RFC3339) <input name="from" value="` + escape(query.Get("from")) + `"></label><label>Before (RFC3339) <input name="before" value="` + escape(query.Get("before")) + `"></label><button>Filter</button></form>`
}
