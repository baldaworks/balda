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
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/google/uuid"
)

// ProvidersOperations is Backoffice's consuming port for provider management.
type ProvidersOperations interface {
	Inventory(ctx context.Context) (providers.Inventory, error)
	Create(ctx context.Context, change providers.Change) (providers.State, error)
	Update(ctx context.Context, change providers.Change) (providers.State, error)
	SetEnabled(ctx context.Context, expected providers.StateVersion, id string, enabled bool, audit usercmd.AuditEvent) (providers.State, error)
	Delete(ctx context.Context, expected providers.StateVersion, id string, audit usercmd.AuditEvent) (providers.State, error)
}

const protectedActionReplace = "replace"

func (r *Runtime) ConfigureProvidersOperations(operations ProvidersOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("provider operations must be configured before Backoffice start")
	}
	r.providers = operations
	return nil
}

func (a *httpApp) providersPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page, err := a.providersView(r, p)
	if err != nil {
		a.providerError(w, r, page, err)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateProviders, page)
}

func (a *httpApp) providerBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Providers · Balda", ViewerUsername: p.User.Username,
		CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationProviders,
		Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationProviders)}
}

func (a *httpApp) providersView(r *http.Request, p security.Principal) (webui.Page, error) {
	page := a.providerBasePage(r, p)
	if a.providers == nil {
		return page, providers.ErrUnavailable
	}
	inventory, err := a.providers.Inventory(r.Context())
	if err != nil {
		return page, err
	}
	view := &webui.ProvidersView{Version: uint64(inventory.Version)}
	for _, item := range inventory.Items {
		view.Rows = append(view.Rows, projectProviderRow(item, inventory.Version))
	}
	id := r.PathValue("provider_id")
	if id != "" {
		item, ok := providerInventoryItem(inventory, id)
		if !ok {
			return page, providers.ErrUnavailable
		}
		page.Title = item.Record.ID + " · Providers · Balda"
		view.Editor = projectProviderEditor(item, inventory.Version, false)
		page.Providers = view
		return page, nil
	}
	if r.Method == http.MethodPost || r.URL.Query().Get("new") == "1" {
		page.Title = "Add provider · Balda"
		view.Editor = projectProviderEditor(providers.Item{}, inventory.Version, true)
	}
	page.Providers = view
	return page, nil
}

func providerInventoryItem(inventory providers.Inventory, id string) (providers.Item, bool) {
	for _, item := range inventory.Items {
		if item.Record.ID == id {
			return item, true
		}
	}
	return providers.Item{}, false
}

func (a *httpApp) providerMutation(w http.ResponseWriter, r *http.Request) (url.Values, webui.Page, security.Principal, bool) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return nil, webui.Page{}, security.Principal{}, false
	}
	page, err := a.providersView(r, p)
	if err != nil {
		a.providerError(w, r, page, err)
		return nil, page, security.Principal{}, false
	}
	return form, page, p, true
}

func (a *httpApp) providerCreate(w http.ResponseWriter, r *http.Request) {
	form, page, principal, ok := a.providerMutation(w, r)
	if !ok {
		return
	}
	change, err := providerChange(form, nil, providerAudit(principal))
	if err == nil {
		_, err = a.providers.Create(r.Context(), change)
	}
	a.providerResult(w, r, page, change.ID, err)
}

func (a *httpApp) providerUpdate(w http.ResponseWriter, r *http.Request) {
	form, page, principal, ok := a.providerMutation(w, r)
	if !ok {
		return
	}
	if page.Providers == nil || page.Providers.Editor == nil || page.Providers.Editor.Row.ReadOnly {
		a.providerError(w, r, page, providers.ErrInvalid)
		return
	}
	item := page.Providers.Editor
	change, err := providerChange(form, item, providerAudit(principal))
	change.ID = r.PathValue("provider_id")
	if err == nil {
		_, err = a.providers.Update(r.Context(), change)
	}
	a.providerResult(w, r, page, change.ID, err)
}

