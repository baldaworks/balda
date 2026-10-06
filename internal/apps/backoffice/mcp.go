package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const mcpProbeOperation = "probe"
const mcpSavedConnectionLabel = "Open saved connection"

// MCPOperations is Backoffice's port to host-owned MCP management. Definition
// validation, durable authority fences and readiness policy remain with the host.
type MCPOperations interface {
	Inventory(ctx context.Context) ([]mcpcmd.Item, error)
	ProviderIDs(ctx context.Context) ([]string, error)
	Create(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error)
	Update(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error)
	SetEnabled(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error)
	Delete(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error)
	Probe(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error)
	ProbeUpdate(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error)
}

// ConfigureMCPOperations wires the host use case before the listener starts.
func (r *Runtime) ConfigureMCPOperations(operations MCPOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("MCP operations must be configured before Backoffice start")
	}
	r.mcp = operations
	return nil
}

func (a *httpApp) mcpPage(w http.ResponseWriter, r *http.Request) {
	principal, _ := security.PrincipalFromContext(r.Context())
	if a.mcp == nil {
		a.mcpError(w, r, a.mcpBasePage(r, principal), mcpcmd.ErrUnavailable)
		return
	}
	page, err := a.mcpView(r, principal)
	if err != nil {
		a.mcpError(w, r, page, err)
		return
	}

	if result := r.URL.Query().Get("oauth_result"); result != "" {
		err := mcpcmd.ErrUnavailable
		switch result {
		case mcpOAuthAuthorizedPending:
			if page.MCP.Editor != nil && page.MCP.Editor.Row.Authorization == "Authorized" {
				page.MCP.ProbeMessage = "Authorization was saved. Check tool availability on the connection; retry if needed."
			}
			a.render(w, r, http.StatusOK, webui.TemplateMCP, page)
			return
		case mcpOAuthInvalid:
			err = mcpcmd.ErrInvalid
		case mcpOAuthForbidden:
			err = mcpcmd.ErrForbidden
		case mcpOAuthDenied:
			err = mcpcmd.ErrAuthRequired
		case mcpOAuthConflict:
			err = mcpcmd.ErrConflict
		case mcpOAuthEnded:
			err = mcpcmd.ErrNotFound
		}
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateMCP, page)
}

func (a *httpApp) mcpBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "MCP servers · Balda", ViewerUsername: p.User.Username, CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationMCP, Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationMCP)}
}

func (a *httpApp) mcpView(r *http.Request, p security.Principal) (webui.Page, error) {
	page := a.mcpBasePage(r, p)
	create := r.URL.Path == a.path("/mcp/new") || (r.Method == http.MethodPost && r.PathValue("connection_id") == "")
	id := r.PathValue("connection_id")
	if create {
		providers, err := a.mcp.ProviderIDs(r.Context())
		if err != nil {
			return page, err
		}
		page.Title = "Add MCP server · Balda"
		page.MCP = &webui.MCPView{Editor: webui.ProjectMCPEditor(mcpcmd.Item{}, providers, true)}
		return page, nil
	}
	items, err := a.mcp.Inventory(r.Context())
	if err != nil {
		return page, err
	}
	if id == "" {
		page.MCP = &webui.MCPView{}
		for _, item := range items {
			page.MCP.Rows = append(page.MCP.Rows, webui.ProjectMCPRow(item))
		}
		return page, nil
	}
	for _, item := range items {
		if item.Connection.ID == id {
			providers, err := a.mcp.ProviderIDs(r.Context())
			if err != nil {
				return page, err
			}
			page.Title = item.Connection.PublicID + " · MCP · Balda"
			page.MCP = &webui.MCPView{Editor: webui.ProjectMCPEditor(item, providers, false)}
			if a.mcpAuthorizations != nil && page.MCP.Editor.AuthorizationAvailable {
				attempt, found, err := a.mcpAuthorizations.CurrentAttempt(r.Context(), id, a.mcpAuthority(p))
				if err != nil {
					return page, err
				}
				if found {
					page.MCP.Editor.Attempt = &webui.MCPAttempt{ID: attempt.ID, Device: attempt.Device, Expires: attempt.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), CancelPath: "/mcp/oauth/attempts/" + url.PathEscape(attempt.ID) + "/cancel", StatusPath: "/mcp/oauth/device/" + url.PathEscape(attempt.ID)}
				}
			}
			return page, nil
		}
	}
	return page, mcpcmd.ErrNotFound
}

