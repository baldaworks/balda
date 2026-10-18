package backoffice

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const reopenSkillLabel = "Reopen skill"

func (a *httpApp) registerSkills(mux *http.ServeMux) {
	for _, route := range []string{"/skills", "/skills/install", "/skills/managed/{skill_id}", "/skills/contributions/{skill_token}"} {
		mux.Handle("GET "+a.path(route), a.browser.Authenticate(a.browser.RequireAdministrator(http.HandlerFunc(a.skillsPage))))
	}
	for route, handler := range map[string]http.HandlerFunc{
		"/skills/review": a.skillReviewInstall, "/skills/managed/{skill_id}/review-update": a.skillReviewUpdate,
		"/skills/accept": a.skillAccept, "/skills/managed/{skill_id}/{skill_operation}": a.skillChange,
		"/skills/refresh": a.skillRefresh, "/skills/retry-publication": a.skillRetry,
	} {
		mux.HandleFunc("POST "+a.path(route), handler)
	}
}

func skillAuthority(p security.Principal) skillcmd.Authority {
	return skillcmd.NewBrowserAuthority(p.User.ID, p.User.Version, p.User.Credential.Version, p.MFAVersion, p.FamilyID, p.Version, time.Now().UTC())
}

func (a *httpApp) skillBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Skills · Balda", ViewerUsername: p.User.Username, CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationSkills, Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationSkills), Skills: &webui.SkillsView{}, RestartURL: a.path("/skills"), RestartLabel: "Reopen Skills"}
}

func (a *httpApp) readSkills(r *http.Request, p security.Principal, page *webui.Page) error {
	if a.skills == nil {
		return skillcmd.ErrUnavailable
	}
	inventory, err := a.skills.Inventory(r.Context(), skillAuthority(p))
	if err != nil {
		return err
	}
	page.Skills = webui.ProjectSkills(inventory)
	return nil
}

func (a *httpApp) skillsPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.skillBasePage(r, p)
	if err := a.readSkills(r, p, &page); err != nil {
		a.skillError(w, r, page, err)
		return
	}
	switch {
	case r.URL.Path == a.path("/skills/install"):
		page.Title = "Install skill · Balda"
		page.Skills.Install = true
		page.RestartURL, page.RestartLabel = a.path("/skills/install"), reopenInstallLabel
	case r.PathValue("skill_id") != "":
		if err := selectManagedSkill(&page, r.PathValue("skill_id")); err != nil {
			a.skillError(w, r, page, err)
			return
		}
	case r.PathValue("skill_token") != "":
		id, err := webui.SkillContributionToken(r.PathValue("skill_token"))
		if err != nil {
			a.skillError(w, r, page, err)
			return
		}
		found := false
		for _, row := range page.Skills.Rows {
			if row.ID == id && row.Ownership != skillcmd.OwnershipManaged {
				page.Skills.Detail = &row
				found = true
				break
			}
		}
		if !found {
			a.skillError(w, r, page, skillcmd.ErrNotFound)
			return
		}
	}
	if page.Skills.Detail != nil {
		page.Title = page.Skills.Detail.Name + " · Skills · Balda"
		page.RestartURL, page.RestartLabel = a.path(page.Skills.Detail.DetailPath), reopenSkillLabel
	}
	a.render(w, r, http.StatusOK, webui.TemplateSkills, page)
}

func selectManagedSkill(page *webui.Page, id string) error {
	for _, row := range page.Skills.Rows {
		if row.Ownership == skillcmd.OwnershipManaged && row.ID.Source.Name == id {
			page.Skills.Detail = &row
			return nil
		}
	}
	return skillcmd.ErrNotFound
}

func skillMutation(r *http.Request, state string, p security.Principal) skillcmd.Mutation {
	return skillcmd.Mutation{ID: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: r.PathValue("skill_id")}, ExpectedState: strings.TrimSpace(state), Authority: skillAuthority(p)}
}

func (a *httpApp) skillReviewInstall(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.skillBasePage(r, p)
	page.Skills.Install = true
	page.Skills.ExpectedState = form.Get("expected_state")
	page.RestartURL, page.RestartLabel = a.path("/skills/install"), reopenInstallLabel
	if a.skills == nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
		return
	}
	candidate, err := a.skills.ReviewInstall(r.Context(), SkillsInstall{Source: form.Get("source"), Ref: form.Get("ref"), Path: form.Get("path"), ExpectedState: form.Get("expected_state"), Authority: skillAuthority(p)})
	if err != nil {
		a.skillError(w, r, page, err)
		return
	}
	page.Skills.Install = false
	page.Skills.Review = &candidate
	a.render(w, r, http.StatusOK, webui.TemplateSkills, page)
}

func (a *httpApp) skillReviewUpdate(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.skillBasePage(r, p)
	page.RestartURL, page.RestartLabel = a.path("/skills/managed/"+r.PathValue("skill_id")), reopenSkillLabel
	if a.skills == nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
		return
	}
	candidate, err := a.skills.ReviewUpdate(r.Context(), skillMutation(r, form.Get("expected_state"), p))
	if err != nil {
		a.skillError(w, r, page, err)
		return
	}
	page.Skills.Update = true
	page.Skills.Review = &candidate
	a.render(w, r, http.StatusOK, webui.TemplateSkills, page)
}