func (a *httpApp) providerSelection(w http.ResponseWriter, r *http.Request) {
	form, page, principal, ok := a.providerMutation(w, r)
	if !ok {
		return
	}
	version, err := providerVersion(form)
	if err == nil && form.Get("confirm") != checkedFormValue {
		err = providers.ErrInvalid
	}
	enabled := form.Get("enabled") == checkedFormValue
	if err == nil {
		_, err = a.providers.SetEnabled(r.Context(), version, r.PathValue("provider_id"), enabled, providerAudit(principal))
	}
	a.providerResult(w, r, page, r.PathValue("provider_id"), err)
}

func (a *httpApp) providerDelete(w http.ResponseWriter, r *http.Request) {
	form, page, principal, ok := a.providerMutation(w, r)
	if !ok {
		return
	}
	version, err := providerVersion(form)
	if err == nil && form.Get("confirm") != checkedFormValue {
		err = providers.ErrInvalid
	}
	if err == nil {
		_, err = a.providers.Delete(r.Context(), version, r.PathValue("provider_id"), providerAudit(principal))
	}
	a.providerResult(w, r, page, r.PathValue("provider_id"), err)
}

func providerVersion(form url.Values) (providers.StateVersion, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil {
		return 0, providers.ErrInvalid
	}
	return providers.StateVersion(value), nil
}

func providerChange(form url.Values, current *webui.ProviderEditor, audit usercmd.AuditEvent) (providers.Change, error) {
	version, err := providerVersion(form)
	if err != nil {
		return providers.Change{}, err
	}
	id := strings.TrimSpace(form.Get("id"))
	if current != nil {
		id = current.Row.ID
	}
	cfg, err := providerConfig(form)
	if err != nil {
		return providers.Change{ExpectedVersion: version, ID: id, Audit: audit}, err
	}
	protected, err := providerProtectedEdit(form, current)
	if err != nil {
		return providers.Change{ExpectedVersion: version, ID: id, Config: cfg, Audit: audit}, err
	}
	return providers.Change{ExpectedVersion: version, ID: id, Config: cfg, Protected: protected, Audit: audit}, nil
}

func providerConfig(form url.Values) (agentconfig.Config, error) {
	typeName := strings.TrimSpace(form.Get("type"))
	cfg := agentconfig.Config{Type: typeName, MCPServers: providerList(form.Get("mcp_servers")), SystemInstructions: strings.TrimSpace(form.Get("system_instructions"))}
	switch typeName {
	case agentconfig.AgentTypeOpenAI, agentconfig.AgentTypeAIStudio:
		block := &agentconfig.LocalAPIConfig{Model: strings.TrimSpace(form.Get("model")), Timeout: strings.TrimSpace(form.Get("timeout")), ReasoningEffort: strings.TrimSpace(form.Get("reasoning_effort"))}
		if typeName == agentconfig.AgentTypeOpenAI {
			cfg.OpenAI = block
		} else {
			cfg.AIStudio = block
		}
	case agentconfig.AgentTypePool:
		cfg.PoolConfig = &agentconfig.PoolConfig{Members: providerList(form.Get("pool_members"))}
	default:
		descriptor, ok := agentconfig.BuiltinDriverRegistry().Lookup(typeName)
		if !ok || descriptor.Class != agentconfig.DriverClassACP || descriptor.Type == agentconfig.AgentTypeGenericACP || descriptor.Deprecated {
			return agentconfig.Config{}, providers.ErrInvalid
		}
		block := &agentconfig.ACPConfig{ExtraArgs: providerList(form.Get("extra_args")), Model: strings.TrimSpace(form.Get("model")),
			ModelConfigID: strings.TrimSpace(form.Get("model_config_id")), ReasoningEffort: strings.TrimSpace(form.Get("reasoning_effort")),
			ReasoningEffortConfigID: strings.TrimSpace(form.Get("reasoning_effort_config_id")), Mode: strings.TrimSpace(form.Get("mode")),
			BridgeVersion: strings.TrimSpace(form.Get("bridge_version")), RegistryID: strings.TrimSpace(form.Get("registry_id"))}
		switch form.Get("inherit_env") {
		case "yes":
			value := true
			block.InheritEnv = &value
		case "no":
			value := false
			block.InheritEnv = &value
		case "default", "":
		default:
			return agentconfig.Config{}, providers.ErrInvalid
		}
		setProviderACPBlock(&cfg, descriptor.Type, block)
	}
	return cfg, nil
}

