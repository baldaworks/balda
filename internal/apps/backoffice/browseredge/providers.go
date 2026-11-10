package browseredge

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) providersPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ProviderService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Providers(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/providers/new") + `">Add provider</a></p><ul>`
	for _, item := range inventory.Items {
		state := "disabled"
		if item.Deleted {
			state = "deleted"
		} else if item.Enabled {
			state = "enabled"
		}
		body += `<li><a href="` + a.path("/providers/") + url.PathEscape(item.ID) + `">` + template.HTMLEscapeString(item.ID) + `</a> · ` +
			template.HTMLEscapeString(item.DriverType) + ` · ` + template.HTMLEscapeString(string(item.Source)) + ` · ` + state + `</li>`
	}
	a.page(w, http.StatusOK, "Providers", body+`</ul>`)
}

func (a *App) newProviderPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ProviderService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Providers(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Add provider", a.providerForm(r, inventory, managementv1.ProviderItem{}, true))
}

func (a *App) providerPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ProviderService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Providers(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	item, ok := providerItem(inventory, r.PathValue("provider_id"))
	if !ok {
		a.failure(w, http.StatusNotFound)
		return
	}
	body := a.providerForm(r, inventory, item, false)
	if item.Source == managementv1.ProviderSourceConfigured || item.Deleted {
		body = `<p>This provider is read-only.</p>` + providerSummary(item)
	}
	a.page(w, http.StatusOK, item.ID, `<p><a href="`+a.path("/providers")+`">Providers</a></p>`+body)
}

func (a *App) createProvider(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	request, err := providerCreateRequest(form)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, available := a.service.(ProviderService)
	if !available {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.CreateProvider(r.Context(), a.auth(r), request); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/providers/"+url.PathEscape(request.ID))
}

func (a *App) updateProvider(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	request, err := providerUpdateRequest(form)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	id := r.PathValue("provider_id")
	service, available := a.service.(ProviderService)
	if !available {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.UpdateProvider(r.Context(), a.auth(r), id, request); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/providers/"+url.PathEscape(id))
}

func (a *App) selectProvider(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := providerVersion(form.Get("expected_version"))
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	id := r.PathValue("provider_id")
	request := managementv1.ProviderSelectionRequest{ExpectedVersion: version, Enabled: form.Get("enabled") == formYesValue}
	service, available := a.service.(ProviderService)
	if !available {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.SelectProvider(r.Context(), a.auth(r), id, request); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/providers/"+url.PathEscape(id))
}

func (a *App) deleteProvider(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := providerVersion(form.Get("expected_version"))
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	id := r.PathValue("provider_id")
	service, available := a.service.(ProviderService)
	if !available {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.DeleteProvider(r.Context(), a.auth(r), id, managementv1.ProviderDeleteRequest{ExpectedVersion: version}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/providers/"+url.PathEscape(id))
}

func (a *App) providerForm(r *http.Request, inventory managementv1.ProviderInventory, item managementv1.ProviderItem, create bool) string {
	config := item.Config
	if create && config.Type == "" && len(inventory.Types) > 0 {
		config.Type = inventory.Types[0]
	}
	action := a.path("/providers")
	if !create {
		action += "/" + url.PathEscape(item.ID)
	}
	body := `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(inventory.Version, 10) + `">`
	if create {
		body += `<label>ID <input name="id" maxlength="128" required></label>`
	}
	body += `<label>Type <select name="type">`
	for _, providerType := range inventory.Types {
		selected := ""
		if providerType == config.Type {
			selected = ` selected`
		}
		body += `<option value="` + escape(providerType) + `"` + selected + `>` + escape(providerType) + `</option>`
	}
	body += `</select></label>` + providerTextInput("model", "Model", stringValue(config.Model)) + providerTextInput("timeout", "Timeout", stringValue(config.Timeout)) +
		providerTextInput("reasoning_effort", "Reasoning effort", stringValue(config.ReasoningEffort)) + providerTextInput("model_config_id", "Model config ID", stringValue(config.ModelConfigID)) +
		providerTextInput("reasoning_effort_config_id", "Reasoning config ID", stringValue(config.ReasoningEffortConfigID)) + providerTextInput("mode", "Mode", stringValue(config.Mode)) +
		providerTextInput("bridge_version", "Bridge version", stringValue(config.BridgeVersion)) + providerTextInput("registry_id", "Registry ID", stringValue(config.RegistryID))
	body += `<label>Inherit environment <select name="inherit_env"><option value="default">default</option><option value="yes"` + selectedBool(config.InheritEnv, true) + `>yes</option><option value="no"` + selectedBool(config.InheritEnv, false) + `>no</option></select></label>`
	body += providerTextarea("extra_args", "Extra arguments", joinStrings(config.ExtraArgs)) + providerTextarea("pool_members", "Pool members", joinStrings(config.PoolMembers)) +
		providerTextarea("mcp_servers", "MCP servers", joinStrings(config.McpServers)) + providerTextarea("system_instructions", "System instructions", stringValue(config.SystemInstructions))
	body += `<fieldset><legend>API key</legend><select name="api_key_action"><option value="keep">keep</option><option value="replace">replace</option><option value="clear">clear</option></select><input type="password" name="api_key" autocomplete="new-password"></fieldset>`
	body += `<fieldset><legend>Environment</legend><p>Existing names: ` + escape(strings.Join(item.EnvironmentNames, ", ")) + `</p><select name="environment_action"><option value="keep">keep</option><option value="replace">replace</option><option value="clear">clear</option></select><textarea name="environment" placeholder="NAME=value"></textarea></fieldset><button>Save</button></form>`
	if !create {
		enabled := ""
		if !item.Enabled {
			enabled = enabledHiddenInput
		}
		body += `<form method="post" action="` + action + `/selection"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(inventory.Version, 10) + `"><input type="hidden" name="confirm" value="yes">` + enabled + `<button>` + map[bool]string{true: "Disable", false: "Enable"}[item.Enabled] + `</button></form>`
		body += `<form method="post" action="` + action + `/delete"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(inventory.Version, 10) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm deletion</label><button>Delete</button></form>`
	}
	return body
}