func (a *httpApp) skillAccept(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.skillBasePage(r, p)
	page.RestartURL, page.RestartLabel = a.path("/skills/install"), reopenInstallLabel
	if id := form.Get("skill_id"); id != "" {
		// Only a current managed identity may choose contextual recovery. It is
		// presentation input, never the identity accepted by the opaque handle.
		if err := a.readSkills(r, p, &page); err != nil {
			a.skillError(w, r, page, err)
			return
		}
		if selectManagedSkill(&page, id) == nil {
			page.RestartURL, page.RestartLabel = a.path(page.Skills.Detail.DetailPath), reopenSkillLabel
		}
		page.Skills.Detail = nil
	}
	if a.skills == nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
		return
	}
	outcome, err := a.skills.Accept(r.Context(), form.Get("candidate_handle"), skillAuthority(p))
	a.skillOutcome(w, r, p, page, outcome, err)
}

func (a *httpApp) skillChange(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.skillBasePage(r, p)
	page.RestartURL, page.RestartLabel = a.path("/skills/managed/"+r.PathValue("skill_id")), reopenSkillLabel
	operation := skillcmd.Operation(r.PathValue("skill_operation"))
	if operation != skillcmd.OperationEnable && operation != skillcmd.OperationDisable && operation != skillcmd.OperationRemove || (operation == skillcmd.OperationDisable || operation == skillcmd.OperationRemove) && form.Get("confirm") != checkedFormValue {
		a.skillError(w, r, page, skillcmd.ErrInvalid)
		return
	}
	if a.skills == nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
		return
	}
	outcome, err := a.skills.Change(r.Context(), operation, skillMutation(r, form.Get("expected_state"), p))
	a.skillOutcome(w, r, p, page, outcome, err)
}

func (a *httpApp) skillRefresh(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.skillBasePage(r, p)
	if a.skills == nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
		return
	}
	outcome, err := a.skills.Refresh(r.Context(), skillcmd.Mutation{ExpectedState: form.Get("expected_state"), Authority: skillAuthority(p)})
	a.skillOutcome(w, r, p, page, outcome, err)
}

func (a *httpApp) skillRetry(w http.ResponseWriter, r *http.Request) {
	_, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.skillBasePage(r, p)
	if a.skills == nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
		return
	}
	outcome, err := a.skills.RetryPublication(r.Context(), skillAuthority(p))
	a.skillOutcome(w, r, p, page, outcome, err)
}

func (a *httpApp) skillOutcome(w http.ResponseWriter, r *http.Request, p security.Principal, page webui.Page, outcome skillcmd.Outcome, err error) {
	if outcome.Accepted && outcome.Pending {
		page.RestartURL, page.RestartLabel = a.path("/skills"), "Reopen Skills"
		if outcome.Entry != nil {
			page.RestartURL, page.RestartLabel = a.path(webui.SkillDetailPath(*outcome.Entry)), reopenSkillLabel
		}
		_ = a.readSkills(r, p, &page)
		page.Skills.Pending = true
		page.Skills.MutationBlocked = true
		page.Error = &webui.ErrorView{Heading: "Accepted; recovery pending", Message: "The change was saved. Catalog activation has not completed. Recover publication or reopen Skills; do not repeat the original action."}
		a.render(w, r, http.StatusServiceUnavailable, webui.TemplateSkills, page)
		return
	}
	if err != nil {
		a.skillError(w, r, page, err)
		return
	}
	location := "/skills"
	if outcome.Entry != nil {
		location = webui.SkillDetailPath(*outcome.Entry)
	}
	if err := webui.RespondMutationPath(w, r, a.path(location)); err != nil {
		a.skillError(w, r, page, skillcmd.ErrUnavailable)
	}
}

func (a *httpApp) skillError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := http.StatusServiceUnavailable, "The operation could not be confirmed. Reopen Skills and check the current state before retrying."
	switch {
	case errors.Is(err, skillcmd.ErrInvalid):
		status, message = http.StatusBadRequest, "Check Source, Ref and Skill path, and confirm the requested action. Source is cleared after submission."
	case errors.Is(err, skillcmd.ErrForbidden):
		status, message = http.StatusForbidden, "Your administrator authority changed. Reopen Skills or sign in again."
	case errors.Is(err, skillcmd.ErrNotFound):
		status, message = http.StatusNotFound, "This skill is no longer available. Reopen Skills."
	case errors.Is(err, skillcmd.ErrConflict):
		status, message = http.StatusConflict, "Skills or the reviewed source changed, or the review expired. Reopen and review the current state before trying again."
	}
	page.Error = &webui.ErrorView{Heading: "Skill operation could not complete", Message: message}
	if status == http.StatusConflict {
		page.Skills.MutationBlocked = true
	}
	if !page.Skills.Install && page.Skills.Review == nil && page.Skills.Detail == nil && len(page.Skills.Rows) == 0 {
		page.Skills.Unavailable = true
	}
	a.render(w, r, status, webui.TemplateSkills, page)
}
