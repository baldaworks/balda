package backoffice

import (
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// QAHandler serves synthetic previews without opening Backoffice state.
func QAHandler(basePath string) (http.Handler, error) {
	renderer, err := webui.NewQARenderer(basePath)
	if err != nil {
		return nil, err
	}
	assets, err := webui.Assets()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET "+basePath+"/assets/", http.StripPrefix(basePath, assets))
	mux.HandleFunc("GET "+basePath+"/qa/ui/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, basePath+"/qa/ui/")
		page, templateName, ok := qaFixture(name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		page.Preview = true
		if page.ReturnTo != "" {
			page.ReturnTo = basePath + "/qa/ui/overview"
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := renderer.Render(w, r, http.StatusOK, templateName, page); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	})
	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, basePath+"/qa/ui/") {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		mux.ServeHTTP(w, r)
	}), basePath), nil
}

type qaEntry struct {
	name         string
	label        string
	templateName string
	page         func() webui.Page
	gallery      bool
}

var qaEntries = []qaEntry{
	{name: "login", label: "Login", templateName: webui.TemplateLogin, page: qaLogin, gallery: true},
	{name: "login-error", label: "Login · error", templateName: webui.TemplateLogin, page: qaLoginError, gallery: true},
	{name: "refresh", label: "Session refresh", templateName: webui.TemplateRefresh, page: qaRefresh, gallery: true},
	{name: "refresh-error", label: "Session refresh · error", templateName: webui.TemplateRefresh, page: qaRefreshError, gallery: true},
	{name: "password", label: "Password replacement", templateName: webui.TemplatePassword, page: qaPassword, gallery: true},
	{name: "password-error", label: "Password replacement · error", templateName: webui.TemplatePassword, page: qaPasswordError, gallery: true},
	{name: "overview", label: "Overview", templateName: webui.TemplateOverview, page: qaOverview, gallery: true},
	{name: "overview-empty", label: "Overview · empty", templateName: webui.TemplateOverview, page: qaOverviewEmpty, gallery: true},
	{name: "access", label: "Access · detail", templateName: webui.TemplateAccess, page: qaAccess, gallery: true},
	{name: "access/users/user-demo", templateName: webui.TemplateAccess, page: qaAccess},
	{name: "access-list", label: "Access · list", templateName: webui.TemplateAccess, page: qaAccessList, gallery: true},
	{name: "access-empty", label: "Access · empty", templateName: webui.TemplateAccess, page: qaAccessEmpty, gallery: true},
	{name: "access-error", label: "Access · error", templateName: webui.TemplateAccess, page: qaAccessError, gallery: true},
	{name: "account", label: "Account", templateName: webui.TemplateAccount, page: qaAccount, gallery: true},
	{name: "account-empty", label: "Account · no sessions", templateName: webui.TemplateAccount, page: qaAccountEmpty, gallery: true},
	{name: "account-error", label: "Account · error", templateName: webui.TemplateAccount, page: qaAccountError, gallery: true},
	{name: "audit", label: "Audit", templateName: webui.TemplateAudit, page: qaAudit, gallery: true},
	{name: "audit-empty", label: "Audit · empty", templateName: webui.TemplateAudit, page: qaAuditEmpty, gallery: true},
	{name: "error", label: "Generic error", templateName: webui.TemplateError, page: qaError, gallery: true},
}

func qaFixture(name string) (webui.Page, string, bool) {
	if name == "" {
		links := make([]webui.QALink, 0, len(qaEntries))
		for _, entry := range qaEntries {
			if entry.gallery {
				links = append(links, webui.QALink{Label: entry.label, Path: "/" + entry.name})
			}
		}
		return webui.Page{Title: "Backoffice UI previews · QA", Gallery: links}, webui.TemplateGallery, true
	}
	for _, entry := range qaEntries {
		if entry.name == name {
			return entry.page(), entry.templateName, true
		}
	}
	return webui.Page{}, "", false
}

var qaNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func qaAdminNavigation(current webui.Location) []webui.NavItem {
	return webui.Navigation(usercmd.BackofficeCapabilities{
		Overview: true, Account: true, ManageUsers: true, ViewAudit: true,
	}, current)
}

func qaOperatorNavigation(current webui.Location) []webui.NavItem {
	return webui.Navigation(usercmd.BackofficeCapabilities{Overview: true, Account: true}, current)
}

func qaUser() webui.UserView {
	return webui.UserView{
		ID: "user-demo", DisplayName: "Bound operator", Username: "operator", Status: "active", Role: "operator",
		CredentialState: "temporary", MustChange: true, Version: 3, CredentialVersion: 2,
		Bindings: []webui.BindingView{{ID: "binding-telegram", ChannelType: "telegram", Principal: "42", DisplayName: "Operator", ProviderUsername: "operator", ProviderFirstName: "Op", Provenance: "synthetic fixture"}, {ID: "binding-slack", ChannelType: "slackagent", Principal: "T1:U1", DisplayName: "Operator Slack"}},
	}
}

