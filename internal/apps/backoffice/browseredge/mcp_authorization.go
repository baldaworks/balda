package browseredge

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const oauthAccessCookie = "balda_mcp_oauth_access"

func authorizationInput(form url.Values) managementv1.MCPAuthorizationInput {
	secret := form.Get("client_secret")
	return managementv1.MCPAuthorizationInput{Scopes: providerEnvironmentLines(form.Get("scopes")), ClientID: form.Get("client_id"), ClientAuthMethod: managementv1.MCPAuthorizationInputClientAuthMethod(form.Get("client_auth_method")), ClientSecret: stringPointer(secret)}
}

func (a *App) onboardMCPBrowser(w http.ResponseWriter, r *http.Request) { a.onboardMCP(w, r, false) }
func (a *App) onboardMCPDevice(w http.ResponseWriter, r *http.Request)  { a.onboardMCP(w, r, true) }
func (a *App) onboardMCP(w http.ResponseWriter, r *http.Request, device bool) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	var callbackAccess string
	var accessExpiresAt time.Time
	if !device {
		callbackAccess, accessExpiresAt, ok = a.oauthCallbackAccess(w, r)
		if !ok {
			return
		}
	}
	creation, err := mcpCreateForm(form)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	request := managementv1.MCPAuthorizationOnboardRequest{Creation: creation, Authorization: authorizationInput(form)}
	if device {
		result, callErr := service.OnboardMCPDevice(r.Context(), a.auth(r), request)
		if callErr != nil {
			a.serviceFailure(w, callErr, false)
			return
		}
		if result.StartError != nil || result.Start == nil {
			a.savedAuthorizationFailure(w, result.Item.ConnectionID)
			return
		}
		a.deviceInstructions(w, *result.Start)
		return
	}
	result, callErr := service.OnboardMCPBrowser(r.Context(), a.auth(r), request)
	if callErr != nil {
		a.serviceFailure(w, callErr, false)
		return
	}
	if result.StartError != nil || result.Start == nil {
		a.savedAuthorizationFailure(w, result.Item.ConnectionID)
		return
	}
	a.redirectToAuthorization(w, r, *result.Start, callbackAccess, accessExpiresAt)
}

func (a *App) beginMCPBrowser(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	access, accessExpiresAt, ok := a.oauthCallbackAccess(w, r)
	if !ok {
		return
	}
	started, err := service.BeginMCPBrowser(r.Context(), a.auth(r), r.PathValue("connection_id"), authorizationInput(form))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirectToAuthorization(w, r, started, access, accessExpiresAt)
}
func (a *App) beginMCPDevice(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	started, err := service.BeginMCPDevice(r.Context(), a.auth(r), r.PathValue("connection_id"), authorizationInput(form))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.deviceInstructions(w, started)
}

func (a *App) oauthCallbackAccess(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	access := cookieValue(r, accessCookie)
	if access == "" {
		a.failure(w, http.StatusUnauthorized)
		return "", time.Time{}, false
	}
	principal, err := a.service.Principal(r.Context(), managementclient.Auth{Access: access})
	if err != nil {
		a.serviceFailure(w, err, false)
		return "", time.Time{}, false
	}
	if principal.AccessExpiresAt.IsZero() || !principal.AccessExpiresAt.After(a.now()) {
		a.failure(w, http.StatusUnauthorized)
		return "", time.Time{}, false
	}
	return access, principal.AccessExpiresAt, true
}

