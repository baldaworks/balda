package backoffice

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/access"
	"github.com/baldaworks/balda/internal/apps/backoffice/audit"
	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const checkedFormValue = "yes"
const sessionPageSize = 20

type httpApp struct {
	renderer        *webui.Renderer
	browser         *security.Browser
	security        *security.Service
	access          *access.Service
	auditLog        *audit.Service
	cards           []webui.CapabilityCard
	bindingChoices  []string
	invitations     BindingInvitations
	bindingChannels BindingChannels
	qa              bool
	basePath        string
}

func newHTTPApp(store usercmd.Store, config ResolvedConfig) (*httpApp, error) {
	renderer, err := webui.NewRenderer(config.Server.BasePath)
	if err != nil {
		return nil, err
	}
	service, err := security.NewService(store, security.Config{
		AccessTTL: config.Server.AccessTokenTTL, RefreshTTL: config.Server.RefreshTokenTTL,
		Origin: config.Server.PublicURL, CeremonyTTL: config.Server.CeremonyTTL, StepUpTTL: config.Server.StepUpTTL,
	})
	if err != nil {
		return nil, err
	}
	bindingChoices := configuredBindingChannels(config.Balda)
	app := &httpApp{renderer: renderer, security: service, access: access.NewService(store), auditLog: audit.NewService(store), cards: ProjectCapabilityCards(config.Balda), bindingChoices: bindingChoices, qa: config.Server.QAUI, basePath: config.Server.BasePath}
	browser, err := security.NewBrowser(service, security.HTTPConfig{
		TrustedOrigin: config.Server.PublicURL, SecureCookies: config.Server.SecureCookies, BasePath: config.Server.BasePath,
		ErrorHandler:      app.renderSecurityError,
		StepUpResponder:   app.renderStepUpRequired,
		CeremonyResponder: app.renderMFACeremony,
		MutationResponder: func(w http.ResponseWriter, r *http.Request, location string) error {
			return webui.RespondMutationAt(w, r, webui.Location(strings.TrimPrefix(location, config.Server.BasePath)), config.Server.BasePath)
		},
	})
	if err != nil {
		return nil, err
	}
	app.browser = browser
	return app, nil
}

