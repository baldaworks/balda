package backoffice

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/access"
	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// BindingInvitations is Backoffice's port to the shared invitation use case.
type BindingInvitations interface {
	Issue(ctx context.Context, actor usercmd.InvitationActor, userID string, version uint64, integration usercmd.BindingIntegration, replace bool) (usercmd.IssuedBindingInvitation, error)
	Pending(ctx context.Context, userID string) ([]usercmd.BindingInvitation, error)
	Cancel(ctx context.Context, actor usercmd.InvitationActor, userID, id string, version uint64) error
}

// BindingChannels reads or retries identity from configured transport adapters.
type BindingChannels interface {
	Get(channel string) (usercmd.BindingChannel, bool)
	Refresh(ctx context.Context, channel string) (usercmd.BindingChannel, error)
}

func (a *httpApp) bindingDetailPage(r *http.Request, principal security.Principal, userID string) (webui.Page, error) {
	actor := access.Actor{User: principal.User, SessionID: principal.FamilyID}
	user, err := a.access.GetUser(r.Context(), actor, userID)
	if err != nil {
		return webui.Page{}, err
	}
	sessionPage, err := a.access.ListSessions(r.Context(), actor, user.ID, usercmd.PageRequest{Limit: sessionPageSize, AfterID: r.URL.Query().Get("after_session")})
	if err != nil {
		return webui.Page{}, err
	}
	sessions := make([]webui.SessionView, 0, len(sessionPage.Sessions))
	for _, session := range sessionPage.Sessions {
		sessions = append(sessions, webui.ProjectSession(session, principal.FamilyID, time.Now().UTC()))
	}
	nextURL := ""
	if sessionPage.NextAfterID != "" {
		nextURL = "/access/users/" + url.PathEscape(user.ID) + "?after_session=" + url.QueryEscape(sessionPage.NextAfterID)
	}
	forms, err := a.projectBindingForms(r, user)
	if err != nil {
		return webui.Page{}, err
	}
	view := webui.ProjectUser(user)
	return webui.Page{Title: "Access · " + user.DisplayName, Current: webui.LocationAccess, ViewerUsername: principal.User.Username, Navigation: webui.Navigation(users.BackofficeCapabilities(principal.User), webui.LocationAccess), User: &view, Sessions: sessions, CSRFToken: a.browser.CSRFToken(r), BindingForms: forms, OwnUser: user.ID == principal.User.ID, SessionActionPrefix: "/access/users/" + url.PathEscape(user.ID) + "/sessions", SessionNextURL: nextURL}, nil
}

func (a *httpApp) projectBindingForms(r *http.Request, user usercmd.User) ([]webui.BindingForm, error) {
	var pending []usercmd.BindingInvitation
	if a.invitations != nil {
		var err error
		pending, err = a.invitations.Pending(r.Context(), user.ID)
		if err != nil {
			return nil, err
		}
	}
	infos := make([]usercmd.BindingChannel, len(a.bindingChoices))
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for i, channel := range a.bindingChoices {
		infos[i].Integration.ChannelType = channel
		if a.bindingChannels == nil {
			continue
		}
		if info, ok := a.bindingChannels.Get(channel); ok {
			infos[i] = info
		}
		if infos[i].Integration.Key != "" || r.Method != http.MethodGet {
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			if info, err := a.bindingChannels.Refresh(ctx, channel); err == nil {
				infos[i] = info
			}
		}()
	}
	workers.Wait()
	forms := make([]webui.BindingForm, 0, len(infos))
	for _, info := range infos {
		form := webui.ProjectBindingForm(info, user, a.browser.CSRFToken(r), pending, time.Now().UTC())
		if a.invitations == nil {
			form.Ready = false
		}
		forms = append(forms, form)
	}
	return forms, nil
}