func (a *App) redirectToAuthorization(w http.ResponseWriter, r *http.Request, started managementv1.MCPBrowserAuthorizationStart, access string, accessExpiresAt time.Time) {
	expires := accessExpiresAt
	if started.ExpiresAt.Before(expires) {
		expires = started.ExpiresAt
	}
	remaining := expires.Sub(a.now())
	if remaining <= 0 {
		a.failure(w, http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthAccessCookie, Value: access, Path: a.path("/mcp/authorization/callback"), Expires: expires, MaxAge: max(1, int(remaining.Seconds())), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, started.AuthorizationURL, http.StatusSeeOther)
}
func (a *App) deviceInstructions(w http.ResponseWriter, started managementv1.MCPDeviceAuthorizationStart) {
	body := `<p>Open <a rel="noreferrer" href="` + escape(started.VerificationURI) + `">` + escape(started.VerificationURI) + `</a> and enter <strong>` + escape(started.UserCode) + `</strong>.</p><p><a href="` + a.path("/mcp/authorization/device/") + url.PathEscape(started.AttemptID) + `">Check status</a></p>`
	a.page(w, http.StatusOK, "Authorize MCP worker", body)
}

func (a *App) completeMCPBrowser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	cookie, err := r.Cookie(oauthAccessCookie)
	a.clearOAuthCookie(w)
	if err != nil || cookie.Value == "" {
		a.redirectAuthorizationResult(w, r, "invalid")
		return
	}
	if len(r.URL.RawQuery) > 16<<10 {
		a.redirectAuthorizationResult(w, r, "invalid")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["iss"]) > 1 || len(query["error"]) > 1 || query.Get("state") == "" || (query.Get("code") == "" && query.Get("error") == "") {
		a.redirectAuthorizationResult(w, r, "invalid")
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.redirectAuthorizationResult(w, r, "failed")
		return
	}
	_, err = service.CompleteMCPBrowser(r.Context(), managementclient.Auth{Access: cookie.Value}, managementv1.MCPBrowserCallback{State: stringPointer(query.Get("state")), Code: stringPointer(query.Get("code")), Issuer: query.Get("iss"), Denied: query.Get("error") != ""})
	if err != nil {
		a.redirectAuthorizationResult(w, r, "failed")
		return
	}
	a.redirectAuthorizationResult(w, r, "completed")
}

func (a *App) redirectAuthorizationResult(w http.ResponseWriter, r *http.Request, result string) {
	http.Redirect(w, r, a.path("/mcp/authorization/return")+"?result="+url.QueryEscape(result), http.StatusSeeOther)
}
func (a *App) clearOAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oauthAccessCookie, Path: a.path("/mcp/authorization/callback"), Expires: time.Unix(1, 0), MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}
func (a *App) mcpAuthorizationReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	message := "Authorization failed. Start a new attempt from the MCP connection page."
	if r.URL.Query().Get("result") == "completed" {
		message = "Authorization completed."
	}
	a.page(w, http.StatusOK, "MCP authorization", `<p>`+escape(message)+`</p><p><a href="`+a.path("/mcp")+`">Continue to MCP</a></p>`)
}

func (a *App) mcpDeviceStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	status, err := service.MCPDeviceStatus(r.Context(), a.auth(r), r.PathValue("attempt_id"))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "MCP authorization status", `<p>Status: `+escape(string(status.Status))+`</p><p><a href="`+a.path("/mcp/")+url.PathEscape(status.ConnectionID)+`">Back to connection</a></p>`)
}
func (a *App) cancelMCPAuthorization(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.mutationForm(w, r); !ok {
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.CancelMCPAuthorization(r.Context(), a.auth(r), r.PathValue("attempt_id")); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/mcp")
}
func (a *App) disconnectMCPAuthorization(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	if form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.DisconnectMCPAuthorization(r.Context(), a.auth(r), r.PathValue("connection_id")); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/mcp/"+url.PathEscape(r.PathValue("connection_id")))
}
func (a *App) retryMCPAuthorization(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	revision := strings.TrimSpace(form.Get("expected_revision_id"))
	if revision == "" {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(MCPService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	item, err := service.RetryMCPAuthorization(r.Context(), a.auth(r), r.PathValue("connection_id"), managementv1.MCPRetryAuthorizationRequest{ExpectedRevisionID: revision})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/mcp/"+url.PathEscape(item.ConnectionID))
}
func (a *App) savedAuthorizationFailure(w http.ResponseWriter, id string) {
	a.page(w, http.StatusServiceUnavailable, "Connection saved", `<p>The connection was saved, but authorization could not start.</p><p><a href="`+a.path("/mcp/")+url.PathEscape(id)+`">Open saved connection</a></p>`)
}