func (a *httpApp) mcpMutation(w http.ResponseWriter, r *http.Request) (url.Values, webui.Page, mcpcmd.Authority, bool) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return nil, webui.Page{}, mcpcmd.Authority{}, false
	}
	if a.mcp == nil {
		a.mcpError(w, r, a.mcpBasePage(r, p), mcpcmd.ErrUnavailable)
		return nil, webui.Page{}, mcpcmd.Authority{}, false
	}
	page, err := a.mcpView(r, p)
	if err != nil {
		a.mcpError(w, r, page, err)
		return nil, page, mcpcmd.Authority{}, false
	}
	if page.MCP.Editor != nil && page.MCP.Editor.Row.Deleted {
		a.mcpError(w, r, page, mcpcmd.ErrConflict)
		return nil, page, mcpcmd.Authority{}, false
	}
	if page.MCP.Editor != nil && page.MCP.Editor.Row.ReadOnly {
		a.mcpError(w, r, page, mcpcmd.ErrForbidden)
		return nil, page, mcpcmd.Authority{}, false
	}
	authority := a.mcpAuthority(p)
	return form, page, authority, true
}

func (a *httpApp) mcpCreate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.mcpMutation(w, r)
	if !ok {
		return
	}
	definition, edits, err := parseMCPDefinition(form)
	if err != nil {
		a.mcpError(w, r, page, err)
		return
	}
	request := mcpcmd.CreateDefinition{PublicID: form.Get("public_id"), Definition: definition, Values: edits, Enabled: true, Authority: authority}
	var item mcpcmd.Item
	switch form.Get("operation") {
	case mcpProbeOperation:
		item, err = a.mcp.Probe(r.Context(), request)
	case "", "save":
		item, err = a.mcp.Create(r.Context(), request)
	default:
		err = mcpcmd.ErrInvalid
	}
	a.mcpResult(w, r, page, item, err, form.Get("operation") == mcpProbeOperation, false)
}

func (a *httpApp) mcpUpdate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.mcpMutation(w, r)
	if !ok {
		return
	}
	definition, edits, err := parseMCPDefinition(form)
	if err != nil {
		a.mcpError(w, r, page, err)
		return
	}
	version, err := mcpVersion(form)
	if err != nil {
		a.mcpError(w, r, page, err)
		return
	}
	request := mcpcmd.UpdateDefinition{ConnectionID: r.PathValue("connection_id"), ExpectedVersion: version, Definition: definition, Values: edits, Enabled: page.MCP.Editor.Enabled, Authority: authority}
	var item mcpcmd.Item
	switch form.Get("operation") {
	case mcpProbeOperation:
		item, err = a.mcp.ProbeUpdate(r.Context(), request)
	case "", "save":
		item, err = a.mcp.Update(r.Context(), request)
	default:
		err = mcpcmd.ErrInvalid
	}
	a.mcpResult(w, r, page, item, err, form.Get("operation") == mcpProbeOperation, false)
}

func (a *httpApp) mcpSelection(w http.ResponseWriter, r *http.Request) { a.mcpChange(w, r, false) }
func (a *httpApp) mcpDelete(w http.ResponseWriter, r *http.Request)    { a.mcpChange(w, r, true) }
func (a *httpApp) mcpChange(w http.ResponseWriter, r *http.Request, remove bool) {
	form, page, authority, ok := a.mcpMutation(w, r)
	if !ok {
		return
	}
	version, err := mcpVersion(form)
	if err != nil || form.Get("confirm") != checkedFormValue {
		a.mcpError(w, r, page, mcpcmd.ErrInvalid)
		return
	}
	request := mcpcmd.ChangeSelection{ConnectionID: r.PathValue("connection_id"), ExpectedVersion: version, Enabled: form.Get("enabled") == checkedFormValue, Authority: authority}
	var item mcpcmd.Item
	if remove {
		item, err = a.mcp.Delete(r.Context(), request)
	} else {
		item, err = a.mcp.SetEnabled(r.Context(), request)
	}
	a.mcpResult(w, r, page, item, err, false, remove)
}

func mcpVersion(form url.Values) (uint64, error) {
	version, err := strconv.ParseUint(form.Get("expected_version"), 10, 64)
	if err != nil || version == 0 {
		return 0, mcpcmd.ErrInvalid
	}
	return version, nil
}

func parseMCPDefinition(form url.Values) (mcpcmd.Definition, mcpcmd.ValueEdits, error) {
	definition := mcpcmd.Definition{Transport: mcpcmd.Transport(form.Get("transport")), Command: form.Get("command"), Directory: form.Get("directory"), Args: mcpLines(form.Get("args")), URL: form.Get("url"), Targets: mcpcmd.Targets{All: form.Get("targets_all") == checkedFormValue, Providers: form["provider"]}}
	env, err := parseMCPValues(form, "env")
	if err != nil {
		return definition, mcpcmd.ValueEdits{}, err
	}
	headers, err := parseMCPValues(form, "header")
	if err != nil {
		return definition, mcpcmd.ValueEdits{}, err
	}
	return definition, mcpcmd.ValueEdits{Env: env, Headers: headers}, nil
}

func mcpLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}
func parseMCPValues(form url.Values, prefix string) (map[string]mcpcmd.ValueEdit, error) {
	keys, kinds, operations, values := form[prefix+"_key"], form[prefix+"_kind"], form[prefix+"_operation"], form[prefix+"_value"]
	if len(keys) > 258 || len(kinds) != len(keys) || len(operations) != len(keys) || len(values) != len(keys) {
		return nil, mcpcmd.ErrInvalid
	}
	edits := make(map[string]mcpcmd.ValueEdit, len(keys))
	for i, key := range keys {
		if key == "" {
			if values[i] != "" {
				return nil, mcpcmd.ErrInvalid
			}
			continue
		}
		if _, duplicate := edits[key]; duplicate {
			return nil, mcpcmd.ErrInvalid
		}
		operation := mcpcmd.ValueOperation(operations[i])
		kind := mcpcmd.ValueKind(kinds[i])
		if operation != mcpcmd.ValueKeep && operation != mcpcmd.ValueSet && operation != mcpcmd.ValueRemove {
			return nil, mcpcmd.ErrInvalid
		}
		if kind != mcpcmd.ValueLiteral && kind != mcpcmd.ValueProtected && kind != mcpcmd.ValueEnvironment {
			return nil, mcpcmd.ErrInvalid
		}
		if operation != mcpcmd.ValueSet && values[i] != "" {
			return nil, mcpcmd.ErrInvalid
		}
		edits[key] = mcpcmd.ValueEdit{Operation: operation, Kind: kind, Value: values[i]}
	}
	return edits, nil
}

func (a *httpApp) mcpResult(w http.ResponseWriter, r *http.Request, page webui.Page, item mcpcmd.Item, err error, probe, remove bool) {
	if err != nil {
		if !probe && item.Connection.ID != "" {
			status, _ := mcpOperationFailure(err)
			page.Error = &webui.ErrorView{Heading: "Connection change saved", Message: "The change was saved, but its current state could not be read. Open the saved connection to check its state; do not repeat creation."}
			page.RestartURL, page.RestartLabel = a.path("/mcp/connections/"+url.PathEscape(item.Connection.ID)), mcpSavedConnectionLabel
			a.render(w, r, status, webui.TemplateError, page)
			return
		}
		a.mcpError(w, r, page, err)
		return
	}
	if probe {
		page.MCP.ProbeMessage = fmt.Sprintf("Candidate probe succeeded: %d tools discovered. No definition was saved; runtime readiness is unchanged.", item.ToolCount)
		a.render(w, r, http.StatusOK, webui.TemplateMCP, page)
		return
	}
	location := a.path("/mcp/connections/" + url.PathEscape(item.Connection.ID))
	if remove {
		location = a.path("/mcp")
	}
	if err := webui.RespondMutationPath(w, r, location); err != nil {
		a.mcpError(w, r, page, mcpcmd.ErrUnavailable)
	}
}

func (a *httpApp) mcpError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := mcpOperationFailure(err)
	page.Error = &webui.ErrorView{Heading: "MCP operation could not complete", Message: message}
	templateName := webui.TemplateMCP
	if page.MCP == nil {
		page.Title = "MCP operation unavailable · Balda"
		templateName = webui.TemplateError
	}
	a.render(w, r, status, templateName, page)
}

func mcpOperationFailure(err error) (int, string) {
	status, message := http.StatusServiceUnavailable, "The operation could not be completed. Reopen MCP management and try again."
	switch {
	case errors.Is(err, mcpcmd.ErrInvalid):
		status, message = http.StatusBadRequest, "Review the transport, provider targets and value operations. Enter replacement values again; inputs are not retained."
	case errors.Is(err, mcpcmd.ErrForbidden):
		status, message = http.StatusForbidden, "This connection is read-only or your administrator authority changed."
	case errors.Is(err, mcpcmd.ErrNotFound):
		status, message = http.StatusNotFound, "This connection is no longer available. Reopen MCP management."
	case errors.Is(err, mcpcmd.ErrConflict):
		status, message = http.StatusConflict, "The connection or administrator authority changed. Reopen the editor before trying again."
	case errors.Is(err, mcpcmd.ErrCredentials):
		message = "Protected credentials are unavailable. Check the host credential configuration and try again."
	case errors.Is(err, mcpcmd.ErrAuthRequired), errors.Is(err, mcpcmd.ErrDisconnected):
		message = "Worker authorization is required before this operation can complete."
	}
	return status, message
}