func providerCreateRequest(form url.Values) (managementv1.ProviderCreateRequest, error) {
	version, err := providerVersion(form.Get("expected_version"))
	if err != nil || strings.TrimSpace(form.Get("id")) == "" {
		return managementv1.ProviderCreateRequest{}, errInvalidProvider
	}
	config, protected, err := providerFormValues(form)
	return managementv1.ProviderCreateRequest{ExpectedVersion: version, ID: strings.TrimSpace(form.Get("id")), Config: config, Protected: protected}, err
}

func providerUpdateRequest(form url.Values) (managementv1.ProviderUpdateRequest, error) {
	version, err := providerVersion(form.Get("expected_version"))
	if err != nil {
		return managementv1.ProviderUpdateRequest{}, err
	}
	config, protected, err := providerFormValues(form)
	return managementv1.ProviderUpdateRequest{ExpectedVersion: version, Config: config, Protected: protected}, err
}

func providerFormValues(form url.Values) (managementv1.ProviderConfig, managementv1.ProviderProtectedEdit, error) {
	config := managementv1.ProviderConfig{Type: strings.TrimSpace(form.Get("type")), Model: stringPointer(strings.TrimSpace(form.Get("model"))), Timeout: stringPointer(strings.TrimSpace(form.Get("timeout"))),
		ReasoningEffort: stringPointer(strings.TrimSpace(form.Get("reasoning_effort"))), ModelConfigID: stringPointer(strings.TrimSpace(form.Get("model_config_id"))),
		ReasoningEffortConfigID: stringPointer(strings.TrimSpace(form.Get("reasoning_effort_config_id"))), Mode: stringPointer(strings.TrimSpace(form.Get("mode"))),
		BridgeVersion: stringPointer(strings.TrimSpace(form.Get("bridge_version"))), RegistryID: stringPointer(strings.TrimSpace(form.Get("registry_id"))),
		ExtraArgs: stringSlicePointer(providerLines(form.Get("extra_args"))), PoolMembers: stringSlicePointer(providerLines(form.Get("pool_members"))),
		McpServers: stringSlicePointer(providerLines(form.Get("mcp_servers"))), SystemInstructions: stringPointer(strings.TrimSpace(form.Get("system_instructions")))}
	switch form.Get("inherit_env") {
	case "default", "":
	case formYesValue:
		value := true
		config.InheritEnv = &value
	case "no":
		value := false
		config.InheritEnv = &value
	default:
		return config, managementv1.ProviderProtectedEdit{}, errInvalidProvider
	}
	protected := managementv1.ProviderProtectedEdit{APIKey: managementv1.ProviderSecretEdit{Operation: managementv1.ProviderEditOperation(form.Get("api_key_action"))}, Environment: managementv1.ProviderEnvironmentEdit{Operation: managementv1.ProviderEditOperation(form.Get("environment_action"))}}
	if protected.APIKey.Operation == "" {
		protected.APIKey.Operation = managementv1.ProviderEditOperationKeep
	}
	if protected.Environment.Operation == "" {
		protected.Environment.Operation = managementv1.ProviderEditOperationKeep
	}
	if protected.APIKey.Operation == managementv1.ProviderEditOperationReplace {
		value := form.Get("api_key")
		protected.APIKey.Value = &value
	}
	if protected.Environment.Operation == managementv1.ProviderEditOperationReplace {
		values, err := providerEnvironment(form.Get("environment"))
		if err != nil {
			return config, protected, err
		}
		protected.Environment.Values = &values
	}
	return config, protected, nil
}

