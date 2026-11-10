package browseredge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

var hookEvents = []managementv1.HookEvent{
	managementv1.SessionStart,
	managementv1.UserPromptSubmit,
	managementv1.Stop,
	managementv1.SessionEnd,
	managementv1.PreToolUse,
	managementv1.PostToolUse,
}

func (a *App) hooksPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(HookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Hooks(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/hooks/new") + `">Add hook</a></p>`
	if inventory.Capabilities != "" {
		body += `<p>` + escape(inventory.Capabilities) + `</p>`
	}
	body += `<ul>`
	for _, definition := range inventory.Definitions {
		state := statusDisabled
		if definition.Enabled {
			state = statusEnabled
		}
		body += `<li><a href="` + a.path("/hooks/") + url.PathEscape(definition.Name) + `">` + escape(definition.Name) + `</a> · ` + escape(string(definition.Event)) + ` · ` + state + `</li>`
	}
	a.page(w, http.StatusOK, "Agent hooks", body+`</ul>`)
}

func (a *App) newHookPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(HookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Hooks(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	definition := managementv1.HookDefinition{Event: managementv1.UserPromptSubmit, Args: []string{}, TimeoutSeconds: 10}
	a.page(w, http.StatusOK, "Add agent hook", a.hookForm(r, definition, inventory.Version, true))
}

func (a *App) hookPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(HookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Hooks(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	definition, ok := findHook(inventory, r.PathValue("hook_name"))
	if !ok {
		a.failure(w, http.StatusNotFound)
		return
	}
	a.page(w, http.StatusOK, definition.Name, a.hookForm(r, definition, inventory.Version, false))
}

func (a *App) createHook(w http.ResponseWriter, r *http.Request) {
	a.saveHook(w, r, true)
}

func (a *App) updateHook(w http.ResponseWriter, r *http.Request) {
	a.saveHook(w, r, false)
}

func (a *App) saveHook(w http.ResponseWriter, r *http.Request, create bool) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(HookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("hook_name")
	if create {
		name = strings.TrimSpace(form.Get("name"))
	}
	request, err := hookSaveRequest(form, name)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	if create {
		err = service.CreateHook(r.Context(), a.auth(r), request)
	} else {
		err = service.UpdateHook(r.Context(), a.auth(r), name, request)
	}
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/hooks/"+url.PathEscape(name))
}

func (a *App) deleteHook(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version < 0 || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(HookService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.DeleteHook(r.Context(), a.auth(r), r.PathValue("hook_name"), managementv1.HookDeleteRequest{ExpectedVersion: version, Confirm: true}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/hooks")
}

func hookSaveRequest(form url.Values, name string) (managementv1.HookSaveRequest, error) {
	version, versionErr := strconv.ParseInt(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	timeout, timeoutErr := strconv.ParseInt(strings.TrimSpace(form.Get("timeout_seconds")), 10, 64)
	var args []string
	argsErr := json.Unmarshal([]byte(form.Get("args")), &args)
	if versionErr != nil || version < 0 || timeoutErr != nil || timeout < 0 || argsErr != nil || args == nil || name == "" {
		return managementv1.HookSaveRequest{}, errors.New("invalid hook form")
	}
	return managementv1.HookSaveRequest{
		ExpectedVersion: version,
		Definition: managementv1.HookDefinition{
			Name: name, Event: managementv1.HookEvent(form.Get("event")), Enabled: form.Get("enabled") == formYesValue,
			Command: form.Get("command"), Args: args, Provider: form.Get("provider"), Matcher: form.Get("matcher"), TimeoutSeconds: timeout,
		},
	}, nil
}

func (a *App) hookForm(r *http.Request, definition managementv1.HookDefinition, version int64, create bool) string {
	action := a.path("/hooks")
	if !create {
		action += "/" + url.PathEscape(definition.Name)
	}
	args, _ := json.Marshal(definition.Args)
	body := `<p><a href="` + a.path("/hooks") + `">Agent hooks</a></p><form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatInt(version, 10) + `">`
	if create {
		body += nameInputLabel
	} else {
		body += `<p><strong>` + escape(definition.Name) + `</strong></p>`
	}
	body += `<label>Event <select name="event">`
	for _, event := range hookEvents {
		selected := ""
		if event == definition.Event {
			selected = selectedAttribute
		}
		body += `<option value="` + escape(string(event)) + `"` + selected + `>` + escape(string(event)) + `</option>`
	}
	body += `</select></label><label>Command <input name="command" value="` + escape(definition.Command) + `" maxlength="4096" required></label><label>Arguments JSON <textarea name="args" required>` + escape(string(args)) + `</textarea></label><label>Provider <input name="provider" value="` + escape(definition.Provider) + `" maxlength="128"></label><label>Matcher <input name="matcher" value="` + escape(definition.Matcher) + `" maxlength="256"></label><label>Timeout seconds <input name="timeout_seconds" type="number" min="0" max="9223372036" value="` + strconv.FormatInt(definition.TimeoutSeconds, 10) + `" required></label><label><input type="checkbox" name="enabled" value="yes"`
	if definition.Enabled {
		body += ` checked`
	}
	body += `> Enabled</label><button>Save</button></form>`
	if !create {
		body += `<form method="post" action="` + action + `/delete"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatInt(version, 10) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm deletion</label><button>Delete</button></form>`
	}
	return body
}

func findHook(inventory managementv1.HookInventory, name string) (managementv1.HookDefinition, bool) {
	for _, definition := range inventory.Definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return managementv1.HookDefinition{}, false
}