func (a *httpApp) handler() (http.Handler, error) {
	assets, err := webui.Assets()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET "+a.path("/assets/"), http.StripPrefix(a.basePath, assets))
	mux.Handle("GET "+a.path("/healthz"), http.StripPrefix(a.basePath, healthHandler()))
	mux.HandleFunc("GET "+a.path("/"), func(w http.ResponseWriter, r *http.Request) {
		if a.basePath != "" && r.URL.Path != a.path("/") {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, a.path(string(webui.LocationOverview)), http.StatusSeeOther)
	})
	mux.HandleFunc("GET "+a.path("/login"), a.loginPage)
	mux.HandleFunc("POST "+a.path("/login"), a.browser.Login)
	mux.HandleFunc("POST "+a.path("/auth/webauthn/finish"), a.browser.FinishLogin)
	mux.HandleFunc("POST "+a.path("/account/2fa/enable/start"), a.browser.BeginEnable)
	mux.HandleFunc("POST "+a.path("/account/2fa/enable/finish"), a.browser.FinishEnable)
	mux.HandleFunc("POST "+a.path("/account/2fa/replace/start"), a.browser.BeginReplace)
	mux.HandleFunc("POST "+a.path("/account/2fa/replace/finish"), a.browser.FinishReplace)
	mux.HandleFunc("POST "+a.path("/account/2fa/disable/start"), a.browser.BeginDisable)
	mux.HandleFunc("POST "+a.path("/account/2fa/disable/finish"), a.browser.FinishDisable)
	mux.Handle("GET "+a.path("/auth/step-up"), a.browser.Authenticate(a.browser.RequireNormal(http.HandlerFunc(a.stepUpPage))))
	mux.HandleFunc("POST "+a.path("/auth/step-up/start"), a.browser.BeginStepUp)
	mux.HandleFunc("POST "+a.path("/auth/step-up/finish"), a.browser.FinishStepUp)
	mux.HandleFunc("GET "+a.path(security.RefreshPath), a.refreshPage)
	mux.HandleFunc("POST "+a.path(security.RefreshPath), a.browser.Refresh)
	mux.HandleFunc("POST "+a.path("/logout"), a.browser.Logout)
	mux.Handle("GET "+a.path("/account/password"), a.browser.Authenticate(http.HandlerFunc(a.passwordPage)))
	mux.HandleFunc("POST "+a.path("/account/password"), a.browser.ReplacePassword)
	mux.Handle("GET "+a.path("/overview"), a.browser.Authenticate(a.browser.RequireNormal(http.HandlerFunc(a.overview))))
	mux.Handle("GET "+a.path("/account"), a.browser.Authenticate(a.browser.RequireNormal(http.HandlerFunc(a.account))))
	mux.HandleFunc("POST "+a.path("/account/sessions/{session_id}/revoke"), a.browser.RevokeSession)
	mux.Handle("GET "+a.path("/audit"), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.audit))))
	mux.Handle("GET "+a.path("/access"), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.accessList))))
	mux.Handle("GET "+a.path("/access/new"), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.accessCreatePage))))
	mux.Handle("GET "+a.path("/access/users/{user_id}"), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.accessDetail))))
	mux.HandleFunc("POST "+a.path("/access/users"), a.accessCreate)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}"), a.accessUpdate)
	for _, channel := range a.bindingChoices {
		route := "/access/users/{user_id}/invitations/" + channel
		mux.HandleFunc("POST "+a.path(route), func(w http.ResponseWriter, r *http.Request) { a.issueBindingInvitation(w, r, channel) })
		mux.HandleFunc("POST "+a.path(route+"/cancel"), func(w http.ResponseWriter, r *http.Request) { a.cancelBindingInvitation(w, r, channel) })
		mux.HandleFunc("POST "+a.path(route+"/identity"), func(w http.ResponseWriter, r *http.Request) { a.refreshBindingIdentity(w, r, channel) })
		mux.Handle("GET "+a.path(route), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, a.path("/access/users/"+url.PathEscape(r.PathValue("user_id"))), http.StatusSeeOther)
		}))))
	}
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/bindings/{binding_id}/delete"), a.accessBindingDelete)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/credential"), a.accessCredentialReset)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/sessions/{session_id}/revoke"), a.accessSessionRevoke)
	qaHandler, err := QAHandler(a.basePath)
	if err != nil {
		return nil, err
	}
	mux.HandleFunc("GET "+a.path("/qa/ui/"), func(w http.ResponseWriter, r *http.Request) {
		if !a.qa {
			http.NotFound(w, r)
			return
		}
		qaHandler.ServeHTTP(w, r)
	})
	return securityHeaders(mux, a.basePath), nil
}

func (a *httpApp) path(route string) string { return a.basePath + route }

func (a *httpApp) loginPage(w http.ResponseWriter, r *http.Request) {
	csrf, err := a.browser.EnsureCSRF(w, r)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateLogin, webui.Page{Title: "Sign in · Balda", Current: webui.LocationLogin, CSRFToken: csrf})
}

func (a *httpApp) refreshPage(w http.ResponseWriter, r *http.Request) {
	csrf, err := a.browser.EnsureCSRF(w, r)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	returnTo := a.browser.SafeReturnPath(r.URL.Query().Get("return_to"), a.path(string(webui.LocationOverview)))
	a.render(w, r, http.StatusOK, webui.TemplateRefresh, webui.Page{Title: "Restore session · Balda", CSRFToken: csrf, ReturnTo: returnTo, AutoRefresh: true})
}

func (a *httpApp) passwordPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, webui.TemplatePassword, webui.Page{
		Title: "Change password · Balda", Current: webui.LocationAccount, CSRFToken: a.browser.CSRFToken(r),
	})
}

