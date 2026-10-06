package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type mcpCallbackKey struct{}

const (
	mcpOAuthAuthorizedPending = "authorized_pending"
	mcpOAuthInvalid           = "invalid"
	mcpOAuthForbidden         = "forbidden"
	mcpOAuthDenied            = "denied"
	mcpOAuthConflict          = "conflict"
	mcpOAuthEnded             = "ended"
	mcpOAuthUnavailable       = "unavailable"
)

// MCPAuthorizations consumes host-owned transient worker authorization flows.
// Protocol, grant installation and definition binding remain with mcpmanage.
type MCPAuthorizations interface {
	CreateAndBeginBrowser(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BrowserAuthorization, error)
	CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error)
	BeginBrowser(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.BrowserAuthorization, error)
	CompleteBrowser(ctx context.Context, callback mcpcmd.BrowserCallback) (mcpcmd.Item, error)
	BeginDevice(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error)
	Device(ctx context.Context, id string, authority mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error)
	CurrentAttempt(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error)
	Cancel(ctx context.Context, id string, authority mcpcmd.Authority) error
	Disconnect(ctx context.Context, connectionID string, authority mcpcmd.Authority) error
	RetryAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error)
}

// ConfigureMCPAuthorizations wires native authorization before HTTP starts.
func (r *Runtime) ConfigureMCPAuthorizations(operations MCPAuthorizations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("MCP authorizations must be configured before Backoffice start")
	}
	r.mcpAuthorizations = operations
	return nil
}

func (a *httpApp) mcpAuthorizationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+a.path("/mcp/connections/oauth/browser"), a.mcpCreateAndBeginBrowser)
	mux.HandleFunc("POST "+a.path("/mcp/connections/oauth/device"), a.mcpCreateAndBeginDevice)
	mux.HandleFunc("POST "+a.path("/mcp/connections/{connection_id}/oauth/browser"), a.mcpBeginBrowser)
	mux.HandleFunc("POST "+a.path("/mcp/connections/{connection_id}/oauth/device"), a.mcpBeginDevice)
	for _, route := range []string{"browser", "device"} {
		mux.Handle("GET "+a.path("/mcp/connections/{connection_id}/oauth/"+route), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, a.path("/mcp/connections/"+url.PathEscape(r.PathValue("connection_id"))), http.StatusSeeOther)
		}))))
	}
	mux.HandleFunc("GET "+a.path("/mcp/oauth/callback"), a.mcpCallback)
	mux.HandleFunc("GET "+a.path("/mcp/oauth/return"), a.mcpOAuthReturn)
	mux.Handle("GET "+a.path("/mcp/oauth/device/{attempt_id}"), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.mcpDeviceStatus))))
	mux.HandleFunc("POST "+a.path("/mcp/oauth/attempts/{attempt_id}/cancel"), a.mcpCancelAuthorization)
	mux.HandleFunc("POST "+a.path("/mcp/connections/{connection_id}/oauth/disconnect"), a.mcpDisconnectAuthorization)
	mux.HandleFunc("POST "+a.path("/mcp/connections/{connection_id}/oauth/retry"), a.mcpRetryAuthorization)
}

func (a *httpApp) mcpAuthority(p security.Principal) mcpcmd.Authority {
	return mcpcmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion, SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC(), FreshProofAge: a.security.FreshProofAge()}
}

// OAuth instructions and redirects must be native; even a forged HTMX request
// cannot inject protocol URLs or write-only instructions into a cached fragment.
func (a *httpApp) mcpNativeMutation(w http.ResponseWriter, r *http.Request) (url.Values, webui.Page, mcpcmd.Authority, bool) {
	return a.mcpNativeMutationLimit(w, r, 0)
}

func (a *httpApp) mcpNativeMutationLimit(w http.ResponseWriter, r *http.Request, limit int64) (url.Values, webui.Page, mcpcmd.Authority, bool) {
	var form url.Values
	var p security.Principal
	var ok bool
	if limit > 0 {
		form, p, ok = a.browser.AdministratorMutationLimit(w, r, limit)
	} else {
		form, p, ok = a.browser.AdministratorMutation(w, r)
	}
	if !ok {
		return nil, webui.Page{}, mcpcmd.Authority{}, false
	}
	page := a.mcpBasePage(r, p)
	if r.Header.Get("HX-Request") != "" || r.Header.Get("HX-History-Restore-Request") != "" {
		a.mcpAuthorizationError(w, r, page, mcpcmd.ErrInvalid)
		return nil, page, mcpcmd.Authority{}, false
	}
	if a.mcpAuthorizations == nil || a.mcp == nil {
		a.mcpAuthorizationError(w, r, page, mcpcmd.ErrUnavailable)
		return nil, page, mcpcmd.Authority{}, false
	}
	return form, page, a.mcpAuthority(p), true
}