func (a *httpApp) issueBindingInvitation(w http.ResponseWriter, r *http.Request, channel string) {
	values, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	page, err := a.bindingDetailPage(r, principal, r.PathValue("user_id"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	version, err := parseFormVersion(values.Get("expected_version"))
	if err != nil {
		a.bindingFormError(w, r, page, channel, err)
		return
	}
	for i := range page.BindingForms {
		form := &page.BindingForms[i]
		if form.ChannelType != channel {
			continue
		}
		if !form.Ready || a.invitations == nil {
			a.bindingFormError(w, r, page, channel, usercmd.ErrBindingInvitationUnavailable)
			return
		}
		issued, err := a.invitations.Issue(r.Context(), usercmd.InvitationActor{UserID: principal.User.ID, SessionID: principal.FamilyID}, page.User.ID, version, form.Integration, values.Get("replace") == checkedFormValue)
		if err != nil {
			a.bindingFormError(w, r, page, channel, err)
			return
		}
		retained := make([]webui.BindingInvitationView, 0, len(form.Pending)+1)
		for _, item := range form.Pending {
			if !item.Current {
				retained = append(retained, item)
			}
		}
		retained = append(retained, webui.BindingInvitationView{ID: issued.Invitation.ID, Version: issued.Invitation.Version, ExpiresAt: issued.Invitation.ExpiresAt, Current: true})
		form.Pending = retained
		form.Active = true
		reveal := webui.ProjectBindingReveal(*form, issued)
		form.Reveal = &reveal
		w.Header().Set("Cache-Control", "no-store")
		a.render(w, r, http.StatusOK, webui.TemplateAccess, page)
		return
	}
	a.browser.WriteError(w, r, usercmd.ErrInvalid)
}

func (a *httpApp) cancelBindingInvitation(w http.ResponseWriter, r *http.Request, channel string) {
	values, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	page, err := a.bindingDetailPage(r, principal, r.PathValue("user_id"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	version, err := parseFormVersion(values.Get("invitation_version"))
	if err != nil {
		a.bindingFormError(w, r, page, channel, err)
		return
	}
	if values.Get("confirm_cancel") != checkedFormValue {
		a.bindingFormError(w, r, page, channel, usercmd.ErrInvalid)
		return
	}
	found := false
	for _, form := range page.BindingForms {
		if form.ChannelType != channel {
			continue
		}
		for _, item := range form.Pending {
			if item.ID == values.Get("invitation_id") {
				found = true
			}
		}
	}
	if !found || a.invitations == nil {
		a.bindingFormError(w, r, page, channel, usercmd.ErrNotFound)
		return
	}
	err = a.invitations.Cancel(r.Context(), usercmd.InvitationActor{UserID: principal.User.ID, SessionID: principal.FamilyID}, page.User.ID, values.Get("invitation_id"), version)
	if err != nil {
		a.bindingFormError(w, r, page, channel, err)
		return
	}
	a.respondAccessDetailMutation(w, r, page.User.ID)
}

func (a *httpApp) refreshBindingIdentity(w http.ResponseWriter, r *http.Request, channel string) {
	_, principal, ok := a.browser.AdministratorMutation(w, r)
	if !ok {
		return
	}
	page, err := a.bindingDetailPage(r, principal, r.PathValue("user_id"))
	if err != nil {
		a.browser.WriteError(w, r, err)
		return
	}
	if a.bindingChannels == nil {
		a.bindingFormError(w, r, page, channel, usercmd.ErrBindingInvitationUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := a.bindingChannels.Refresh(ctx, channel); err != nil {
		a.bindingFormError(w, r, page, channel, usercmd.ErrBindingInvitationUnavailable)
		return
	}
	a.respondAccessDetailMutation(w, r, page.User.ID)
}

func (a *httpApp) bindingFormError(w http.ResponseWriter, r *http.Request, page webui.Page, channel string, err error) {
	status, message := http.StatusInternalServerError, "The invitation could not be updated. Refresh bindings and try again."
	switch {
	case errors.Is(err, usercmd.ErrBindingInvitationUnavailable):
		status, message = http.StatusServiceUnavailable, "The bot connection could not be verified. Retry connection, then generate an invitation."
	case errors.Is(err, usercmd.ErrInvalid):
		status, message = http.StatusBadRequest, "Review the form and required confirmation."
	case errors.Is(err, usercmd.ErrConflict):
		status, message = http.StatusConflict, "The user or invitation changed. Refresh bindings before trying again. Replacing a pending invitation requires confirmation."
	case errors.Is(err, usercmd.ErrForbidden):
		status, message = http.StatusForbidden, "This user or administrator is no longer allowed to create an invitation."
	case errors.Is(err, usercmd.ErrNotFound):
		status, message = http.StatusNotFound, "This invitation is no longer pending. Refresh bindings."
	}
	for i := range page.BindingForms {
		if page.BindingForms[i].ChannelType == channel {
			page.BindingForms[i].Error = message
			if errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
				page.BindingForms[i].Ready = false
			}
		}
	}
	a.render(w, r, status, webui.TemplateAccess, page)
}