func (a *httpApp) overview(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.renderSecurityError(w, r, http.StatusUnauthorized)
		return
	}
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateOverview, webui.Page{
		Title: "Overview · Balda", Current: webui.LocationOverview,
		Navigation: webui.Navigation(capabilities, webui.LocationOverview), Capabilities: a.cards,
	})
}

func (a *httpApp) account(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	accessCookie, err := r.Cookie(security.AccessCookieName)
	if err != nil || accessCookie.Value == "" {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	sessionPage, err := a.security.ListSessions(r.Context(), accessCookie.Value, "", usercmd.PageRequest{
		Limit: sessionPageSize, AfterID: r.URL.Query().Get("after_session"),
	})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	sessions := make([]webui.SessionView, 0, len(sessionPage.Sessions))
	for _, session := range sessionPage.Sessions {
		sessions = append(sessions, webui.ProjectSession(session, principal.FamilyID, time.Now().UTC()))
	}
	nextURL := ""
	if sessionPage.NextAfterID != "" {
		nextURL = "/account?after_session=" + url.QueryEscape(sessionPage.NextAfterID)
	}
	var mfa *webui.MFAView
	if principal.User.Role == usercmd.RoleAdministrator {
		status, err := a.security.MFAStatus(r.Context(), accessCookie.Value)
		if err != nil {
			a.browser.WriteError(w, r, err)
			return
		}
		mfa = &webui.MFAView{Enabled: status.Enabled, Available: status.Available, CreatedAt: status.CreatedAt, LastUsedAt: status.LastUsedAt}
	}
	view := webui.ProjectUser(principal.User)
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAccount, webui.Page{
		Title: "Account · Balda", Current: webui.LocationAccount,
		Navigation: webui.Navigation(capabilities, webui.LocationAccount),
		User:       &view, MFA: mfa, Sessions: sessions, CSRFToken: a.browser.CSRFToken(r),
		SessionActionPrefix: "/account/sessions", SessionNextURL: nextURL,
	})
}