func qaSessions() []webui.SessionView {
	return []webui.SessionView{{
		ID: "family-demo", Assurance: "normal", CreatedAt: qaNow,
		LastSeenAt: qaNow, ExpiresAt: qaNow.Add(12 * time.Hour), Current: true, Version: 2,
	}}
}

func qaLogin() webui.Page {
	return webui.Page{Title: "Sign in · QA", Current: webui.LocationLogin, CSRFToken: "qa-csrf"}
}

func qaLoginError() webui.Page {
	page := qaLogin()
	page.Error = &webui.ErrorView{Heading: "Sign in failed", Message: "Check the synthetic credentials."}
	return page
}

func qaRefresh() webui.Page {
	return webui.Page{Title: "Continue session · QA", CSRFToken: "qa-csrf", ReturnTo: "/overview"}
}

func qaRefreshError() webui.Page {
	page := qaRefresh()
	page.Error = &webui.ErrorView{Heading: "Session expired", Message: "Sign in again."}
	return page
}

func qaPassword() webui.Page {
	return webui.Page{Title: "Change password · QA", Current: webui.LocationAccount, CSRFToken: "qa-csrf"}
}

func qaPasswordError() webui.Page {
	page := qaPassword()
	page.Error = &webui.ErrorView{Heading: "Password not changed", Message: "Check the required fields."}
	return page
}

func qaOverview() webui.Page {
	return webui.Page{
		Title: "Overview · QA", Current: webui.LocationOverview,
		Navigation: qaAdminNavigation(webui.LocationOverview),
		Capabilities: []webui.CapabilityCard{
			{ID: "telegram", Name: "Telegram", Mode: "webhook", ListenAddr: "127.0.0.1:8080", Endpoint: "/telegram"},
			{ID: "slackagent", Name: "Slack Agent", Mode: "agent-events", Streaming: true},
			{ID: "webhooks", Name: "Webhooks", Mode: "inbound", RouteCount: 3},
		},
	}
}

func qaOverviewEmpty() webui.Page {
	page := qaOverview()
	page.Capabilities = nil
	return page
}

func qaAccess() webui.Page {
	user := qaUser()
	return webui.Page{
		Title: "Access · QA", Current: webui.LocationAccess,
		Navigation: qaAdminNavigation(webui.LocationAccess),
		User:       &user, CSRFToken: "qa-csrf", Sessions: qaSessions(), BindingChoices: []string{"telegram", "slackagent", "zulip"},
		SessionActionPrefix: "/access/users/" + user.ID + "/sessions",
	}
}

func qaAccessList() webui.Page {
	return webui.Page{
		Title: "Access · QA", Current: webui.LocationAccess,
		Navigation: qaAdminNavigation(webui.LocationAccess),
		Users:      []webui.UserView{qaUser()}, CSRFToken: "qa-csrf",
	}
}

func qaAccessEmpty() webui.Page {
	page := qaAccessList()
	page.Users = nil
	return page
}

func qaAccessError() webui.Page {
	page := qaAccess()
	page.Error = &webui.ErrorView{Heading: "User not updated", Message: "Review the synthetic input."}
	return page
}

func qaAccount() webui.Page {
	user := qaUser()
	user.CredentialState = "active"
	user.MustChange = false
	return webui.Page{
		Title: "Account · QA", Current: webui.LocationAccount,
		Navigation: qaOperatorNavigation(webui.LocationAccount),
		User:       &user, CSRFToken: "qa-csrf", Sessions: qaSessions(),
		SessionActionPrefix: "/account/sessions",
	}
}

func qaAccountEmpty() webui.Page {
	page := qaAccount()
	page.Sessions = nil
	return page
}

func qaAccountError() webui.Page {
	page := qaAccount()
	page.Error = &webui.ErrorView{Heading: "Account unavailable", Message: "Try again later."}
	return page
}

func qaAudit() webui.Page {
	familyID := "11111111-1111-4111-8111-111111111111"
	adminID := "22222222-2222-4222-8222-222222222222"
	return webui.Page{
		Title: "Audit · QA", Current: webui.LocationAudit,
		Navigation: qaAdminNavigation(webui.LocationAudit),
		Audit: []webui.AuditView{
			{ID: "33333333-3333-4333-8333-333333333331", Action: "session.refresh.succeeded", Outcome: "succeeded", ActorUserID: adminID, ActorSessionID: familyID, TargetType: "session", TargetID: familyID, OccurredAt: qaNow},
			{ID: "33333333-3333-4333-8333-333333333332", Action: "session.refresh.replay", Outcome: "denied", ActorUserID: adminID, ActorSessionID: familyID, TargetType: "session", TargetID: familyID, OccurredAt: qaNow.Add(time.Minute)},
			{ID: "33333333-3333-4333-8333-333333333333", Action: "session.revoked", Outcome: "succeeded", ActorUserID: adminID, TargetType: "session", TargetID: familyID, OccurredAt: qaNow.Add(2 * time.Minute)},
		},
	}
}

func qaAuditEmpty() webui.Page {
	page := qaAudit()
	page.Audit = nil
	return page
}

func qaError() webui.Page {
	return webui.Page{
		Title: "Unavailable · QA",
		Error: &webui.ErrorView{Heading: "Service unavailable", Message: "Try again later."},
	}
}
