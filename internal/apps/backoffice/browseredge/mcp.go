package browseredge

import (
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const mcpProbeValue = "probe"
const selectedAttribute = " selected"
const mcpNone = "none"

func (a *App) mcpInventory(w http.ResponseWriter, r *http.Request) (MCPService, managementv1.MCPInventory, bool) {
	if _, ok := a.principal(w, r); !ok {
		return nil, managementv1.MCPInventory{}, false
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return nil, managementv1.MCPInventory{}, false
	}
	inventory, err := service.MCPConnections(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return nil, inventory, false
	}
	return service, inventory, true
}

func (a *App) mcpPage(w http.ResponseWriter, r *http.Request) {
	_, inventory, ok := a.mcpInventory(w, r)
	if !ok {
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/mcp/new") + `">Add MCP server</a></p><ul>`
	for _, item := range inventory.Items {
		body += `<li><a href="` + a.path("/mcp/") + url.PathEscape(item.ConnectionID) + `">` + escape(item.PublicID) + `</a> · ` + escape(string(item.Source)) + ` · ` + escape(string(item.Status)) + ` · recovery: ` + escape(mcpRecovery(item)) + `</li>`
	}
	a.page(w, http.StatusOK, "MCP servers", body+`</ul>`)
}
func (a *App) newMCPPage(w http.ResponseWriter, r *http.Request) {
	_, inventory, ok := a.mcpInventory(w, r)
	if !ok {
		return
	}
	a.page(w, http.StatusOK, "Add MCP server", a.mcpForm(r, inventory, managementv1.MCPItem{}, true))
}
func (a *App) mcpConnectionPage(w http.ResponseWriter, r *http.Request) {
	_, inventory, ok := a.mcpInventory(w, r)
	if !ok {
		return
	}
	item, found := mcpItem(inventory, r.PathValue("connection_id"))
	if !found {
		a.failure(w, http.StatusNotFound)
		return
	}
	body := `<p><a href="` + a.path("/mcp") + `">MCP servers</a></p>`
	if item.Source == managementv1.MCPItemSourceConfig || item.Deleted {
		body += `<p>This connection is read-only.</p>` + mcpSummary(item)
	} else {
		body += mcpSummary(item) + a.mcpForm(r, inventory, item, false)
	}
	a.page(w, http.StatusOK, item.PublicID, body)
}

func (a *App) createMCP(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	request, err := mcpCreateForm(form)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	var item managementv1.MCPItem
	if form.Get("operation") == mcpProbeValue {
		item, err = service.ProbeMCPConnection(r.Context(), a.auth(r), request)
	} else {
		item, err = service.CreateMCPConnection(r.Context(), a.auth(r), request)
	}
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if form.Get("operation") == mcpProbeValue {
		a.page(w, http.StatusOK, "MCP probe", "Probe succeeded: "+strconv.Itoa(int(item.ToolCount))+" tools. No definition was saved.")
		return
	}
	a.redirect(w, "/mcp/"+url.PathEscape(item.ConnectionID))
}
func (a *App) updateMCP(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	request, err := mcpUpdateForm(form)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	id := r.PathValue("connection_id")
	var item managementv1.MCPItem
	if form.Get("operation") == mcpProbeValue {
		item, err = service.ProbeMCPConnectionUpdate(r.Context(), a.auth(r), id, request)
	} else {
		item, err = service.UpdateMCPConnection(r.Context(), a.auth(r), id, request)
	}
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if form.Get("operation") == mcpProbeValue {
		a.page(w, http.StatusOK, "MCP probe", "Probe succeeded: "+strconv.Itoa(int(item.ToolCount))+" tools. No definition was saved.")
		return
	}
	a.redirect(w, "/mcp/"+url.PathEscape(id))
}
func (a *App) selectMCP(w http.ResponseWriter, r *http.Request) { a.changeMCP(w, r, false) }
func (a *App) deleteMCP(w http.ResponseWriter, r *http.Request) { a.changeMCP(w, r, true) }
func (a *App) changeMCP(w http.ResponseWriter, r *http.Request, remove bool) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(form.Get("expected_version"), 10, 64)
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("connection_id")
	request := managementv1.MCPSelectionRequest{ExpectedVersion: version, Enabled: form.Get("enabled") == formYesValue}
	if remove {
		_, err = service.DeleteMCPConnection(r.Context(), a.auth(r), id, request)
	} else {
		_, err = service.SelectMCPConnection(r.Context(), a.auth(r), id, request)
	}
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if remove {
		a.redirect(w, "/mcp")
	} else {
		a.redirect(w, "/mcp/"+url.PathEscape(id))
	}
}
func (a *App) diagnoseMCP(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.mutationForm(w, r); !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	result, err := service.DiagnoseMCPConnection(r.Context(), a.auth(r), r.PathValue("connection_id"))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "MCP diagnostic", escape(string(result.Outcome))+": "+strconv.Itoa(int(result.ToolCount))+" tools")
}

func (a *App) mcpForm(r *http.Request, inventory managementv1.MCPInventory, item managementv1.MCPItem, create bool) string {
	definition := item.Definition
	action := a.path("/mcp")
	if !create {
		action += "/" + url.PathEscape(item.ConnectionID)
	}
	body := `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `">`
	if create {
		body += `<label>ID <input name="public_id" required></label>`
	}
	body += `<label>Transport <select name="transport">`
	for _, v := range []string{"stdio", "http", "sse"} {
		selected := ""
		if string(definition.Transport) == v {
			selected = selectedAttribute
		}
		body += `<option` + selected + `>` + v + `</option>`
	}
	body += `</select></label><label>Command <input name="command" value="` + escape(stringValue(definition.Command)) + `"></label><label>Directory <input name="directory" value="` + escape(stringValue(definition.Directory)) + `"></label><label>URL <input name="url" value="` + escape(stringValue(definition.URL)) + `"></label><label>Arguments <textarea name="args">` + escape(joinStrings(definition.Args)) + `</textarea></label><label>Environment edits <textarea name="env" placeholder="KEY|kind|operation|value">` + escape(mcpEditRows(definition.Env)) + `</textarea></label><label>Header edits <textarea name="headers" placeholder="KEY|kind|operation|value">` + escape(mcpEditRows(definition.Headers)) + `</textarea></label><label><input type="checkbox" name="targets_all" value="yes"` + checked(definition.Targets.All) + `>All providers</label>`
	for _, provider := range inventory.ProviderIds {
		body += `<label><input type="checkbox" name="provider" value="` + escape(provider) + `"` + checked(contains(definition.Targets.Providers, provider)) + `>` + escape(provider) + `</label>`
	}
	if !create && item.Enabled {
		body += enabledHiddenInput
	}
	body += `<fieldset><legend>Worker authorization</legend><label>Scopes <textarea name="scopes"></textarea></label><label>Client ID <input name="client_id"></label><label>Client auth <select name="client_auth_method"><option>none</option><option>client_secret_basic</option><option>client_secret_post</option></select></label><label>Client secret <input type="password" name="client_secret" autocomplete="new-password"></label></fieldset>`
	body += `<button name="operation" value="save">Save</button><button name="operation" value="probe">Probe</button>`
	if create {
		body += `<button formaction="` + a.path("/mcp/authorization/browser/onboard") + `">Save and authorize in browser</button><button formaction="` + a.path("/mcp/authorization/device/onboard") + `">Save and authorize with device</button>`
	} else {
		body += `<button formaction="` + action + `/authorization/browser">Authorize in browser</button><button formaction="` + action + `/authorization/device">Authorize with device</button>`
	}
	body += `</form>`
	if !create {
		enable := ""
		if !item.Enabled {
			enable = enabledHiddenInput
		}
		body += `<form method="post" action="` + action + `/diagnostic"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><button>Diagnose</button></form><form method="post" action="` + action + `/selection"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `">` + enable + `<input type="hidden" name="confirm" value="yes"><button>` + map[bool]string{true: "Disable", false: "Enable"}[item.Enabled] + `</button></form><form method="post" action="` + action + `/delete"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(item.Version, 10) + `"><label><input type="checkbox" name="confirm" value="yes" required>Confirm deletion</label><button>Delete</button></form>`
		body += `<form method="post" action="` + action + `/authorization/disconnect"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="confirm" value="yes"><button>Disconnect authorization</button></form><form method="post" action="` + action + `/authorization/retry"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_revision_id" value="` + escape(item.CurrentRevisionID) + `"><button>Retry authorization</button></form>`
		if item.ActiveAttempt != nil {
			body += `<p>Active attempt: ` + escape(item.ActiveAttempt.AttemptID) + `</p>`
			if item.ActiveAttempt.Device {
				body += `<p><a href="` + a.path("/mcp/authorization/device/") + url.PathEscape(item.ActiveAttempt.AttemptID) + `">Check authorization</a></p>`
			}
			body += `<form method="post" action="` + a.path("/mcp/authorization/attempts/") + url.PathEscape(item.ActiveAttempt.AttemptID) + `/cancel"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><button>Cancel authorization</button></form>`
		}
	}
	return body
}

func mcpCreateForm(form url.Values) (managementv1.MCPCreateRequest, error) {
	definition, env, headers, err := mcpDefinitionForm(form)
	return managementv1.MCPCreateRequest{PublicID: strings.TrimSpace(form.Get("public_id")), Definition: definition, Env: &env, Headers: &headers, Enabled: true}, err
}
func mcpUpdateForm(form url.Values) (managementv1.MCPUpdateRequest, error) {
	version, err := strconv.ParseUint(form.Get("expected_version"), 10, 64)
	if err != nil || version == 0 {
		return managementv1.MCPUpdateRequest{}, errInvalidProvider
	}
	definition, env, headers, err := mcpDefinitionForm(form)
	return managementv1.MCPUpdateRequest{ExpectedVersion: version, Definition: definition, Env: &env, Headers: &headers, Enabled: form.Get("enabled") == formYesValue}, err
}
func mcpDefinitionForm(form url.Values) (managementv1.MCPDefinition, managementv1.MCPValueEdits, managementv1.MCPValueEdits, error) {
	env, err := mcpValueEdits(form.Get("env"))
	if err != nil {
		return managementv1.MCPDefinition{}, nil, nil, err
	}
	headers, err := mcpValueEdits(form.Get("headers"))
	definition := managementv1.MCPDefinition{Transport: managementv1.MCPDefinitionTransport(form.Get("transport")), Command: stringPointer(strings.TrimSpace(form.Get("command"))), Directory: stringPointer(strings.TrimSpace(form.Get("directory"))), URL: stringPointer(strings.TrimSpace(form.Get("url"))), Args: stringSlicePointer(providerEnvironmentLines(form.Get("args"))), Targets: managementv1.MCPTargets{All: form.Get("targets_all") == formYesValue, Providers: form["provider"]}}
	return definition, env, headers, err
}
func mcpValueEdits(raw string) (managementv1.MCPValueEdits, error) {
	result := managementv1.MCPValueEdits{}
	for _, line := range providerEnvironmentLines(raw) {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 || parts[0] == "" {
			return nil, errInvalidProvider
		}
		key, err := url.QueryUnescape(parts[0])
		if err != nil || key == "" {
			return nil, errInvalidProvider
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errInvalidProvider
		}
		value := parts[3]
		edit := managementv1.MCPValueEdit{Kind: managementv1.MCPValueEditKind(parts[1]), Operation: managementv1.MCPValueEditOperation(parts[2])}
		if edit.Operation == managementv1.MCPValueEditOperationSet {
			edit.Value = &value
		} else if value != "" {
			return nil, errInvalidProvider
		}
		result[key] = edit
	}
	return result, nil
}
func mcpItem(inventory managementv1.MCPInventory, id string) (managementv1.MCPItem, bool) {
	for _, item := range inventory.Items {
		if item.ConnectionID == id {
			return item, true
		}
	}
	return managementv1.MCPItem{}, false
}
func mcpSummary(item managementv1.MCPItem) string {
	diagnostic := mcpNone
	if item.LastDiagnostic != nil {
		diagnostic = string(item.LastDiagnostic.Outcome) + " / " + strconv.Itoa(int(item.LastDiagnostic.ToolCount)) + " tools"
	}
	return `<dl><dt>Transport</dt><dd>` + template.HTMLEscapeString(string(item.Definition.Transport)) + `</dd><dt>Status</dt><dd>` + escape(string(item.Status)) + `</dd><dt>Tools</dt><dd>` + strconv.Itoa(int(item.ToolCount)) + `</dd><dt>Publication</dt><dd>` + strconv.FormatUint(item.PublishedVersion, 10) + ` / ` + strconv.FormatUint(item.Version, 10) + `</dd><dt>Recovery</dt><dd>` + escape(mcpRecovery(item)) + `</dd><dt>Authorization</dt><dd>` + escape(item.Authorization) + `</dd><dt>Last diagnostic</dt><dd>` + escape(diagnostic) + `</dd></dl>` + mcpDefinitionSummary(item.Definition)
}
func mcpRecovery(item managementv1.MCPItem) string {
	if item.Recovery == nil {
		return mcpNone
	}
	return string(*item.Recovery)
}
func mcpEditRows(values *map[string]managementv1.MCPValueBinding) string {
	if values == nil {
		return ""
	}
	keys := make([]string, 0, len(*values))
	for key := range *values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	rows := make([]string, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, url.QueryEscape(key)+"|"+string((*values)[key].Kind)+"|keep|")
	}
	return strings.Join(rows, "\n")
}
func mcpDefinitionSummary(d managementv1.MCPDefinition) string {
	bindings := func(values *map[string]managementv1.MCPValueBinding) string {
		if values == nil {
			return mcpNone
		}
		keys := make([]string, 0, len(*values))
		for key, binding := range *values {
			keys = append(keys, key+" ("+string(binding.Kind)+")")
		}
		slices.Sort(keys)
		return strings.Join(keys, ", ")
	}
	return `<dl><dt>Command</dt><dd>` + escape(stringValue(d.Command)) + `</dd><dt>Directory</dt><dd>` + escape(stringValue(d.Directory)) + `</dd><dt>URL</dt><dd>` + escape(stringValue(d.URL)) + `</dd><dt>Targets</dt><dd>` + escape(strings.Join(d.Targets.Providers, ", ")) + `</dd><dt>Environment</dt><dd>` + escape(bindings(d.Env)) + `</dd><dt>Headers</dt><dd>` + escape(bindings(d.Headers)) + `</dd></dl>`
}
func checked(value bool) string {
	if value {
		return " checked"
	}
	return ""
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
