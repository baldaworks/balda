package browseredge

import (
	"net/http"
	"net/url"
	"strings"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) pluginsPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Plugins(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/plugins/install") + `">Install plugin</a> · <a href="` + a.path("/plugin-marketplaces") + `">Marketplaces</a></p><ul>`
	for _, item := range inventory.Items {
		state := statusDisabled
		if item.Enabled {
			state = statusEnabled
		}
		if item.Removed {
			state = statusDeleted
		}
		body += `<li><a href="` + a.path("/plugins/") + url.PathEscape(item.Name) + `">` + escape(item.Name) + `</a> · ` + escape(item.Version) + ` · ` + state + `</li>`
	}
	a.page(w, http.StatusOK, "Plugins", body+`</ul>`)
}

func (a *App) pluginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	detail, err := service.Plugin(r.Context(), a.auth(r), r.PathValue("plugin_name"))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, detail.Item.Name, a.pluginDetail(r, detail))
}

func (a *App) pluginInstallPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	marketplace, name := r.URL.Query().Get("marketplace"), r.URL.Query().Get("plugin")
	if marketplace == "" {
		inventory, err := service.PluginMarketplaces(r.Context(), a.auth(r))
		if err != nil {
			a.serviceFailure(w, err, false)
			return
		}
		body := `<p><a href="` + a.path("/plugins") + `">Plugins</a></p><ul>`
		for _, item := range inventory.Items {
			body += `<li><a href="` + a.path("/plugins/install?marketplace=") + url.QueryEscape(item.Name) + `">` + escape(item.Name) + `</a></li>`
		}
		a.page(w, http.StatusOK, "Install plugin", body+`</ul>`)
		return
	}
	if name == "" {
		detail, err := service.PluginMarketplace(r.Context(), a.auth(r), marketplace)
		if err != nil {
			a.serviceFailure(w, err, false)
			return
		}
		body := `<p><a href="` + a.path("/plugins/install") + `">Install plugin</a></p><ul>`
		for _, candidate := range detail.Candidates {
			if candidate.CandidateToken != "" {
				body += `<li><a href="` + a.path("/plugins/install?marketplace=") + url.QueryEscape(marketplace) + `&amp;plugin=` + url.QueryEscape(candidate.Name) + `">` + escape(candidate.Name) + `</a> · ` + escape(candidate.Version) + `</li>`
			}
		}
		a.page(w, http.StatusOK, "Install plugin", body+`</ul>`)
		return
	}
	candidate, err := service.ReviewPlugin(r.Context(), a.auth(r), name, marketplace)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Review plugin", a.pluginCandidateForm(r, candidate, true))
}

func (a *App) pluginReviewPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	candidate, err := service.ReviewPlugin(r.Context(), a.auth(r), r.PathValue("plugin_name"), "")
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Review plugin upgrade", a.pluginCandidateForm(r, candidate, false))
}

func (a *App) installPlugin(w http.ResponseWriter, r *http.Request) {
	a.pluginMutation(w, r, true)
}

func (a *App) mutatePlugin(w http.ResponseWriter, r *http.Request) {
	a.pluginMutation(w, r, false)
}

func (a *App) pluginMutation(w http.ResponseWriter, r *http.Request, install bool) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("plugin_name")
	operation := managementv1.PluginOperation(form.Get("operation"))
	if install {
		name, operation = form.Get("plugin_name"), managementv1.PluginOperationInstall
	}
	request := managementv1.PluginMutationRequest{
		Operation: operation, MarketplaceName: form.Get("marketplace_name"), CandidateToken: form.Get("candidate_token"),
		Revision: form.Get("revision"), ExpectedState: form.Get("expected_state"), Confirm: form.Get("confirm") == formYesValue,
	}
	outcome, err := service.MutatePlugin(r.Context(), a.auth(r), name, request)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if outcome.Accepted && outcome.RecoveryPending {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if operation == managementv1.PluginOperationRemove {
		a.redirect(w, "/plugins")
		return
	}
	a.redirect(w, "/plugins/"+url.PathEscape(name))
}

func (a *App) pluginMarketplacesPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.PluginMarketplaces(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Plugin marketplaces", a.pluginMarketplaceList(inventory))
}

func (a *App) pluginMarketplaceList(inventory managementv1.PluginMarketplaceInventory) string {
	body := `<p><a href="` + a.path("/plugins") + `">Plugins</a> · <a href="` + a.path("/plugin-marketplaces/new") + `">Add marketplace</a></p><ul>`
	for _, item := range inventory.Items {
		body += `<li><a href="` + a.path("/plugin-marketplaces/") + url.PathEscape(item.Name) + `">` + escape(item.Name) + `</a> · ` + escape(item.LastAttemptStatus) + `</li>`
	}
	return body + `</ul>`
}

func (a *App) newPluginMarketplacePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	body := `<p><a href="` + a.path("/plugin-marketplaces") + `">Marketplaces</a></p><form method="post" action="` + a.path("/plugin-marketplaces") + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `">` + nameInputLabel + `<label>Source <input name="source" maxlength="2048" required></label><label>Ref <input name="ref" maxlength="512"></label><label>Sparse paths <textarea name="sparse"></textarea></label><button>Add</button></form>`
	a.page(w, http.StatusOK, "Add plugin marketplace", body)
}

func (a *App) addPluginMarketplace(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	request := managementv1.PluginMarketplaceAddRequest{Name: form.Get("name"), Source: form.Get("source"), Ref: form.Get("ref"), Sparse: nonemptyLines(form.Get("sparse"))}
	detail, err := service.AddPluginMarketplace(r.Context(), a.auth(r), request)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/plugin-marketplaces/"+url.PathEscape(detail.Item.Name))
}

func (a *App) pluginMarketplacePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	detail, err := service.PluginMarketplace(r.Context(), a.auth(r), r.PathValue("marketplace_name"))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, detail.Item.Name, a.pluginMarketplaceDetail(r, detail))
}

func (a *App) refreshPluginMarketplace(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("marketplace_name")
	if _, err := service.RefreshPluginMarketplace(r.Context(), a.auth(r), name, managementv1.PluginMarketplaceMutationRequest{ExpectedState: form.Get("expected_state")}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/plugin-marketplaces/"+url.PathEscape(name))
}

func (a *App) removePluginMarketplace(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	if form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(PluginService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("marketplace_name")
	if err := service.RemovePluginMarketplace(r.Context(), a.auth(r), name, managementv1.PluginMarketplaceMutationRequest{ExpectedState: form.Get("expected_state"), Confirm: true}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/plugin-marketplaces")
}

func (a *App) pluginCandidateForm(r *http.Request, candidate managementv1.PluginCandidate, install bool) string {
	action := a.path("/plugins/") + url.PathEscape(candidate.Name) + "/lifecycle"
	operation := managementv1.PluginOperationUpgrade
	marketplace := ""
	if install {
		action, operation = a.path("/plugins/install"), managementv1.PluginOperationInstall
		marketplace = candidate.Marketplace
	}
	return `<p><a href="` + a.path("/plugins") + `">Plugins</a></p><p><strong>` + escape(candidate.Name) + `</strong> ` + escape(candidate.Version) + ` · revision <code>` + escape(candidate.Revision) + `</code></p><form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="plugin_name" value="` + escape(candidate.Name) + `"><input type="hidden" name="operation" value="` + string(operation) + `"><input type="hidden" name="marketplace_name" value="` + escape(marketplace) + `"><input type="hidden" name="candidate_token" value="` + escape(candidate.CandidateToken) + `"><input type="hidden" name="expected_state" value="` + escape(candidate.ExpectedState) + `"><button>Accept</button></form>`
}

func (a *App) pluginDetail(r *http.Request, detail managementv1.PluginDetail) string {
	item := detail.Item
	action := a.path("/plugins/") + url.PathEscape(item.Name) + "/lifecycle"
	body := `<p><a href="` + a.path("/plugins") + `">Plugins</a></p><p>Version ` + escape(item.Version) + ` · revision <code>` + escape(item.Revision) + `</code></p>`
	if item.OriginAvailable && !item.RecoveryPending && !item.Removed {
		body += `<p><a href="` + a.path("/plugins/") + url.PathEscape(item.Name) + `/review">Review upgrade</a></p>`
	}
	for _, operation := range []managementv1.PluginOperation{managementv1.PluginOperationEnable, managementv1.PluginOperationDisable, managementv1.PluginOperationRemove} {
		confirm := ""
		if operation == managementv1.PluginOperationDisable || operation == managementv1.PluginOperationRemove {
			confirm = `<label><input type="checkbox" name="confirm" value="yes" required> Confirm</label>`
		}
		body += `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="operation" value="` + string(operation) + `"><input type="hidden" name="expected_state" value="` + escape(item.ExpectedState) + `">` + confirm + `<button>` + string(operation) + `</button></form>`
	}
	for _, revision := range detail.Revisions {
		if revision.Available && !revision.Current {
			body += `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="operation" value="rollback"><input type="hidden" name="revision" value="` + escape(revision.Revision) + `"><input type="hidden" name="expected_state" value="` + escape(item.ExpectedState) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm rollback to ` + escape(revision.Version) + `</label><button>rollback</button></form>`
		}
	}
	return body
}

func (a *App) pluginMarketplaceDetail(r *http.Request, detail managementv1.PluginMarketplaceDetail) string {
	item := detail.Item
	action := a.path("/plugin-marketplaces/") + url.PathEscape(item.Name)
	body := `<p><a href="` + a.path("/plugin-marketplaces") + `">Marketplaces</a></p><p>` + escape(item.Source) + ` · ` + escape(item.LastAttemptStatus) + `</p><form method="post" action="` + action + `/refresh"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_state" value="` + escape(item.ExpectedState) + `"><button>Refresh</button></form><form method="post" action="` + action + `/remove"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_state" value="` + escape(item.ExpectedState) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm removal</label><button>Remove</button></form><ul>`
	for _, candidate := range detail.Candidates {
		body += `<li>` + escape(candidate.Name) + ` · ` + escape(candidate.Version) + `</li>`
	}
	return body + `</ul>`
}

func nonemptyLines(raw string) []string {
	result := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}