func setProviderACPBlock(cfg *agentconfig.Config, typeName string, block *agentconfig.ACPConfig) {
	switch typeName {
	case agentconfig.AgentTypeGeminiACP:
		cfg.GeminiACP = block
	case agentconfig.AgentTypeCodexACP:
		cfg.CodexACP = block
	case agentconfig.AgentTypeOpenCodeACP:
		cfg.OpenCodeACP = block
	case agentconfig.AgentTypeCopilotACP:
		cfg.CopilotACP = block
	case agentconfig.AgentTypeClaudeCodeACP:
		cfg.ClaudeCodeACP = block
	case agentconfig.AgentTypeGrokACP:
		cfg.GrokACP = block
	case agentconfig.AgentTypeRegistryACP:
		cfg.RegistryACP = block
	case agentconfig.AgentTypeAgyACP:
		cfg.AgyACP = block
	}
}

func providerProtectedEdit(form url.Values, current *webui.ProviderEditor) (providers.ProtectedEdit, error) {
	edit := providers.ProtectedEdit{}
	switch form.Get("api_key_action") {
	case "", "keep":
	case protectedActionReplace:
		value := form.Get("api_key")
		edit.APIKey = &value
	case "clear":
		value := ""
		edit.APIKey = &value
	default:
		return providers.ProtectedEdit{}, providers.ErrInvalid
	}
	switch form.Get("environment_action") {
	case "", "keep":
	case "clear", protectedActionReplace:
		edit.Environment = make(map[string]*string)
		if current != nil {
			for _, name := range current.EnvironmentNames {
				edit.Environment[name] = nil
			}
		}
		if form.Get("environment_action") == protectedActionReplace {
			for _, line := range strings.Split(strings.ReplaceAll(form.Get("environment"), "\r\n", "\n"), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				name, value, ok := strings.Cut(line, "=")
				name = strings.TrimSpace(name)
				if !ok || name == "" {
					return providers.ProtectedEdit{}, providers.ErrInvalid
				}
				copyValue := value
				edit.Environment[name] = &copyValue
			}
		}
	default:
		return providers.ProtectedEdit{}, providers.ErrInvalid
	}
	return edit, nil
}

func providerList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
}

func providerAudit(p security.Principal) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: uuid.NewString(), ActorUserID: p.User.ID, ActorSessionID: p.FamilyID,
		Source: "backoffice", OccurredAt: time.Now().UTC()}
}

func (a *httpApp) providerResult(w http.ResponseWriter, r *http.Request, page webui.Page, id string, err error) {
	if err != nil {
		a.providerError(w, r, page, err)
		return
	}
	location := "/providers"
	if id != "" {
		location += "/" + url.PathEscape(id)
	}
	if err := webui.RespondMutationPath(w, r, a.path(location)); err != nil {
		a.providerError(w, r, page, providers.ErrUnavailable)
	}
}

func (a *httpApp) providerError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := providerOperationFailure(err)
	page.Error = &webui.ErrorView{Heading: "Provider operation could not complete", Message: message}
	templateName := webui.TemplateProviders
	if page.Providers == nil {
		page.Title = "Providers unavailable · Balda"
		templateName = webui.TemplateError
	}
	a.render(w, r, status, templateName, page)
}

func providerOperationFailure(err error) (int, string) {
	switch {
	case errors.Is(err, providers.ErrInvalid):
		return http.StatusBadRequest, "Review the provider type, public settings, pool members, and protected-value action."
	case errors.Is(err, providers.ErrConflict):
		return http.StatusConflict, "Provider state changed. Reopen Providers before trying again."
	case errors.Is(err, providers.ErrUnavailable):
		return http.StatusServiceUnavailable, "The provider definition or required protected values are unavailable. Reopen Providers and check the current state."
	default:
		return http.StatusServiceUnavailable, "Provider management is unavailable. Reopen Providers after the service recovers."
	}
}
