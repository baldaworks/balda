package backoffice

import (
	"fmt"
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
		page, templateName, status, ok := qaFixture(name)
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
			w.WriteHeader(status)
			return
		}
		if err := renderer.Render(w, r, status, templateName, page); err != nil {
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
	status       int
}

var qaEntries = []qaEntry{
	{name: "login", label: "Login", templateName: webui.TemplateLogin, page: qaLogin, gallery: true},
	{name: "login-error", label: "Login · error", templateName: webui.TemplateLogin, page: qaLoginError, gallery: true},
	{name: "refresh", label: "Session refresh", templateName: webui.TemplateRefresh, page: qaRefresh, gallery: true},
	{name: "refresh-error", label: "Session refresh · error", templateName: webui.TemplateRefresh, page: qaRefreshError, gallery: true},
	{name: "refresh-conflict", label: "Session refresh · concurrent", templateName: webui.TemplateRefresh, page: qaRefreshConflict, gallery: true},
	{name: "password", label: "Password replacement", templateName: webui.TemplatePassword, page: qaPassword, gallery: true},
	{name: "password-error", label: "Password replacement · error", templateName: webui.TemplatePassword, page: qaPasswordError, gallery: true},
	{name: "overview", label: "Overview", templateName: webui.TemplateOverview, page: qaOverview, gallery: true},
	{name: "overview-empty", label: "Overview · empty", templateName: webui.TemplateOverview, page: qaOverviewEmpty, gallery: true},
	{name: "access", label: "Access · detail", templateName: webui.TemplateAccess, page: qaAccess, gallery: true},
	{name: "access-primary", label: "Access · primary administrator", templateName: webui.TemplateAccess, page: qaAccessPrimary, gallery: true},
	{name: "access-long", label: "Access · long content", templateName: webui.TemplateAccess, page: qaAccessLong, gallery: true},
	{name: "access/users/user-demo", templateName: webui.TemplateAccess, page: qaAccess},
	{name: "access-list", label: "Access · list", templateName: webui.TemplateAccess, page: qaAccessList, gallery: true},
	{name: "access-create", label: "Access · create", templateName: webui.TemplateAccess, page: qaAccessCreate, gallery: true},
	{name: "access-empty", label: "Access · empty", templateName: webui.TemplateAccess, page: qaAccessEmpty, gallery: true},
	{name: "access-error", label: "Access · error", templateName: webui.TemplateAccess, page: qaAccessError, gallery: true},
	{name: "account", label: "Account", templateName: webui.TemplateAccount, page: qaAccount, gallery: true},
	{name: "account-many", label: "Account · many active sessions", templateName: webui.TemplateAccount, page: qaAccountMany, gallery: true},
	{name: "account-many-next", label: "Account · older active sessions", templateName: webui.TemplateAccount, page: qaAccountManyNext, gallery: true},
	{name: "account-session-states", label: "Account · session state coverage", templateName: webui.TemplateAccount, page: qaAccountSessionStates, gallery: true},
	{name: "account-empty", label: "Account · no sessions", templateName: webui.TemplateAccount, page: qaAccountEmpty, gallery: true},
	{name: "account-error", label: "Account · error", templateName: webui.TemplateAccount, page: qaAccountError, gallery: true},
	{name: "audit", label: "Audit", templateName: webui.TemplateAudit, page: qaAudit, gallery: true},
	{name: "audit-empty", label: "Audit · empty", templateName: webui.TemplateAudit, page: qaAuditEmpty, gallery: true},
	{name: "error", label: "Generic error", templateName: webui.TemplateError, page: qaError, gallery: true},
	{name: "form-bad-request", label: "Form · invalid input (400)", templateName: webui.TemplateAccess, page: qaFormBadRequest, gallery: true, status: http.StatusBadRequest},
	{name: "form-forbidden", label: "Form · permission denied (403)", templateName: webui.TemplateError, page: qaFormForbidden, gallery: true, status: http.StatusForbidden},
	{name: "form-conflict", label: "Form · conflicting update (409)", templateName: webui.TemplateAccess, page: qaFormConflict, gallery: true, status: http.StatusConflict},
	{name: "form-server-error", label: "Form · service error (500)", templateName: webui.TemplateError, page: qaError, gallery: true, status: http.StatusInternalServerError},
}

func qaFixture(name string) (webui.Page, string, int, bool) {
	if name == "" {
		links := make([]webui.QALink, 0, len(qaEntries))
		for _, entry := range qaEntries {
			if entry.gallery {
				links = append(links, webui.QALink{Label: entry.label, Path: "/" + entry.name})
			}
		}
		return webui.Page{Title: "Backoffice UI previews · QA", Gallery: links}, webui.TemplateGallery, http.StatusOK, true
	}
	for _, entry := range qaEntries {
		if entry.name == name {
			status := entry.status
			if status == 0 {
				status = http.StatusOK
			}
			return entry.page(), entry.templateName, status, true
		}
	}
	return webui.Page{}, "", 0, false
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
	return []webui.SessionView{
		{ID: "family-demo", Assurance: "normal", CreatedAt: qaNow.Add(-time.Hour), LastSeenAt: qaNow, ExpiresAt: qaNow.Add(12 * time.Hour), Current: true, DeviceLabel: "Firefox on Linux", ConnectionPeer: "192.0.2.10", Version: 2},
		{ID: "family-other", Assurance: "normal", CreatedAt: qaNow.Add(-2 * time.Hour), LastSeenAt: qaNow.Add(-time.Hour), ExpiresAt: qaNow.Add(11 * time.Hour), DeviceLabel: "Unknown browser or device", Version: 1},
	}
}

