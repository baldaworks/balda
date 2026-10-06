package backoffice

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
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
		if len(page.Navigation) > 0 {
			if page.ViewerUsername == "" {
				page.ViewerUsername = "qa-superuser"
			}
		}
		if page.ReturnTo != "" {
			page.ReturnTo = basePath + "/qa/ui/overview"
		}
		if page.RestartURL != "" {
			page.RestartURL = basePath + "/qa/ui/mcp"
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
	{name: "mcp-oauth-return", label: "MCP · native callback continuation", templateName: webui.TemplateOAuthReturn, page: func() webui.Page {
		return webui.Page{Title: "Continue to MCP · QA", RestartURL: "/mcp"}
	}, gallery: true},
	{name: "mcp-authorizing", label: "MCP · pending browser authorization", templateName: webui.TemplateMCP, page: func() webui.Page {
		p := qaMCPEditor(false, true)
		p.MCP.Editor.Attempt = &webui.MCPAttempt{ID: "qa-attempt", CancelPath: "/mcp/oauth/attempts/qa-attempt/cancel", Expires: "2026-08-01 12:10 UTC"}
		return p
	}, gallery: true},
	{name: "mcp-device-issued", label: "MCP · one-time device instructions", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPDevice(mcpcmd.DevicePending, true) }, gallery: true},
	{name: "mcp-device-pending", label: "MCP · device status without instructions", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPDevice(mcpcmd.DevicePending, false) }, gallery: true},
	{name: "mcp-device-authorized", label: "MCP · saved device grant, readiness separate", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPDevice(mcpcmd.DeviceAuthorized, false) }, gallery: true},
	{name: "mcp-device-denied", label: "MCP · device denied", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPDevice(mcpcmd.DeviceDenied, false) }, gallery: true},
	{name: "mcp-device-expired", label: "MCP · device expired", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPDevice(mcpcmd.DeviceExpired, false) }, gallery: true},
	{name: "mcp-device-failed", label: "MCP · device failed", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPDevice(mcpcmd.DeviceFailed, false) }, gallery: true},
	{name: "mcp-authorization-unavailable", label: "MCP · authorization unsupported", templateName: webui.TemplateError, page: func() webui.Page {
		p := qaMCP()
		p.MCP = nil
		p.Error = &webui.ErrorView{Heading: "Worker authorization could not complete", Message: "Device authorization is unavailable. Reopen the connection and use supported browser authorization."}
		return p
	}, status: http.StatusServiceUnavailable, gallery: true},
	{name: "mcp-authorization-retry", label: "MCP · saved authorization with explicit attachment retry", templateName: webui.TemplateMCP, page: func() webui.Page {
		p := qaMCPEditor(false, true)
		p.MCP.Editor.Row.RevisionID = "qa-revision"
		p.MCP.Editor.Row.Authorization = "Authorized"
		p.MCP.Editor.Row.Recovery = ""
		p.MCP.ProbeMessage = "Authorization was saved. Check tool availability on the connection; retry if needed."
		return p
	}, gallery: true},

	{name: "mcp-retained", label: "MCP · deleted retained definition", templateName: webui.TemplateMCP, page: func() webui.Page {
		p := qaMCPEditor(false, false)
		p.MCP.Editor.Row.Deleted = true
		p.MCP.Editor.AuthorizationAvailable = false
		p.MCP.Editor.Row.Status = "Deleted"
		p.MCP.Editor.Row.Ready = false
		return p
	}, gallery: true},
	{name: "mcp", label: "MCP · inventory and readiness", templateName: webui.TemplateMCP, page: qaMCP, gallery: true},
	{name: "mcp-public", label: "MCP · available public server, optional OAuth", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPState(mcpcmd.StatusReady, "", mcpcmd.TransportHTTP) }, gallery: true},
	{name: "mcp-auth-required", label: "MCP · authorization required", templateName: webui.TemplateMCP, page: func() webui.Page {
		return qaMCPState(mcpcmd.StatusAuthRequired, mcpcmd.GrantAuthRequired, mcpcmd.TransportSSE)
	}, gallery: true},
	{name: "mcp-revoked", label: "MCP · revoked shared authorization", templateName: webui.TemplateMCP, page: func() webui.Page {
		return qaMCPState(mcpcmd.StatusDisconnected, mcpcmd.GrantDisconnected, mcpcmd.TransportHTTP)
	}, gallery: true},
	{name: "mcp-stdio", label: "MCP · local process", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPState(mcpcmd.StatusReady, "", mcpcmd.TransportStdio) }, gallery: true},
	{name: "mcp-saved-start-failed", label: "MCP · saved connection, failed OAuth start", templateName: webui.TemplateError, page: func() webui.Page {
		p := qaMCP()
		p.MCP = nil
		p.Error = &webui.ErrorView{Heading: "Connection saved; OAuth could not start", Message: "Use browser authorization if the service does not support device authorization. Open the saved connection to retry authorization."}
		p.RestartURL, p.RestartLabel = "/mcp/connections/qa-worker", "Open saved connection"
		return p
	}, gallery: true, status: http.StatusServiceUnavailable},
	{name: "mcp-empty", label: "MCP · empty inventory", templateName: webui.TemplateMCP, page: func() webui.Page { p := qaMCP(); p.MCP.Rows = nil; return p }, gallery: true},
	{name: "mcp/new", label: "MCP · new server", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPEditor(true, false) }, gallery: true},
	{name: "mcp/connections/qa-worker", label: "MCP · edit server and protected values", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPEditor(false, false) }, gallery: true},
	{name: "mcp/connections/config:qa-worker", label: "MCP · configuration read-only", templateName: webui.TemplateMCP, page: func() webui.Page { return qaMCPEditor(false, true) }, gallery: true},
	{name: "mcp-probe", label: "MCP · candidate probe", templateName: webui.TemplateMCP, page: func() webui.Page {
		p := qaMCPEditor(false, false)
		p.MCP.ProbeMessage = "Candidate probe succeeded: 3 tools discovered. No definition was saved; runtime readiness is unchanged."
		return p
	}, gallery: true},
	{name: "mcp-invalid", label: "MCP · invalid definition (400)", templateName: webui.TemplateMCP, page: func() webui.Page {
		p := qaMCPEditor(true, false)
		p.Error = &webui.ErrorView{Heading: "MCP operation could not complete", Message: "Review the transport, targets and value operations. Enter replacement values again."}
		return p
	}, gallery: true, status: 400},
	{name: "mcp-conflict", label: "MCP · conflicting update (409)", templateName: webui.TemplateMCP, page: func() webui.Page {
		p := qaMCPEditor(false, false)
		p.Error = &webui.ErrorView{Heading: "MCP operation could not complete", Message: "The connection or administrator authority changed. Reopen the editor before trying again."}
		return p
	}, gallery: true, status: 409},
	{name: "mcp-unavailable", label: "MCP · unavailable management (503)", templateName: webui.TemplateError, page: func() webui.Page {
		return webui.Page{Title: "MCP management unavailable · QA", Error: &webui.ErrorView{Heading: "MCP management unavailable", Message: "Check the host configuration and reopen MCP management."}}
	}, gallery: true, status: 503},

	{name: "account-2fa-off", label: "Account · 2FA off", templateName: webui.TemplateAccount, page: func() webui.Page { p := qaAccount(); p.MFA = &webui.MFAView{Available: true}; return p }, gallery: true},
	{name: "account-2fa-enabled", label: "Account · 2FA enabled", templateName: webui.TemplateAccount, page: func() webui.Page {
		p := qaAccount()
		p.MFA = &webui.MFAView{Enabled: true, Available: true, CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
		return p
	}, gallery: true},
	{name: "account-2fa-unavailable", label: "Account · 2FA unavailable", templateName: webui.TemplateAccount, page: func() webui.Page { p := qaAccount(); p.MFA = &webui.MFAView{}; return p }, gallery: true},
	{name: "webauthn-register", label: "Passkey · registration", templateName: webui.TemplateWebAuthn, page: func() webui.Page { return qaWebAuthn(true) }, gallery: true},
	{name: "webauthn-assert", label: "Passkey · assertion", templateName: webui.TemplateWebAuthn, page: func() webui.Page { return qaWebAuthn(false) }, gallery: true},
	{name: "step-up", label: "Passkey · step-up", templateName: webui.TemplateStepUp, page: func() webui.Page { return webui.Page{Title: "Confirm your passkey", CSRFToken: "synthetic-csrf"} }, gallery: true},
	{name: "style-guide", label: "Foundation · component guide", templateName: webui.TemplateStyleGuide, page: qaLayout, gallery: true},
	{name: "layout", label: "Foundation · full layout", templateName: webui.TemplateLayout, page: qaLayout, gallery: true},
	{name: "layout-long", label: "Foundation · long layout", templateName: webui.TemplateLayout, page: qaLayoutLong, gallery: true},
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
	{name: "bindings-issued", label: "Bindings · one-time actions", templateName: webui.TemplateAccess, page: qaBindingsIssued, gallery: true},
	{name: "bindings-pending", label: "Bindings · pending and expired", templateName: webui.TemplateAccess, page: qaBindingsPending, gallery: true},
	{name: "bindings-unavailable", label: "Bindings · connection unavailable", templateName: webui.TemplateAccess, page: qaBindingsUnavailable, gallery: true},
	{name: "bindings-disabled", label: "Bindings · user disabled", templateName: webui.TemplateAccess, page: qaBindingsDisabled, gallery: true},
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
		return webui.Page{Title: "Backoffice UI previews · QA", Gallery: links, Navigation: qaAdminNavigation("")}, webui.TemplateGallery, http.StatusOK, true
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
		Overview: true, Account: true, ManageUsers: true, ManageMCP: true, ViewAudit: true,
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
		User:       &user, CSRFToken: "qa-csrf", Sessions: qaSessions(), BindingForms: qaBindingForms(user),
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

func qaLayout() webui.Page {
	return webui.Page{Title: "Layout foundation · QA", Navigation: qaAdminNavigation("")}
}

func qaLayoutLong() webui.Page {
	page := qaLayout()
	page.ViewerUsername = "administrator-with-a-very-long-username-for-responsive-review"
	page.Navigation[0].Label = "Overview of configured integrations and channel connections"
	page.Gallery = []webui.QALink{{Label: "A long synthetic activity entry with a user-provided label that must remain readable on a narrow screen", Path: "/overview"}, {Label: "A second activity entry with technical detail and explanatory text", Path: "/audit"}, {Label: "A third entry for checking spacing, wrapping and the normal-flow footer", Path: "/account"}}
	return page
}

func qaBindingForms(user webui.UserView) []webui.BindingForm {
	forms := make([]webui.BindingForm, 0, 4)
	for _, channel := range []string{"telegram", "slackagent", "zulip", "mattermost"} {
		forms = append(forms, webui.BindingForm{ChannelType: channel, Integration: usercmd.BindingIntegration{ChannelType: channel, Key: "synthetic-bot"}, UserID: user.ID, UserVersion: user.Version, CSRFToken: "qa-csrf", Ready: true, BotName: "Synthetic configured workspace", BotUsername: "synthetic-bot", CommandsEnabled: true, Preview: true})
	}
	return forms
}

func qaBindingsIssued() webui.Page {
	page := qaBindingsPending()
	for i := range page.BindingForms {
		form := &page.BindingForms[i]
		issued := usercmd.IssuedBindingInvitation{Payload: "bind_SYNTHETIC_PREVIEW_ONLY", Invitation: usercmd.BindingInvitation{ExpiresAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC).Add(24 * time.Hour)}}
		reveal := webui.ProjectBindingReveal(*form, issued)
		reveal.BotURL = "" // Preview never opens an external bot with a synthetic credential.
		form.Reveal = &reveal
	}
	return page
}
func qaBindingsPending() webui.Page {
	page := qaAccess()
	for i := range page.BindingForms {
		form := &page.BindingForms[i]
		form.Active = true
		form.Pending = []webui.BindingInvitationView{{ID: "pending-" + form.ChannelType, Version: 1, ExpiresAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC).Add(24 * time.Hour), Current: true}, {ID: "expired-" + form.ChannelType, Version: 1, ExpiresAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC).Add(-time.Hour), Expired: true}}
	}
	return page
}
func qaBindingsUnavailable() webui.Page {
	page := qaAccess()
	for i := range page.BindingForms {
		page.BindingForms[i].Ready = false
		page.BindingForms[i].BotUsername = ""
		page.BindingForms[i].Error = "The bot connection could not be verified. Retry connection."
	}
	return page
}
func qaBindingsDisabled() webui.Page {
	page := qaAccess()
	page.User.Status = "disabled"
	for i := range page.BindingForms {
		page.BindingForms[i].Disabled = true
	}
	return page
}

