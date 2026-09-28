package backoffice

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/access"
	"github.com/baldaworks/balda/internal/apps/backoffice/audit"
	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const checkedFormValue = "yes"

type httpApp struct {
	renderer       *webui.Renderer
	browser        *security.Browser
	security       *security.Service
	access         *access.Service
	auditLog       *audit.Service
	cards          []webui.CapabilityCard
	bindingChoices []string
	qa             bool
	basePath       string
}

func newHTTPApp(store usercmd.Store, config ResolvedConfig) (*httpApp, error) {
	renderer, err := webui.NewRenderer(config.Server.BasePath)
	if err != nil {
		return nil, err
	}
	service, err := security.NewService(store, security.Config{
		AccessTTL: config.Server.AccessTokenTTL, RefreshTTL: config.Server.RefreshTokenTTL,
	})
	if err != nil {
		return nil, err
	}
	bindingChoices := configuredBindingChannels(config.Balda)
	app := &httpApp{renderer: renderer, security: service, access: access.NewService(store, bindingChoices...), auditLog: audit.NewService(store), cards: ProjectCapabilityCards(config.Balda), bindingChoices: bindingChoices, qa: config.Server.QAUI, basePath: config.Server.BasePath}
	browser, err := security.NewBrowser(service, security.HTTPConfig{
		TrustedOrigin: config.Server.PublicURL, SecureCookies: config.Server.SecureCookies, BasePath: config.Server.BasePath,
		ErrorHandler: app.renderSecurityError,
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
	mux.Handle("GET "+a.path("/access/users/{user_id}"), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.accessDetail))))
	mux.HandleFunc("POST "+a.path("/access/users"), a.accessCreate)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}"), a.accessUpdate)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/bindings"), a.accessBindingCreate)
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
	a.render(w, r, http.StatusOK, webui.TemplateRefresh, webui.Page{Title: "Continue session · Balda", CSRFToken: csrf, ReturnTo: returnTo})
}

func (a *httpApp) passwordPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, webui.TemplatePassword, webui.Page{
		Title: "Replace password · Balda", Current: webui.LocationAccount, CSRFToken: a.browser.CSRFToken(r),
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
	sessionPage, err := a.security.ListSessions(r.Context(), accessCookie.Value, "", usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	sessions := make([]webui.SessionView, 0, len(sessionPage.Sessions))
	for _, session := range sessionPage.Sessions {
		sessions = append(sessions, webui.ProjectSession(session, principal.FamilyID))
	}
	view := webui.ProjectUser(principal.User)
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAccount, webui.Page{
		Title: "Account · Balda", Current: webui.LocationAccount,
		Navigation: webui.Navigation(capabilities, webui.LocationAccount),
		User:       &view, Sessions: sessions, CSRFToken: a.browser.CSRFToken(r),
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
	request := audit.Request{
		AfterID: r.URL.Query().Get("after"), Limit: limit,
		Action: usercmd.AuditAction(r.URL.Query().Get("action")), Outcome: usercmd.AuditOutcome(r.URL.Query().Get("outcome")),
		TargetType: usercmd.AuditTargetType(r.URL.Query().Get("target")),
	}
	page, err := a.auditLog.List(r.Context(), principal.User, request)
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	events := make([]webui.AuditView, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, webui.ProjectAudit(event))
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
		nextURL = a.path("/audit?") + query.Encode()
	}
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAudit, webui.Page{
		Title: "Audit · Balda", Current: webui.LocationAudit,
		Navigation: webui.Navigation(capabilities, webui.LocationAudit), Audit: events,
		AuditAction: string(request.Action), AuditOutcome: string(request.Outcome),
		AuditTargetType: string(request.TargetType), NextURL: nextURL,
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
	views := make([]webui.UserView, 0, len(userList))
	for _, user := range userList {
		views = append(views, webui.ProjectUser(user))
	}
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAccess, webui.Page{
		Title: "Access · Balda", Current: webui.LocationAccess,
		Navigation: webui.Navigation(capabilities, webui.LocationAccess), Users: views,
		CSRFToken: a.browser.CSRFToken(r),
	})
}

func (a *httpApp) accessDetail(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		a.browser.WriteError(w, r, security.ErrUnauthenticated)
		return
	}
	actor := access.Actor{User: principal.User, SessionID: principal.FamilyID}
	user, err := a.access.GetUser(r.Context(), actor, r.PathValue("user_id"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	sessionPage, err := a.access.ListSessions(r.Context(), actor, user.ID)
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	sessions := make([]webui.SessionView, 0, len(sessionPage.Sessions))
	for _, session := range sessionPage.Sessions {
		sessions = append(sessions, webui.ProjectSession(session, principal.FamilyID))
	}
	view := webui.ProjectUser(user)
	capabilities := users.BackofficeCapabilities(principal.User)
	a.render(w, r, http.StatusOK, webui.TemplateAccess, webui.Page{
		Title: "Access · " + user.DisplayName, Current: webui.LocationAccess,
		Navigation: webui.Navigation(capabilities, webui.LocationAccess), User: &view,
		Sessions: sessions, CSRFToken: a.browser.CSRFToken(r), BindingChoices: a.bindingChoices,
	})
}

func (a *httpApp) accessBindingCreate(w http.ResponseWriter, r *http.Request) {
	form, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	version, err := parseFormVersion(form.Get("expected_version"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	err = a.access.AddBinding(r.Context(), access.Actor{User: principal.User, SessionID: principal.FamilyID}, access.BindingInput{
		UserID: r.PathValue("user_id"), ChannelType: form.Get("channel_type"),
		Principal: form.Get("principal"), ExpectedVersion: version,
	})
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	a.respondAccessDetailMutation(w, r, r.PathValue("user_id"))
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
	if webui.EligibleFragment(r) {
		w.Header().Set("HX-Location", location)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
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
	a.respondMutation(w, r, webui.LocationAccess)
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

func (a *httpApp) renderSecurityError(w http.ResponseWriter, r *http.Request, status int) {
	message := "The request could not be completed."
	if status == http.StatusUnauthorized {
		message = "Your session is unavailable. Continue with the refresh token or sign in again."
	}
	page := webui.Page{Title: http.StatusText(status) + " · Balda", Error: &webui.ErrorView{Heading: http.StatusText(status), Message: message}}
	templateName := webui.TemplateError
	switch r.URL.Path {
	case a.path("/login"):
		templateName = webui.TemplateLogin
		page.Current = webui.LocationLogin
		page.CSRFToken = a.browser.CSRFToken(r)
	case a.path(security.RefreshPath):
		templateName = webui.TemplateRefresh
		page.CSRFToken = a.browser.CSRFToken(r)
		page.ReturnTo = a.browser.SafeReturnPath(r.FormValue("return_to"), a.path(string(webui.LocationOverview)))
	case a.path("/account/password"):
		templateName = webui.TemplatePassword
		page.CSRFToken = a.browser.CSRFToken(r)
	default:
		if status == http.StatusUnauthorized && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			templateName = webui.TemplateRefresh
			page.CSRFToken = a.browser.CSRFToken(r)
			page.ReturnTo = a.browser.SafeReturnPath(r.URL.RequestURI(), a.path(string(webui.LocationOverview)))
		}
	}
	a.render(w, r, status, templateName, page)
}

func (a *httpApp) render(w http.ResponseWriter, r *http.Request, status int, name string, page webui.Page) {
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
