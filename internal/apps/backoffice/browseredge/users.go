package browseredge

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) usersPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Users(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/access/users/new") + `">Add user</a></p><ul>`
	for _, user := range inventory.Users {
		body += `<li><a href="` + a.path("/access/users/") + url.PathEscape(user.UserID) + `">` + escape(user.DisplayName) + `</a> · ` + escape(user.Username) + ` · ` + escape(string(user.Role)) + ` · ` + escape(string(user.Status)) + `</li>`
	}
	a.page(w, http.StatusOK, "Access", body+htmlListEnd)
}

func (a *App) newUserPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	a.page(w, http.StatusOK, "Add user", a.userForm(r, managementv1.UserRecord{}, true)+a.credentialForm(r, managementv1.UserRecord{}, true))
}

func (a *App) userPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	user, err := service.User(r.Context(), a.auth(r), r.PathValue("user_id"))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	sessions, err := service.UserSessions(r.Context(), a.auth(r), user.UserID)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	bindings, err := service.UserBindings(r.Context(), a.auth(r), user.UserID)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	channels, err := service.BindingChannels(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/access/users") + `">Access</a></p>` + a.userForm(r, user, false) + a.credentialForm(r, user, false) + `<h2>WebAuth sessions</h2><ul>`
	for _, session := range sessions.Sessions {
		body += `<li><code>` + escape(session.WebAuthSessionID) + `</code> · ` + escape(string(session.Assurance)) + `<form method="post" action="` + a.path("/access/users/") + url.PathEscape(user.UserID) + `/sessions/` + url.PathEscape(session.WebAuthSessionID) + `/revoke"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><label><input type="checkbox" name="confirm_current" value="yes"> Confirm current session</label><button>Revoke</button></form></li>`
	}
	body += htmlListEnd + a.bindingControls(r, user, bindings, channels)
	a.page(w, http.StatusOK, user.DisplayName, body)
}

func (a *App) createUser(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	password := form.Get("temporary_password")
	request := managementv1.UserCreateRequest{DisplayName: strings.TrimSpace(form.Get("display_name")), Username: strings.TrimSpace(form.Get("username")), TemporaryPassword: &password, Role: managementv1.WebAuthRole(form.Get("role")), Status: managementv1.UserStatus(form.Get("status"))}
	if request.DisplayName == "" || request.Username == "" || password == "" || !request.Role.Valid() || !request.Status.Valid() {
		a.failure(w, http.StatusBadRequest)
		return
	}
	user, err := service.CreateUser(r.Context(), a.auth(r), request)
	password = ""
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users/"+url.PathEscape(user.UserID))
}