func qaWebAuthn(registration bool) webui.Page {
	return webui.Page{Title: "Verify your passkey", CSRFToken: "synthetic-csrf", Ceremony: &webui.MFACeremonyView{Registration: registration, OptionsJSON: `{"publicKey":{"challenge":"c3ludGhldGlj","rpId":"invalid.example"}}`, Transaction: "synthetic-invalid", FinishPath: "/webauthn-assert", CancelPath: "/account"}}
}

func qaMCPItem() mcpcmd.Item {
	return mcpcmd.Item{Connection: mcpcmd.Connection{ID: "qa-worker", PublicID: "synthetic-worker-tools", CurrentRevisionID: "qa-revision", Source: mcpcmd.SourceManaged, Version: 4, Enabled: true}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://mcp.example.test/worker", OAuth: true, Scopes: []string{"tools.read"}, Targets: mcpcmd.Targets{Providers: []string{"hosted-primary"}}, Headers: map[string]mcpcmd.ValueBinding{"X-Worker-Key": {Kind: mcpcmd.ValueProtected}, "X-Deployment": {Kind: mcpcmd.ValueEnvironment}}}, Status: mcpcmd.StatusUnavailable, Authorization: mcpcmd.GrantAuthorized}
}

func qaMCPState(status mcpcmd.Status, authorization mcpcmd.GrantStatus, transport mcpcmd.Transport) webui.Page {
	p := qaMCP()
	item := qaMCPItem()
	item.Status, item.Authorization, item.ToolCount = status, authorization, 3
	item.Definition.Transport = transport
	if authorization == "" {
		item.Definition.OAuth = false
	}
	if transport == mcpcmd.TransportStdio {
		item.Definition.Command, item.Definition.URL = "synthetic-tool-server", ""
		item.Definition.Scopes, item.Definition.Headers = nil, nil
	}
	p.MCP.Rows = nil
	p.MCP.Editor = webui.ProjectMCPEditor(item, nil, false)
	return p
}
func qaMCP() webui.Page {
	p := webui.Page{Title: "MCP servers · QA", Current: webui.LocationMCP, Navigation: qaAdminNavigation(webui.LocationMCP), CSRFToken: "qa-csrf", MCP: &webui.MCPView{}}
	item := qaMCPItem()
	p.MCP.Rows = append(p.MCP.Rows, webui.ProjectMCPRow(item))
	for _, status := range []mcpcmd.Status{mcpcmd.StatusReady, mcpcmd.StatusPending, mcpcmd.StatusDisabled, mcpcmd.StatusDeleted, mcpcmd.StatusConflict, mcpcmd.StatusAuthRequired, mcpcmd.StatusDisconnected} {
		item.Connection.PublicID = "synthetic-" + string(status)
		item.Status = status
		item.Connection.Deleted = status == mcpcmd.StatusDeleted
		item.ToolCount = 3
		item.Authorization = ""
		p.MCP.Rows = append(p.MCP.Rows, webui.ProjectMCPRow(item))
	}
	for _, reason := range []mcpcmd.RecoveryReason{mcpcmd.RecoveryAuthorizationRequired, mcpcmd.RecoveryFirstAuthorization, mcpcmd.RecoveryCaptureRequired} {
		item.Connection.ID = "config:qa-worker"
		item.Connection.PublicID = "configured-" + string(reason)
		item.Connection.Source = mcpcmd.SourceConfig
		item.Status = mcpcmd.StatusUnavailable
		item.Recovery = reason
		item.Connection.Deleted = false
		p.MCP.Rows = append(p.MCP.Rows, webui.ProjectMCPRow(item))
	}
	return p
}
func qaMCPEditor(create, configured bool) webui.Page {
	p := qaMCP()
	p.MCP.Rows = nil
	item := qaMCPItem()
	if configured {
		item.Connection.ID = "config:qa-worker"
		item.Connection.Source = mcpcmd.SourceConfig
		item.Recovery = mcpcmd.RecoveryCaptureRequired
		item.Authorization = ""
	}
	if create {
		item = mcpcmd.Item{}
	}
	p.MCP.Editor = webui.ProjectMCPEditor(item, []string{"hosted-primary", "acp-backup"}, create)
	if create {
		p.Title = "Add MCP server · QA"
	} else {
		p.Title = "Edit MCP server · QA"
	}
	return p
}

func qaMCPDevice(status mcpcmd.DeviceStatus, instructions bool) webui.Page {
	p := qaMCP()
	p.Title = "Worker authorization · synthetic preview"
	p.MCP = &webui.MCPView{Device: webui.ProjectMCPDevice(mcpcmd.DeviceAuthorization{ID: "qa-attempt", ConnectionID: "qa-worker", Status: status, UserCode: "INVALID-QA-CODE", VerificationURI: "https://issuer.example.test/verify", VerificationURIComplete: "https://issuer.example.test/verify?code=invalid-qa", ExpiresAt: time.Date(2026, 8, 1, 12, 10, 0, 0, time.UTC)}, instructions)}
	return p
}