func qaManySessions() []webui.SessionView {
	sessions := qaSessions()
	for i := 0; i < 8; i++ {
		sessions = append(sessions, webui.SessionView{
			ID: fmt.Sprintf("family-%02d", i), CreatedAt: qaNow.Add(-time.Duration(i+3) * time.Hour),
			LastSeenAt: qaNow.Add(-time.Duration(i+2) * time.Hour), ExpiresAt: qaNow.Add(12 * time.Hour),
			DeviceLabel: "Synthetic browser session", Version: 1,
		})
	}
	return sessions
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

func qaRefreshConflict() webui.Page {
	page := qaRefresh()
	page.Error = &webui.ErrorView{Heading: "Session changed", Message: "Another request refreshed this session. Reopen the page."}
	page.RefreshRetryable = true
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

func qaAccessPrimary() webui.Page {
	page := qaAccess()
	page.User.ID = "primary-demo"
	page.User.DisplayName = "QA Primary Administrator"
	page.User.Username = "superuser"
	page.User.Role = "administrator"
	page.User.Primary = true
	page.User.CredentialState = "active"
	page.User.MustChange = false
	page.User.Bindings = nil
	page.OwnUser = true
	page.SessionActionPrefix = "/access/users/primary-demo/sessions"
	return page
}

func qaAccessLong() webui.Page {
	page := qaAccess()
	page.User.DisplayName = "Long synthetic display name used to check that a Backoffice profile remains readable on a narrow phone screen"
	page.User.Bindings[1].Principal = "T12345678901234567890:U12345678901234567890"
	page.User.Bindings[1].DisplayName = "Long synthetic Slack Agent binding with a descriptive label spanning several words"
	return page
}

func qaAccessList() webui.Page {
	return webui.Page{
		Title: "Access · QA", Current: webui.LocationAccess,
		Navigation: qaAdminNavigation(webui.LocationAccess),
		Users:      []webui.UserView{qaUser()}, CSRFToken: "qa-csrf",
	}
}

func qaAccessCreate() webui.Page {
	return webui.Page{
		Title: "Create user · QA", Current: webui.LocationAccess,
		Navigation: qaAdminNavigation(webui.LocationAccess),
		CreateUser: true, CSRFToken: "qa-csrf",
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

func qaAccountMany() webui.Page {
	page := qaAccount()
	page.Sessions = qaManySessions()
	page.SessionNextURL = "/account-many-next"
	return page
}

func qaAccountManyNext() webui.Page {
	page := qaAccount()
	page.Sessions = []webui.SessionView{{ID: "family-older", LastSeenAt: qaNow.Add(-11 * time.Hour), CreatedAt: qaNow.Add(-12 * time.Hour), ExpiresAt: qaNow.Add(time.Hour), DeviceLabel: "Older synthetic session", Version: 1}}
	return page
}

func qaAccountSessionStates() webui.Page {
	page := qaAccount()
	page.MixedSessions = true
	page.Sessions = append(page.Sessions,
		webui.SessionView{ID: "family-ended", LastSeenAt: qaNow.Add(-3 * time.Hour), CreatedAt: qaNow.Add(-4 * time.Hour), ExpiresAt: qaNow.Add(time.Hour), RevokedAt: qaNow.Add(-time.Hour), Revoked: true, DeviceLabel: "Ended synthetic session"},
		webui.SessionView{ID: "family-expired", LastSeenAt: qaNow.Add(-5 * time.Hour), CreatedAt: qaNow.Add(-6 * time.Hour), ExpiresAt: qaNow.Add(-time.Hour), Expired: true, DeviceLabel: "Expired synthetic session"},
	)
	return page
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
			{ID: "33333333-3333-4333-8333-333333333333", Action: "session.revoked", ActionLabel: "Ended browser session", Outcome: "succeeded", ActorUserID: adminID, ActorName: "QA Administrator", TargetType: "session", TargetID: familyID, TargetName: "Browser session", OccurredAt: qaNow.Add(2 * time.Minute)},
			{ID: "33333333-3333-4333-8333-333333333332", Action: "session.refresh.replay", ActionLabel: "Detected session token replay", Outcome: "denied", ActorUserID: adminID, ActorName: "QA Administrator", ActorSessionID: familyID, TargetType: "session", TargetID: familyID, TargetName: "Browser session", OccurredAt: qaNow.Add(time.Minute)},
			{ID: "33333333-3333-4333-8333-333333333331", Action: "session.refresh.succeeded", ActionLabel: "Restored browser session", Outcome: "succeeded", ActorUserID: adminID, ActorName: "QA Administrator", ActorSessionID: familyID, TargetType: "session", TargetID: familyID, TargetName: "Browser session", OccurredAt: qaNow},
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

func qaFormBadRequest() webui.Page {
	page := qaAccess()
	page.Error = &webui.ErrorView{Heading: "User not updated", Message: "Review the input and try again."}
	return page
}

func qaFormForbidden() webui.Page {
	page := qaError()
	page.Error = &webui.ErrorView{Heading: "Permission denied", Message: "Sign in with an authorized account or return to Overview."}
	return page
}

func qaFormConflict() webui.Page {
	page := qaAccess()
	page.Error = &webui.ErrorView{Heading: "User not updated", Message: "This user changed while you were editing. Reload the page and review the latest details."}
	return page
}