var errInvalidProvider = &providerFormError{}

type providerFormError struct{}

func (*providerFormError) Error() string { return "invalid provider form" }

func providerVersion(raw string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
}

func providerEnvironment(raw string) (map[string]string, error) {
	values := make(map[string]string)
	for _, line := range providerEnvironmentLines(raw) {
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, errInvalidProvider
		}
		values[name] = value
	}
	return values, nil
}

func providerEnvironmentLines(raw string) []string {
	parts := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func providerLines(raw string) []string {
	parts := strings.FieldsFunc(strings.ReplaceAll(raw, "\r\n", "\n"), func(r rune) bool { return r == '\n' || r == ',' })
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func providerItem(inventory managementv1.ProviderInventory, id string) (managementv1.ProviderItem, bool) {
	for _, item := range inventory.Items {
		if item.ID == id {
			return item, true
		}
	}
	return managementv1.ProviderItem{}, false
}

func providerSummary(item managementv1.ProviderItem) string {
	return `<dl><dt>Type</dt><dd>` + escape(item.DriverType) + `</dd><dt>Revision</dt><dd>` + escape(item.CurrentRevisionID) + `</dd><dt>Source</dt><dd>` + escape(string(item.Source)) + `</dd></dl>`
}

func providerTextInput(name, label, value string) string {
	return `<label>` + escape(label) + ` <input name="` + escape(name) + `" value="` + escape(value) + `"></label>`
}
func providerTextarea(name, label, value string) string {
	return `<label>` + escape(label) + ` <textarea name="` + escape(name) + `">` + escape(value) + `</textarea></label>`
}
func escape(value string) string { return template.HTMLEscapeString(value) }
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func joinStrings(value *[]string) string {
	if value == nil {
		return ""
	}
	return strings.Join(*value, "\n")
}
func stringSlicePointer(value []string) *[]string {
	if len(value) == 0 {
		return nil
	}
	return &value
}
func selectedBool(value *bool, want bool) string {
	if value != nil && *value == want {
		return ` selected`
	}
	return ""
}