func (a *httpApp) audit(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	limit := 0
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			a.browser.WriteError(w, r, usercmd.ErrInvalid)
			return
		}
		limit = parsed
	}
	fromText := strings.TrimSpace(r.URL.Query().Get("from"))
	toText := strings.TrimSpace(r.URL.Query().Get("to"))
	var from, before time.Time
	if fromText != "" {
		parsed, parseErr := time.Parse("2006-01-02", fromText)
		if parseErr != nil {
			a.browser.WriteError(w, r, usercmd.ErrInvalid)
			return
		}
		from = parsed
	}
	if toText != "" {
		lastDay, parseErr := time.Parse("2006-01-02", toText)
		if parseErr != nil {
			a.browser.WriteError(w, r, usercmd.ErrInvalid)
			return
		}
		before = lastDay.AddDate(0, 0, 1)
	}
	request := audit.Request{
		AfterID: r.URL.Query().Get("after"), Limit: limit,
		Action: usercmd.AuditAction(r.URL.Query().Get("action")), Outcome: usercmd.AuditOutcome(r.URL.Query().Get("outcome")),
		TargetType:  usercmd.AuditTargetType(r.URL.Query().Get("target")),
		ActorUserID: r.URL.Query().Get("actor"), From: from, Before: before,
	}
	page, err := a.auditLog.List(r.Context(), principal.User, request)
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	events := make([]webui.AuditView, 0, len(page.Events))
	actor := access.Actor{User: principal.User, SessionID: principal.FamilyID}
	knownUsers := make(map[string]string)
	userName := func(id string) (string, error) {
		if name, ok := knownUsers[id]; ok {
			return name, nil
		}
		user, lookupErr := a.access.GetUser(r.Context(), actor, id)
		if errors.Is(lookupErr, usercmd.ErrNotFound) {
			knownUsers[id] = ""
			return "", nil
		}
		if lookupErr != nil {
			return "", lookupErr
		}
		knownUsers[id] = user.DisplayName
		return user.DisplayName, nil
	}
	for _, event := range page.Events {
		view := webui.ProjectAudit(event)
		if event.ActorUserID != "" {
			name, lookupErr := userName(event.ActorUserID)
			if lookupErr != nil {
				a.browser.WriteError(w, r, lookupErr)
				return
			}
			if name != "" {
				view.ActorName = name
			}
		}
		if event.TargetType == usercmd.AuditTargetUser && event.TargetID != "" {
			name, lookupErr := userName(event.TargetID)
			if lookupErr != nil {
				a.browser.WriteError(w, r, lookupErr)
				return
			}
			if name != "" {
				view.TargetName = name
			}
		}
		events = append(events, view)
	}
	nextURL := ""
	if page.NextAfterID != "" {
		query := url.Values{"after": {page.NextAfterID}}
		if request.Limit != 0 {
			query.Set("limit", strconv.Itoa(request.Limit))
		}
		if request.Action != "" {
			query.Set("action", string(request.Action))
		}
		if request.Outcome != "" {
			query.Set("outcome", string(request.Outcome))
		}
		if request.TargetType != "" {
			query.Set("target", string(request.TargetType))
		}
		if request.ActorUserID != "" {
			query.Set("actor", request.ActorUserID)
		}
		if fromText != "" {
			query.Set("from", fromText)
		}
		if toText != "" {
			query.Set("to", toText)
		}
		nextURL = a.path("/audit?") + query.Encode()
	}
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAudit, webui.Page{
		Title: "Audit · Balda", Current: webui.LocationAudit,
		Navigation: webui.Navigation(capabilities, webui.LocationAudit), Audit: events,
		AuditAction: string(request.Action), AuditOutcome: string(request.Outcome),
		AuditTargetType: string(request.TargetType), AuditActor: request.ActorUserID,
		AuditFrom: fromText, AuditTo: toText, NextURL: nextURL,
	})
}

func (a *httpApp) accessList(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	userList, err := a.access.ListUsers(r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	role := r.URL.Query().Get("role")
	status := r.URL.Query().Get("status")
	if len(search) > 100 || (role != "" && role != string(usercmd.RoleAdministrator) && role != string(usercmd.RoleOperator)) ||
		(status != "" && status != string(usercmd.StatusActive) && status != string(usercmd.StatusDisabled)) {
		a.browser.WriteError(w, r, usercmd.ErrInvalid)
		return
	}
	views := make([]webui.UserView, 0, len(userList))
	for _, user := range userList {
		if (role != "" && string(user.Role) != role) || (status != "" && string(user.Status) != status) ||
			(search != "" && !strings.Contains(strings.ToLower(user.DisplayName+" "+user.Username), strings.ToLower(search))) {
			continue
		}
		views = append(views, webui.ProjectUser(user))
	}
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAccess, webui.Page{
		Title: "Access · Balda", Current: webui.LocationAccess,
		Navigation: webui.Navigation(capabilities, webui.LocationAccess), Users: views,
		CSRFToken: a.browser.CSRFToken(r), AccessSearch: search, AccessRole: role, AccessStatus: status,
	})
}

func (a *httpApp) accessCreatePage(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAccess, webui.Page{
		Title: "Create user · Balda", Current: webui.LocationAccess,
		Navigation: webui.Navigation(capabilities, webui.LocationAccess),
		CreateUser: true, CSRFToken: a.browser.CSRFToken(r),
	})
}

