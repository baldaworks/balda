package backoffice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// HookOperations is Backoffice's consuming port for managed hook configuration.
type HookOperations interface {
	Inventory(ctx context.Context, authority hookcmd.Authority) (hookcmd.Record, error)
	Save(ctx context.Context, request hookcmd.Save) error
	Delete(ctx context.Context, request hookcmd.Delete) error
}

// ConfigureHookOperations binds the shared host owner before HTTP starts.
func (r *Runtime) ConfigureHookOperations(operations HookOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("hook operations must be configured before Backoffice start")
	}
	r.hooks = operations
	return nil
}

func hookAuthority(p security.Principal) hookcmd.Authority {
	return hookcmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion, SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
}

func (a *httpApp) hookBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Agent hooks · Balda", ViewerUsername: p.User.Username, CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationHooks, Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationHooks)}
}

func (a *httpApp) hooksPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.hookBasePage(r, p)
	if a.hooks == nil {
		a.hookError(w, r, page, hookcmd.ErrUnavailable)
		return
	}
	record, err := a.hooks.Inventory(r.Context(), hookAuthority(p))
	if err != nil {
		a.hookError(w, r, page, err)
		return
	}
	page.Hooks = webui.ProjectHooks(record)
	if capabilities, ok := a.hooks.(interface{ CapabilityDescription() string }); ok {
		page.Hooks.Capabilities = capabilities.CapabilityDescription()
	}
	name := r.PathValue("hook_name")
	if name != "" {
		index := slices.IndexFunc(record.Definitions, func(d hookcmd.Definition) bool { return d.Name == name })
		if index < 0 {
			a.hookError(w, r, page, hookcmd.ErrNotFound)
			return
		}
		page.Hooks.Editor = webui.ProjectHookEditor(record.Definitions[index], record.Version, false)
	} else if r.URL.Query().Get("new") == "1" {
		page.Hooks.Editor = webui.ProjectHookEditor(hookcmd.Definition{Event: hookcmd.UserPromptSubmit, TimeoutSeconds: 10}, record.Version, true)
	}
	a.render(w, r, http.StatusOK, webui.TemplateHooks, page)
}

const hookDeleteAction = "delete"

func (a *httpApp) hookMutation(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 64<<10)
	if !ok {
		return
	}
	page := a.hookBasePage(r, p)
	name := r.PathValue("hook_name")
	create := name == ""
	if create {
		name = form.Get("name")
	}
	version, versionErr := strconv.ParseInt(form.Get("expected_version"), 10, 64)
	timeout, timeoutErr := strconv.Atoi(form.Get("timeout_seconds"))
	definition := hookcmd.Definition{Name: name, Event: hookcmd.EventName(form.Get("event")), Enabled: form.Get("enabled") == checkedFormValue, Command: form.Get("command"), Provider: form.Get("provider"), Matcher: form.Get("matcher"), TimeoutSeconds: timeout}
	arguments, argsErr := parseHookArguments(form.Get("args"))
	definition.Args = arguments
	page.Hooks = webui.ProjectHooks(hookcmd.Record{})
	if capabilities, ok := a.hooks.(interface{ CapabilityDescription() string }); ok {
		page.Hooks.Capabilities = capabilities.CapabilityDescription()
	}
	page.Hooks.Editor = webui.ProjectHookEditor(definition, version, create)
	page.Hooks.Editor.ArgsJSON = form.Get("args")
	if versionErr != nil || version < 0 || !hookcmd.ValidName(name) {
		a.hookError(w, r, page, hookcmd.ErrInvalid)
		return
	}
	if a.hooks == nil {
		a.hookError(w, r, page, hookcmd.ErrUnavailable)
		return
	}
	var err error
	switch {
	case r.PathValue("hook_action") == hookDeleteAction:
		if form.Get("confirm") != checkedFormValue {
			err = hookcmd.ErrInvalid
		} else {
			err = a.hooks.Delete(r.Context(), hookcmd.Delete{Name: name, ExpectedVersion: version, Authority: hookAuthority(p)})
		}
	case timeoutErr != nil || argsErr != nil || definition.Validate() != nil:
		err = hookcmd.ErrInvalid
	default:
		err = a.hooks.Save(r.Context(), hookcmd.Save{Definition: definition, Create: create, ExpectedVersion: version, Authority: hookAuthority(p)})
	}
	if err != nil {
		a.hookError(w, r, page, err)
		return
	}
	if err := webui.RespondMutationPath(w, r, a.path("/hooks")); err != nil {
		a.hookError(w, r, page, hookcmd.ErrUnavailable)
	}
}

func (a *httpApp) hookDelete(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("hook_action", hookDeleteAction)
	a.hookMutation(w, r)
}

func (a *httpApp) hookError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := http.StatusServiceUnavailable, "The operation could not be confirmed. Reopen Agent hooks and check the saved configuration before retrying."
	switch {
	case errors.Is(err, hookcmd.ErrInvalid):
		status, message = http.StatusBadRequest, "Check the name, event, executable, JSON argument array, matcher and timeout (whole seconds, 0 for no hook deadline). Confirm deletion when removing a hook."
	case errors.Is(err, hookcmd.ErrForbidden):
		status, message = http.StatusForbidden, "Your administrator authority changed. Sign in again."
	case errors.Is(err, hookcmd.ErrConflict):
		status, message = http.StatusConflict, "Agent hooks changed. Reopen the hook and review its current configuration before saving."
	case errors.Is(err, hookcmd.ErrNotFound):
		status, message = http.StatusNotFound, "This hook is no longer available. Open Agent hooks."
	case errors.Is(err, hookcmd.ErrUnsupported):
		status, message = http.StatusBadRequest, "Native tool hooks require a Claude Code, Codex or OpenCode ACP provider. Every pool member must support the adapter. Review the configured provider before enabling this hook."
	}
	page.Error = &webui.ErrorView{Heading: "Agent hook operation could not complete", Message: message}
	name := webui.TemplateHooks
	if page.Hooks == nil {
		name = webui.TemplateError
		page.RestartURL, page.RestartLabel = a.path("/hooks"), "Open Agent hooks"
	}
	a.render(w, r, status, name, page)
}

func parseHookArguments(raw string) ([]string, error) {
	var values []any
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil || len(values) > 32 {
		return nil, hookcmd.ErrInvalid
	}
	args := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, hookcmd.ErrInvalid
		}
		args[i] = text
	}
	return args, nil
}
