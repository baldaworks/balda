package backoffice

import (
	"encoding/json"
	"net/http"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/go-webauthn/webauthn/protocol"
)

func (a *httpApp) renderMFACeremony(w http.ResponseWriter, r *http.Request, purpose usercmd.MFAPurpose, start security.CeremonyStart) {
	options, err := json.Marshal(start.Options)
	if err != nil {
		a.renderSecurityError(w, r, http.StatusInternalServerError)
		return
	}
	_, registration := start.Options.(*protocol.CredentialCreation)
	finish, cancel, title := "/auth/webauthn/finish", string(webui.LocationLogin), "Verify your passkey"
	switch purpose {
	case usercmd.MFAEnable:
		finish, cancel, title = "/account/2fa/enable/finish", string(webui.LocationAccount), "Register your passkey"
	case usercmd.MFAReplace:
		finish, cancel, title = "/account/2fa/replace/finish", string(webui.LocationAccount), "Replace your passkey"
	case usercmd.MFADisable:
		finish, cancel, title = "/account/2fa/disable/finish", string(webui.LocationAccount), "Disable two-factor authentication"
	case usercmd.MFAStepUp:
		finish, cancel = "/auth/step-up/finish", string(webui.LocationAccount)
	}
	// Ceremonies always cross a native document boundary and are excluded from HTMX history.
	r.Header.Del("HX-Request")
	a.render(w, r, http.StatusOK, webui.TemplateWebAuthn, webui.Page{Title: title + " · Balda", CSRFToken: r.FormValue("csrf_token"), ReturnTo: a.browser.SafeReturnPath(r.FormValue("return_to"), a.path(string(webui.LocationOverview))),
		Ceremony: &webui.MFACeremonyView{Registration: registration, OptionsJSON: string(options), Transaction: start.Transaction, FinishPath: finish, CancelPath: cancel, Confirm: purpose == usercmd.MFAReplace || purpose == usercmd.MFADisable}})
}

func (a *httpApp) stepUpPage(w http.ResponseWriter, r *http.Request) {
	p, ok := security.PrincipalFromContext(r.Context())
	if !ok || p.User.Role != usercmd.RoleAdministrator || !p.MFAEnabled {
		a.browser.WriteError(w, r, security.ErrForbidden)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateStepUp, webui.Page{Title: "Confirm your passkey · Balda", CSRFToken: a.browser.CSRFToken(r), ReturnTo: a.browser.SafeReturnPath(r.URL.Query().Get("return_to"), a.path(string(webui.LocationAccount)))})
}