func (a *httpApp) accessDetail(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	page, err := a.bindingDetailPage(r, principal, r.PathValue("user_id"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateAccess, page)
}

func (a *httpApp) accessBindingDelete(w http.ResponseWriter, r *http.Request) {
	form, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	version, err := parseFormVersion(form.Get("expected_version"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	err = a.access.RemoveBinding(r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID}, access.BindingRemoval{
		UserID: r.PathValue("user_id"), BindingID: r.PathValue("binding_id"),
		ExpectedVersion: version, ConfirmImpact: form.Get("confirm_bot_impact") == checkedFormValue,
	})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	a.respondAccessDetailMutation(w, r, r.PathValue("user_id"))
}

func (a *httpApp) respondAccessDetailMutation(w http.ResponseWriter, r *http.Request, userID string) {
	location := a.path("/access/users/" + url.PathEscape(userID))
	if err := webui.RespondMutationPath(w, r, location); err != nil {
		a.browser.WriteError(w, r, err)
	}
}

func (a *httpApp) accessCreate(w http.ResponseWriter, r *http.Request) {
	form, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	password := []byte(form.Get("temporary_password"))
	defer clear(password)
	_, err := a.access.CreateUser(r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID}, access.CreateInput{
		DisplayName: form.Get("display_name"), Username: form.Get("username"), TemporaryPassword: password,
		Role: usercmd.Role(form.Get("role")), Status: usercmd.UserStatus(form.Get("status")),
	})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	a.respondMutation(w, r, webui.LocationAccess)
}

func (a *httpApp) accessUpdate(w http.ResponseWriter, r *http.Request) {
	form, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	expectedVersion, err := parseFormVersion(form.Get("expected_version"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	_, err = a.access.UpdateUser(r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID}, access.UpdateInput{
		UserID: r.PathValue("user_id"), DisplayName: form.Get("display_name"), Username: form.Get("username"),
		Role: usercmd.Role(form.Get("role")), Status: usercmd.UserStatus(form.Get("status")), ExpectedVersion: expectedVersion,
		AcknowledgeBotAccessImpact: form.Get("confirm_bot_impact") == checkedFormValue,
	})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	a.respondMutation(w, r, webui.LocationAccess)
}

func (a *httpApp) accessCredentialReset(w http.ResponseWriter, r *http.Request) {
	form, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	userVersion, err := parseFormVersion(form.Get("expected_user_version"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	credentialVersion, err := parseFormVersion(form.Get("expected_credential_version"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	password := []byte(form.Get("temporary_password"))
	defer clear(password)
	err = a.access.ResetCredential(r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID}, access.CredentialInput{
		UserID: r.PathValue("user_id"), ExpectedUserVersion: userVersion,
		ExpectedCredentialVersion: credentialVersion, TemporaryPassword: password,
		ConfirmCurrent: form.Get("confirm_current") == checkedFormValue,
	})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	a.respondMutation(w, r, webui.LocationAccess)
}

func (a *httpApp) accessSessionRevoke(w http.ResponseWriter, r *http.Request) {
	form, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	err := a.access.RevokeSession(
		r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID},
		r.PathValue("user_id"), r.PathValue("session_id"), form.Get("confirm_current") == checkedFormValue,
	)
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	if r.PathValue("session_id") == principal.FamilyID {
		a.respondMutation(w, r, webui.LocationLogin)
		return
	}
	a.respondAccessDetailMutation(w, r, r.PathValue("user_id"))
}

func (a *httpApp) respondMutation(w http.ResponseWriter, r *http.Request, location webui.Location) {
	if err := webui.RespondMutationAt(w, r, location, a.basePath); err != nil {
		a.browser.WriteError(w, r, fmt.Errorf("respond to mutation: %w", err))
	}
}

func parseFormVersion(raw string) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || version == 0 {
		return 0, usercmd.ErrInvalid
	}
	return version, nil
}

func (a *httpApp) renderStepUpRequired(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusForbidden, webui.TemplateError, webui.Page{
		Title: "Verification required · Balda", StepUpURL: a.path("/auth/step-up"),
		Error: &webui.ErrorView{Heading: "Confirm your passkey", Message: "This change was not applied. Confirm your passkey, then return and submit it again."},
	})
}

func (a *httpApp) renderSecurityError(w http.ResponseWriter, r *http.Request, status int) {
	message := "The request could not be completed."
	if status == http.StatusServiceUnavailable {
		message = "Passkey verification is unavailable. Use a supported browser at the configured HTTPS domain or localhost. If the key was lost or the domain changed, ask the host administrator to run the confirmed offline 2FA recovery command, then register a new key."
	}
	if status == http.StatusUnauthorized {
		switch r.URL.Path {
		case a.path("/login"):
			message = "Sign-in failed. Check your username and password."
		case a.path(security.RefreshPath):
			message = "This session cannot be restored. Sign in again to continue."
		case a.path("/account/password"):
			message = "The password could not be verified. Check it and try again."
		default:
			message = "Your session is unavailable. The browser will check whether it can restore access. You can also sign in again."
		}
	}
	if status == http.StatusConflict && r.URL.Path == a.path(security.RefreshPath) {
		message = "Another request has just refreshed this session. Reopen the page in a moment."
	}
	page := webui.Page{Title: http.StatusText(status) + " · Balda", Error: &webui.ErrorView{Heading: http.StatusText(status), Message: message}}
	templateName := webui.TemplateError
	switch {
	case strings.HasPrefix(r.URL.Path, a.path("/account/2fa/")):
		page.Error.Message = "The passkey operation did not complete. Check your current password and verification, then start again from Account. Your second-factor setting has not changed."
		page.RestartURL, page.RestartLabel = a.path(string(webui.LocationAccount)), "Return to Account"
	case strings.HasPrefix(r.URL.Path, a.path("/auth/step-up/")):
		page.Error.Message = "Passkey verification failed or expired. Start a new confirmation before repeating your sensitive action."
		page.RestartURL, page.RestartLabel = a.path("/auth/step-up"), "Start verification again"
	case r.URL.Path == a.path("/auth/webauthn/finish"):
		page.Error.Message = "Passkey verification failed or expired. Sign in again to start a new verification."
		page.RestartURL, page.RestartLabel = a.path(string(webui.LocationLogin)), "Start sign-in again"
	}
	switch r.URL.Path {
	case a.path("/login"):
		templateName = webui.TemplateLogin
		page.Current = webui.LocationLogin
		page.CSRFToken = a.browser.CSRFToken(r)
	case a.path(security.RefreshPath):
		templateName = webui.TemplateRefresh
		page.CSRFToken = a.browser.CSRFToken(r)
		page.ReturnTo = a.browser.SafeReturnPath(r.FormValue("return_to"), a.path(string(webui.LocationOverview)))
		page.RefreshRetryable = status == http.StatusConflict
	case a.path("/account/password"):
		templateName = webui.TemplatePassword
		page.CSRFToken = a.browser.CSRFToken(r)
	default:
		if status == http.StatusUnauthorized && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			csrf, err := a.browser.EnsureCSRF(w, r)
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			templateName = webui.TemplateRefresh
			page.CSRFToken = csrf
			page.ReturnTo = a.browser.SafeReturnPath(r.URL.RequestURI(), a.path(string(webui.LocationOverview)))
			page.AutoRefresh = true
		}
	}
	a.render(w, r, status, templateName, page)
}

func (a *httpApp) render(w http.ResponseWriter, r *http.Request, status int, name string, page webui.Page) {
	if principal, ok := security.PrincipalFromContext(r.Context()); ok && len(page.Navigation) > 0 {
		page.ViewerUsername = principal.User.Username
		if page.CSRFToken == "" {
			page.CSRFToken = a.browser.CSRFToken(r)
		}
	}
	if err := a.renderer.Render(w, r, status, name, page); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func securityHeaders(next http.Handler, basePath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, basePath+"/assets/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// The pinned HTMX script injects one fixed indicator stylesheet.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'sha256-faU7yAF8NxuMTNEwVmBz+VcYeIoBQ2EMHW3WaVxCvnk='; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