func mcpBeginRequest(form url.Values, id string, authority mcpcmd.Authority) mcpcmd.BeginAuthorization {
	request := mcpcmd.BeginAuthorization{ConnectionID: id, ClientID: form.Get("client_id"), ClientAuthMethod: form.Get("client_auth_method"), ClientSecret: form.Get("client_secret"), Authority: authority}
	if form.Has("scopes") {
		request.Scopes = append([]string{}, mcpLines(form.Get("scopes"))...)
	}
	return request
}

func (a *httpApp) mcpOnboardingRequest(w http.ResponseWriter, r *http.Request) (mcpcmd.CreateDefinition, mcpcmd.BeginAuthorization, webui.Page, bool) {
	form, page, authority, ok := a.mcpNativeMutationLimit(w, r, 1<<20)
	if !ok {
		return mcpcmd.CreateDefinition{}, mcpcmd.BeginAuthorization{}, page, false
	}
	definition, values, err := parseMCPDefinition(form)
	if err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return mcpcmd.CreateDefinition{}, mcpcmd.BeginAuthorization{}, page, false
	}
	creation := mcpcmd.CreateDefinition{PublicID: form.Get("public_id"), Definition: definition, Values: values, Enabled: true, Authority: authority}
	return creation, mcpBeginRequest(form, "", authority), page, true
}

func (a *httpApp) mcpCreateAndBeginBrowser(w http.ResponseWriter, r *http.Request) {
	creation, request, page, ok := a.mcpOnboardingRequest(w, r)
	if !ok {
		return
	}
	item, started, err := a.mcpAuthorizations.CreateAndBeginBrowser(r.Context(), creation, request)
	if err != nil {
		a.mcpOnboardingError(w, r, page, item, err)
		return
	}
	if err := a.browser.PreserveOAuthCallbackCredential(w, r, started.ExpiresAt); err != nil {
		a.mcpOnboardingError(w, r, page, item, mcpcmd.ErrUnavailable)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, started.AuthorizationURL, http.StatusSeeOther)
}

func (a *httpApp) mcpCreateAndBeginDevice(w http.ResponseWriter, r *http.Request) {
	creation, request, page, ok := a.mcpOnboardingRequest(w, r)
	if !ok {
		return
	}
	item, started, err := a.mcpAuthorizations.CreateAndBeginDevice(r.Context(), creation, request)
	if err != nil {
		a.mcpOnboardingError(w, r, page, item, err)
		return
	}
	page.Title = "Authorize connection · MCP · Balda"
	page.MCP = &webui.MCPView{Device: webui.ProjectMCPDevice(started, true)}
	a.render(w, r, http.StatusOK, webui.TemplateMCP, page)
}

func (a *httpApp) mcpOnboardingError(w http.ResponseWriter, r *http.Request, page webui.Page, item mcpcmd.Item, err error) {
	if item.Connection.ID == "" {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	status, message := mcpAuthorizationFailure(err)
	page.Error = &webui.ErrorView{Heading: "Connection saved; OAuth could not start", Message: message + " Open the saved connection to retry authorization."}
	page.RestartURL, page.RestartLabel = a.path("/mcp/connections/"+url.PathEscape(item.Connection.ID)), mcpSavedConnectionLabel
	a.render(w, r, status, webui.TemplateError, page)
}
func (a *httpApp) mcpBeginBrowser(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.mcpNativeMutation(w, r)
	if !ok {
		return
	}
	started, err := a.mcpAuthorizations.BeginBrowser(r.Context(), mcpBeginRequest(form, r.PathValue("connection_id"), authority))
	if err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	if err := a.browser.PreserveOAuthCallbackCredential(w, r, started.ExpiresAt); err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, started.AuthorizationURL, http.StatusSeeOther)
}
func (a *httpApp) mcpBeginDevice(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.mcpNativeMutation(w, r)
	if !ok {
		return
	}
	started, err := a.mcpAuthorizations.BeginDevice(r.Context(), mcpBeginRequest(form, r.PathValue("connection_id"), authority))
	if err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	page.Title = "Authorize worker · MCP · Balda"
	page.MCP = &webui.MCPView{Device: webui.ProjectMCPDevice(started, true)}
	a.render(w, r, http.StatusOK, webui.TemplateMCP, page)
}