func (a *App) updateUser(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(form.Get("expected_version"), 10, 64)
	request := managementv1.UserUpdateRequest{DisplayName: strings.TrimSpace(form.Get("display_name")), Username: strings.TrimSpace(form.Get("username")), Role: managementv1.WebAuthRole(form.Get("role")), Status: managementv1.UserStatus(form.Get("status")), ExpectedVersion: version, AcknowledgeBotAccessImpact: form.Get("acknowledge_bot_access_impact") == formYesValue}
	if err != nil || version == 0 || request.DisplayName == "" || request.Username == "" || !request.Role.Valid() || !request.Status.Valid() {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	userID := r.PathValue("user_id")
	if _, err := service.UpdateUser(r.Context(), a.auth(r), userID, request); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users/"+url.PathEscape(userID))
}

func (a *App) resetUserCredential(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	userVersion, userErr := strconv.ParseUint(form.Get("expected_user_version"), 10, 64)
	credentialVersion, credentialErr := strconv.ParseUint(form.Get("expected_credential_version"), 10, 64)
	password := form.Get("temporary_password")
	if userErr != nil || credentialErr != nil || userVersion == 0 || credentialVersion == 0 || password == "" {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	userID := r.PathValue("user_id")
	err := service.ResetUserCredential(r.Context(), a.auth(r), userID, managementv1.UserCredentialResetRequest{TemporaryPassword: &password, ExpectedUserVersion: userVersion, ExpectedCredentialVersion: credentialVersion, ConfirmCurrent: form.Get("confirm_current") == formYesValue})
	password = ""
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users/"+url.PathEscape(userID))
}

func (a *App) revokeUserSession(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	if err := a.service.RevokeSession(r.Context(), a.auth(r), r.PathValue("session_id"), form.Get("confirm_current") == formYesValue); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users/"+url.PathEscape(r.PathValue("user_id")))
}

func (a *App) deleteUserBinding(w http.ResponseWriter, r *http.Request) {
	form, service, userID, ok := a.userMutation(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(form.Get("expected_user_version"), 10, 64)
	if err != nil || version == 0 || form.Get("confirm_impact") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	err = service.DeleteUserBinding(r.Context(), a.auth(r), userID, r.PathValue("binding_id"), managementv1.BindingDeleteRequest{
		ExpectedUserVersion: version, ConfirmImpact: managementv1.BindingDeleteRequestConfirmImpact(true),
	})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users/"+url.PathEscape(userID))
}

func (a *App) issueBindingInvitation(w http.ResponseWriter, r *http.Request) {
	form, service, userID, ok := a.userMutation(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(form.Get("expected_user_version"), 10, 64)
	channel := strings.TrimSpace(form.Get("channel_type"))
	if err != nil || version == 0 || channel == "" {
		a.failure(w, http.StatusBadRequest)
		return
	}
	issued, err := service.IssueBindingInvitation(r.Context(), a.auth(r), userID, managementv1.BindingInvitationIssueRequest{
		ChannelType: channel, ExpectedUserVersion: version, Replace: form.Get("replace") == formYesValue,
	})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.page(w, http.StatusOK, "Binding invitation", `<p>This proof is shown once.</p><pre>`+escape(issued.Proof)+`</pre><p><a href="`+a.path("/access/users/")+url.PathEscape(userID)+`">Return to user</a></p>`)
}

func (a *App) cancelBindingInvitation(w http.ResponseWriter, r *http.Request) {
	form, service, userID, ok := a.userMutation(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(form.Get("expected_invitation_version"), 10, 64)
	if err != nil || version == 0 || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	err = service.CancelBindingInvitation(r.Context(), a.auth(r), userID, r.PathValue("invitation_id"), managementv1.BindingInvitationCancelRequest{
		ExpectedInvitationVersion: version, Confirm: managementv1.BindingInvitationCancelRequestConfirm(true),
	})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users/"+url.PathEscape(userID))
}

func (a *App) userMutation(w http.ResponseWriter, r *http.Request) (url.Values, UserService, string, bool) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return nil, nil, "", false
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return nil, nil, "", false
	}
	return form, service, r.PathValue("user_id"), true
}

func (a *App) refreshBindingChannel(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.mutationForm(w, r); !ok {
		return
	}
	service, ok := a.service.(UserService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if _, err := service.RefreshBindingChannel(r.Context(), a.auth(r), r.PathValue("channel_type")); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/access/users")
}

func (a *App) bindingControls(r *http.Request, user managementv1.UserRecord, inventory managementv1.UserBindingInventory, channels managementv1.BindingChannelInventory) string {
	csrf := escape(cookieValue(r, csrfCookie))
	body := `<h2>Bindings</h2><ul>`
	for _, binding := range inventory.Bindings {
		body += `<li>` + escape(binding.ChannelType) + ` · <code>` + escape(binding.Principal) + `</code><form method="post" action="` + a.path("/access/users/") + url.PathEscape(user.UserID) + `/bindings/` + url.PathEscape(binding.BindingID) + `/delete"><input type="hidden" name="csrf_token" value="` + csrf + `"><input type="hidden" name="expected_user_version" value="` + strconv.FormatUint(user.Version, 10) + `"><label><input type="checkbox" name="confirm_impact" value="yes" required> Confirm bot access impact</label><button>Remove</button></form></li>`
	}
	body += `</ul><h2>Pending invitations</h2><ul>`
	for _, invitation := range inventory.Invitations {
		body += `<li>` + escape(invitation.ChannelType) + ` · expires ` + escape(invitation.ExpiresAt.UTC().Format(time.RFC3339)) + `<form method="post" action="` + a.path("/access/users/") + url.PathEscape(user.UserID) + `/binding-invitations/` + url.PathEscape(invitation.InvitationID) + `/cancel"><input type="hidden" name="csrf_token" value="` + csrf + `"><input type="hidden" name="expected_invitation_version" value="` + strconv.FormatUint(invitation.Version, 10) + `"><input type="hidden" name="confirm" value="yes"><button>Cancel</button></form></li>`
	}
	body += `</ul><h2>New invitation</h2>`
	for _, channel := range channels.Channels {
		if channel.Ready {
			body += `<form method="post" action="` + a.path("/access/users/") + url.PathEscape(user.UserID) + `/binding-invitations"><input type="hidden" name="csrf_token" value="` + csrf + `"><input type="hidden" name="expected_user_version" value="` + strconv.FormatUint(user.Version, 10) + `"><input type="hidden" name="channel_type" value="` + escape(channel.ChannelType) + `"><label><input type="checkbox" name="replace" value="yes"> Replace pending</label><button>Issue for ` + escape(channel.ChannelType) + `</button></form>`
			continue
		}
		body += `<form method="post" action="` + a.path("/binding-channels/") + url.PathEscape(channel.ChannelType) + `/refresh"><input type="hidden" name="csrf_token" value="` + csrf + `"><button>Refresh ` + escape(channel.ChannelType) + ` identity</button></form>`
	}
	return body
}

func (a *App) userForm(r *http.Request, user managementv1.UserRecord, create bool) string {
	action := a.path("/access/users")
	if !create {
		action += "/" + url.PathEscape(user.UserID)
	}
	body := `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `">`
	if !create {
		body += `<input type="hidden" name="expected_version" value="` + strconv.FormatUint(user.Version, 10) + `">`
	}
	body += `<label>Display name <input name="display_name" value="` + escape(user.DisplayName) + `" maxlength="256" required></label><label>Username <input name="username" value="` + escape(user.Username) + `" maxlength="256" required></label>`
	body += `<label>Role <select name="role"><option value="operator"` + selected(string(user.Role), "operator", create) + `>operator</option><option value="administrator"` + selected(string(user.Role), "administrator", false) + `>administrator</option></select></label>`
	body += `<label>Status <select name="status"><option value="active"` + selected(string(user.Status), "active", create) + `>active</option><option value="disabled"` + selected(string(user.Status), "disabled", false) + `>disabled</option></select></label>`
	if !create {
		body += `<label><input type="checkbox" name="acknowledge_bot_access_impact" value="yes"> Acknowledge bot access impact</label>`
	}
	if create {
		body += `<label>Temporary password <input type="password" name="temporary_password" minlength="12" maxlength="72" required></label>`
	}
	return body + `<button>Save</button></form>`
}

func (a *App) credentialForm(r *http.Request, user managementv1.UserRecord, create bool) string {
	if create {
		return ""
	}
	return `<h2>Reset credential</h2><form method="post" action="` + a.path("/access/users/") + url.PathEscape(user.UserID) + `/credential-reset"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_user_version" value="` + strconv.FormatUint(user.Version, 10) + `"><input type="hidden" name="expected_credential_version" value="` + strconv.FormatUint(user.CredentialVersion, 10) + `"><label>Temporary password <input type="password" name="temporary_password" minlength="12" maxlength="72" required></label><label><input type="checkbox" name="confirm_current" value="yes"> Confirm current session</label><button>Reset</button></form>`
}

func selected(actual, expected string, defaultValue bool) string {
	if actual == expected || (actual == "" && defaultValue) {
		return selectedAttribute
	}
	return ""
}
