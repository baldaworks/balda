package backoffice

import (
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

type httpApp struct {
	renderer *webui.Renderer
	browser  *security.Browser
	cards    []webui.CapabilityCard
	qa       bool
}

func newHTTPApp(store usercmd.Store, config ResolvedConfig) (*httpApp, error) {
	renderer, err := webui.NewRenderer()
	if err != nil {
		return nil, err
	}
	service, err := security.NewService(store, security.Config{
		AccessTTL: config.Server.AccessTokenTTL, RefreshTTL: config.Server.RefreshTokenTTL,
	})
	if err != nil {
		return nil, err
	}
	app := &httpApp{renderer: renderer, cards: ProjectCapabilityCards(config.Balda), qa: config.Server.QAUI}
	browser, err := security.NewBrowser(service, security.HTTPConfig{
		TrustedOrigin: config.Server.PublicURL, SecureCookies: config.Server.SecureCookies,
		ErrorHandler: app.renderSecurityError,
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
	mux.Handle("GET /assets/", assets)
	mux.Handle("GET /healthz", healthHandler())
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, string(webui.LocationOverview), http.StatusSeeOther)
	})
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.browser.Login)
	mux.HandleFunc("GET "+security.RefreshPath, a.refreshPage)
	mux.HandleFunc("POST "+security.RefreshPath, a.browser.Refresh)
	mux.HandleFunc("POST /logout", a.browser.Logout)
	mux.Handle("GET /account/password", a.browser.Authenticate(http.HandlerFunc(a.passwordPage)))
	mux.HandleFunc("POST /account/password", a.browser.ReplacePassword)
	mux.Handle("GET /overview", a.browser.Authenticate(a.browser.RequireNormal(http.HandlerFunc(a.overview))))
	mux.HandleFunc("GET /qa/ui/", a.qaPage)
	return securityHeaders(mux), nil
}

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
	returnTo := a.browser.SafeReturnPath(r.URL.Query().Get("return_to"), string(webui.LocationOverview))
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

func (a *httpApp) qaPage(w http.ResponseWriter, r *http.Request) {
	if !a.qa {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	fixture := strings.TrimPrefix(r.URL.Path, "/qa/ui/")
	page, templateName, ok := qaFixture(fixture)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	a.render(w, r, http.StatusOK, templateName, page)
}

func qaFixture(name string) (webui.Page, string, bool) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	admin := usercmd.BackofficeCapabilities{Overview: true, Account: true, ManageUsers: true, ViewAudit: true}
	switch name {
	case "login":
		return webui.Page{Title: "Sign in · QA", Current: webui.LocationLogin, CSRFToken: "qa-csrf"}, webui.TemplateLogin, true
	case "refresh":
		return webui.Page{Title: "Continue session · QA", CSRFToken: "qa-csrf", ReturnTo: "/overview"}, webui.TemplateRefresh, true
	case "password":
		return webui.Page{Title: "Replace password · QA", CSRFToken: "qa-csrf"}, webui.TemplatePassword, true
	case "overview", "":
		return webui.Page{
			Title: "Overview · QA", Current: webui.LocationOverview,
			Navigation: webui.Navigation(admin, webui.LocationOverview),
			Capabilities: []webui.CapabilityCard{
				{ID: "telegram", Name: "Telegram", Mode: "webhook", ListenAddr: "127.0.0.1:8080", Endpoint: "/telegram"},
				{ID: "slack-agent", Name: "Slack Agent", Mode: "agent-events", Streaming: true},
				{ID: "webhooks", Name: "Webhooks", Mode: "inbound", RouteCount: 3},
			},
			Audit: []webui.AuditView{{Action: "session.login.succeeded", Outcome: "succeeded", TargetType: "session", TargetID: "family-demo", OccurredAt: now}},
		}, webui.TemplateOverview, true
	default:
		return webui.Page{}, "", false
	}
}

func (a *httpApp) renderSecurityError(w http.ResponseWriter, r *http.Request, status int) {
	message := "The request could not be completed."
	if status == http.StatusUnauthorized {
		message = "Your session is unavailable. Continue with the refresh token or sign in again."
	}
	page := webui.Page{Title: http.StatusText(status) + " · Balda", Error: &webui.ErrorView{Heading: http.StatusText(status), Message: message}}
	templateName := webui.TemplateError
	switch r.URL.Path {
	case "/login":
		templateName = webui.TemplateLogin
		page.Current = webui.LocationLogin
		page.CSRFToken = a.browser.CSRFToken(r)
	case security.RefreshPath:
		templateName = webui.TemplateRefresh
		page.CSRFToken = a.browser.CSRFToken(r)
		page.ReturnTo = a.browser.SafeReturnPath(r.FormValue("return_to"), string(webui.LocationOverview))
	case "/account/password":
		templateName = webui.TemplatePassword
		page.CSRFToken = a.browser.CSRFToken(r)
	default:
		if status == http.StatusUnauthorized && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			templateName = webui.TemplateRefresh
			page.CSRFToken = a.browser.CSRFToken(r)
			page.ReturnTo = a.browser.SafeReturnPath(r.URL.RequestURI(), string(webui.LocationOverview))
		}
	}
	a.render(w, r, status, templateName, page)
}

func (a *httpApp) render(w http.ResponseWriter, r *http.Request, status int, name string, page webui.Page) {
	if err := a.renderer.Render(w, r, status, name, page); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