func (a *httpApp) mcpCallback(w http.ResponseWriter, r *http.Request) {
	r = a.browser.RestoreOAuthCallbackCredential(w, r)
	// Preserve protocol input privately, then give every browser guard/error
	// responder a metadata-only return path. Never replay a callback after refresh.
	query, parseErr := url.ParseQuery(r.URL.RawQuery)
	if len(r.URL.RawQuery) > 16<<10 || r.Header.Get("HX-Request") != "" || r.Header.Get("HX-History-Restore-Request") != "" {
		parseErr = mcpcmd.ErrInvalid
	}
	for _, key := range []string{"state", "code", "iss", "error"} {
		if len(query[key]) > 1 {
			parseErr = mcpcmd.ErrInvalid
		}
	}
	safe := r.Clone(context.WithValue(r.Context(), mcpCallbackKey{}, true))
	safeURL := *r.URL
	safeURL.Path, safeURL.RawPath, safeURL.RawQuery = a.path("/mcp"), "", ""
	safe.URL = &safeURL
	safe.RequestURI = safeURL.RequestURI()
	safe.Header = r.Header.Clone()
	safe.Header.Del("HX-Request")
	safe.Header.Del("HX-Target")
	safe.Header.Del("HX-History-Restore-Request")
	w.Header().Set("Referrer-Policy", "no-referrer")
	guarded := a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := security.PrincipalFromContext(r.Context())
		if parseErr != nil || query.Get("state") == "" || (query.Get("code") == "" && query.Get("error") == "") {
			a.mcpCallbackResult(w, r, mcpcmd.Item{}, mcpcmd.ErrInvalid)
			return
		}
		if a.mcpAuthorizations == nil {
			a.mcpCallbackResult(w, r, mcpcmd.Item{}, mcpcmd.ErrUnavailable)
			return
		}
		item, err := a.mcpAuthorizations.CompleteBrowser(r.Context(), mcpcmd.BrowserCallback{State: query.Get("state"), Code: query.Get("code"), Issuer: query.Get("iss"), Denied: query.Get("error") != "", Authority: a.mcpAuthority(p)})
		if err != nil {
			a.mcpCallbackResult(w, r, item, err)
			return
		}

		http.Redirect(w, r, a.path("/mcp/oauth/return"), http.StatusSeeOther)
	})))
	guarded.ServeHTTP(w, safe)
}

// Committing this native document ends the foreign redirect chain. The explicit
// same-origin navigation then sends normal Strict cookies, including without JS.
func (a *httpApp) mcpOAuthReturn(w http.ResponseWriter, r *http.Request) {
	location := a.path("/mcp")
	switch result := r.URL.Query().Get("oauth_result"); result {
	case mcpOAuthAuthorizedPending, mcpOAuthInvalid, mcpOAuthForbidden, mcpOAuthDenied, mcpOAuthConflict, mcpOAuthEnded, mcpOAuthUnavailable:
		location += "?oauth_result=" + result
	}
	native := r.Clone(r.Context())
	native.Header.Del("HX-Request")
	native.Header.Del("HX-Target")
	native.Header.Del("HX-History-Restore-Request")
	w.Header().Set("Referrer-Policy", "no-referrer")
	a.render(w, native, http.StatusOK, webui.TemplateOAuthReturn, webui.Page{Title: "Continue to MCP · Balda", RestartURL: location})
}

