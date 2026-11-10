package browseredge

import (
	"html/template"
	"net/http"
	"strconv"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
)

func (a *App) systemOverview(w http.ResponseWriter, r *http.Request) (string, bool) {
	service, ok := a.service.(SystemService)
	if !ok {
		a.serviceFailure(w, managementclient.ErrUnavailable, false)
		return "", false
	}
	view, err := service.SystemOverview(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return "", false
	}
	body := `<h2>Configured integrations</h2>`
	if len(view.Capabilities) == 0 {
		return body + `<p>No integrations configured.</p>`, true
	}
	body += `<ul>`
	for _, capability := range view.Capabilities {
		body += `<li><strong>` + template.HTMLEscapeString(capability.Name) + `</strong> · ` + template.HTMLEscapeString(capability.Mode)
		if capability.RouteCount > 0 {
			body += ` · routes: ` + strconv.Itoa(capability.RouteCount)
		}
		for _, setting := range capability.Settings {
			body += `<br>` + template.HTMLEscapeString(setting.Label) + `: ` + template.HTMLEscapeString(setting.Value)
		}
		for _, endpoint := range capability.Endpoints {
			body += `<br>` + template.HTMLEscapeString(endpoint.Label) + `: <code>` + template.HTMLEscapeString(endpoint.URL) + `</code>`
		}
		body += `</li>`
	}
	return body + `</ul>`, true
}

func (a *App) agentPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(SystemService)
	if !ok {
		a.serviceFailure(w, managementclient.ErrUnavailable, false)
		return
	}
	agent, err := service.SystemAgent(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a></p><dl>` +
		`<dt>Profile</dt><dd>` + template.HTMLEscapeString(agent.Profile) + `</dd>` +
		`<dt>Provider ID</dt><dd>` + template.HTMLEscapeString(agent.ProviderID) + `</dd>` +
		`<dt>Provider type</dt><dd>` + template.HTMLEscapeString(agent.ProviderType) + `</dd>` +
		`<dt>Model</dt><dd>` + template.HTMLEscapeString(agent.Model) + `</dd>` +
		`<dt>Reasoning effort</dt><dd>` + template.HTMLEscapeString(agent.ReasoningEffort) + `</dd></dl>` +
		`<h2>Global instruction</h2><pre>` + template.HTMLEscapeString(agent.GlobalInstruction) + `</pre>`
	a.page(w, http.StatusOK, "Agent", body)
}