func (a *httpApp) mcpDeviceStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.mcpBasePage(r, p)
	if a.mcpAuthorizations == nil {
		a.mcpAuthorizationError(w, r, page, mcpcmd.ErrUnavailable)
		return
	}
	device, err := a.mcpAuthorizations.Device(r.Context(), r.PathValue("attempt_id"), a.mcpAuthority(p))
	if err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	page.Title = "Worker authorization status · MCP · Balda"
	page.MCP = &webui.MCPView{Device: webui.ProjectMCPDevice(device, false)}
	a.render(w, r, http.StatusOK, webui.TemplateMCP, page)
}
func (a *httpApp) mcpCancelAuthorization(w http.ResponseWriter, r *http.Request) {
	_, page, authority, ok := a.mcpNativeMutation(w, r)
	if !ok {
		return
	}
	if err := a.mcpAuthorizations.Cancel(r.Context(), r.PathValue("attempt_id"), authority); err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	http.Redirect(w, r, a.path("/mcp"), http.StatusSeeOther)
}
func (a *httpApp) mcpDisconnectAuthorization(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.mcpNativeMutation(w, r)
	if !ok {
		return
	}
	if form.Get("confirm") != checkedFormValue {
		a.mcpAuthorizationError(w, r, page, mcpcmd.ErrInvalid)
		return
	}
	if err := a.mcpAuthorizations.Disconnect(r.Context(), r.PathValue("connection_id"), authority); err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	http.Redirect(w, r, a.path("/mcp/connections/"+url.PathEscape(r.PathValue("connection_id"))), http.StatusSeeOther)
}
func (a *httpApp) mcpRetryAuthorization(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.mcpNativeMutation(w, r)
	if !ok {
		return
	}
	expected := form.Get("expected_revision_id")
	if strings.TrimSpace(expected) == "" {
		a.mcpAuthorizationError(w, r, page, mcpcmd.ErrInvalid)
		return
	}
	item, err := a.mcpAuthorizations.RetryAuthorization(r.Context(), mcpcmd.SelectAuthorization{ConnectionID: r.PathValue("connection_id"), ExpectedRevisionID: expected, Authority: authority})
	if err != nil {
		a.mcpAuthorizationError(w, r, page, err)
		return
	}
	http.Redirect(w, r, a.path("/mcp/connections/"+url.PathEscape(item.Connection.ID)), http.StatusSeeOther)
}
func (a *httpApp) mcpAuthorizationError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := mcpAuthorizationFailure(err)
	page.Error = &webui.ErrorView{Heading: "OAuth could not complete", Message: message}
	page.RestartURL, page.RestartLabel = a.path("/mcp"), "Return to MCP"
	if id := r.PathValue("connection_id"); id != "" {
		page.RestartURL, page.RestartLabel = a.path("/mcp/connections/"+url.PathEscape(id)), "Open connection"
	}
	name := webui.TemplateMCP
	if page.MCP == nil {
		name = webui.TemplateError
	}
	a.render(w, r, status, name, page)
}

func mcpAuthorizationFailure(err error) (int, string) {
	status, message := http.StatusServiceUnavailable, "Authorization could not complete. Open the connection and start again. Use browser authorization if the service does not support device authorization."
	switch {
	case errors.Is(err, mcpcmd.ErrInvalid):
		status, message = 400, "Check the client settings and scopes, then start again. A static Authorization header cannot be combined with OAuth. Replacement client secrets are not retained."
	case errors.Is(err, mcpcmd.ErrForbidden):
		status, message = 403, "Your administrator authority changed. Confirm your current session and start again."
	case errors.Is(err, mcpcmd.ErrNotFound), errors.Is(err, mcpcmd.ErrAuthAttempt):
		status, message = 404, "This authorization attempt is no longer available. It may have ended or the host restarted. Reopen MCP and start again."
	case errors.Is(err, mcpcmd.ErrConflict):
		status, message = 409, "The connection or authorization changed. Open its current state before retrying. If it has a static Authorization header, remove that header from the definition before starting OAuth."
	case errors.Is(err, mcpcmd.ErrAuthRequired):
		status, message = 403, "Authorization was denied, expired or no longer valid. Open the connection and start a new attempt."
	case errors.Is(err, mcpcmd.ErrCredentials):
		message = "Protected worker credentials are unavailable. Check the host credential configuration before starting again."
	}
	return status, message
}

// Redirect every callback outcome to metadata before rendering. A failed callback
// must not leave its code/state in the browser location or a history entry.
func (a *httpApp) mcpCallbackResult(w http.ResponseWriter, r *http.Request, item mcpcmd.Item, err error) {
	result := mcpOAuthUnavailable
	switch {
	case item.Authorization == mcpcmd.GrantAuthorized:
		result = mcpOAuthAuthorizedPending
	case errors.Is(err, mcpcmd.ErrInvalid):
		result = mcpOAuthInvalid
	case errors.Is(err, mcpcmd.ErrForbidden):
		result = mcpOAuthForbidden
	case errors.Is(err, mcpcmd.ErrAuthRequired):
		result = mcpOAuthDenied
	case errors.Is(err, mcpcmd.ErrConflict):
		result = mcpOAuthConflict
	case errors.Is(err, mcpcmd.ErrNotFound), errors.Is(err, mcpcmd.ErrAuthAttempt):
		result = mcpOAuthEnded
	}
	location := a.path("/mcp/oauth/return")
	http.Redirect(w, r, location+"?oauth_result="+result, http.StatusSeeOther)
}
