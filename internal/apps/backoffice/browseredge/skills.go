package browseredge

import (
	"net/http"
	"net/url"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) skillsPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Skills(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Skills", a.skillInventory(r, inventory))
}

func (a *App) skillInstallPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Skills(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/skills") + `">Skills</a></p><form method="post" action="` + a.path("/skills/reviews/install") + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_state" value="` + escape(inventory.ExpectedState) + `"><label>Source <input name="source" maxlength="4096" required></label><label>Ref <input name="ref" maxlength="512"></label><label>Skill path <input name="path" maxlength="1024"></label><button>Review</button></form>`
	a.page(w, http.StatusOK, "Install skill", body)
}

func (a *App) skillPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Skills(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	id := r.PathValue("skill_id")
	for _, item := range inventory.Items {
		if item.Ownership == managementv1.SkillOwnershipManaged && item.SourceName == id {
			a.page(w, http.StatusOK, item.Name, a.skillDetail(r, item, inventory.ExpectedState))
			return
		}
	}
	a.failure(w, http.StatusNotFound)
}

func (a *App) reviewSkillInstall(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	candidate, err := service.ReviewSkillInstall(r.Context(), a.auth(r), managementv1.SkillInstallReviewRequest{
		Source: form.Get("source"), Ref: form.Get("ref"), Path: form.Get("path"), ExpectedState: form.Get("expected_state"),
	})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Review skill", a.skillCandidate(r, candidate))
}

func (a *App) reviewSkillUpdate(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	candidate, err := service.ReviewSkillUpdate(r.Context(), a.auth(r), r.PathValue("skill_id"), managementv1.SkillObservedStateRequest{ExpectedState: form.Get("expected_state")})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Review skill update", a.skillCandidate(r, candidate))
}

func (a *App) acceptSkillReview(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	outcome, err := service.AcceptSkillReview(r.Context(), a.auth(r), managementv1.SkillAcceptRequest{CandidateHandle: form.Get("candidate_handle")})
	a.skillMutationOutcome(w, r, outcome, err)
}

func (a *App) mutateSkill(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	operation := managementv1.SkillLifecycleOperation(form.Get("operation"))
	if (operation == managementv1.SkillLifecycleOperationDisable || operation == managementv1.SkillLifecycleOperationRemove) && form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	outcome, err := service.MutateSkill(r.Context(), a.auth(r), r.PathValue("skill_id"), managementv1.SkillMutationRequest{
		Operation: operation, ExpectedState: form.Get("expected_state"), Confirm: form.Get("confirm") == formYesValue,
	})
	a.skillMutationOutcome(w, r, outcome, err)
}

func (a *App) refreshSkills(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	outcome, err := service.RefreshSkills(r.Context(), a.auth(r), managementv1.SkillObservedStateRequest{ExpectedState: form.Get("expected_state")})
	a.skillMutationOutcome(w, r, outcome, err)
}

func (a *App) retrySkillPublication(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.mutationForm(w, r); !ok {
		return
	}
	service, ok := a.service.(SkillService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	outcome, err := service.RetrySkillPublication(r.Context(), a.auth(r))
	a.skillMutationOutcome(w, r, outcome, err)
}

func (a *App) skillMutationOutcome(w http.ResponseWriter, r *http.Request, outcome managementv1.SkillMutationOutcome, err error) {
	if outcome.Accepted && outcome.PublicationPending {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if outcome.Item != nil && outcome.Item.Ownership == managementv1.SkillOwnershipManaged {
		a.redirect(w, "/skills/"+url.PathEscape(outcome.Item.SourceName))
		return
	}
	a.redirect(w, "/skills")
}

func (a *App) skillInventory(r *http.Request, inventory managementv1.SkillInventory) string {
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/skills/install") + `">Install skill</a></p>`
	if inventory.PublicationPending {
		body += `<form method="post" action="` + a.path("/skills/publication/retry") + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><button>Retry publication</button></form>`
	}
	body += `<form method="post" action="` + a.path("/skills/refresh") + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_state" value="` + escape(inventory.ExpectedState) + `"><button>Refresh unmanaged skills</button></form><ul>`
	for _, item := range inventory.Items {
		label := escape(item.Name) + ` · ` + escape(string(item.Ownership)) + ` · ` + escape(item.SourceLabel)
		if item.Ownership == managementv1.SkillOwnershipManaged {
			label = `<a href="` + a.path("/skills/") + url.PathEscape(item.SourceName) + `">` + escape(item.Name) + `</a> · managed`
		}
		body += `<li>` + label + `</li>`
	}
	return body + `</ul>`
}

func (a *App) skillDetail(r *http.Request, item managementv1.SkillEntry, expectedState string) string {
	action := a.path("/skills/") + url.PathEscape(item.SourceName)
	body := `<p><a href="` + a.path("/skills") + `">Skills</a></p><p>` + escape(item.Description) + `</p><form method="post" action="` + action + `/reviews/update"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_state" value="` + escape(expectedState) + `"><button>Review update</button></form>`
	for _, operation := range []managementv1.SkillLifecycleOperation{managementv1.SkillLifecycleOperationEnable, managementv1.SkillLifecycleOperationDisable, managementv1.SkillLifecycleOperationRemove} {
		confirm := ""
		if operation != managementv1.SkillLifecycleOperationEnable {
			confirm = `<label><input type="checkbox" name="confirm" value="yes" required> Confirm</label>`
		}
		body += `<form method="post" action="` + action + `/lifecycle"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="operation" value="` + string(operation) + `"><input type="hidden" name="expected_state" value="` + escape(expectedState) + `">` + confirm + `<button>` + string(operation) + `</button></form>`
	}
	return body
}

func (a *App) skillCandidate(r *http.Request, candidate managementv1.SkillCandidate) string {
	return `<p><a href="` + a.path("/skills") + `">Skills</a></p><p><strong>` + escape(candidate.Item.Name) + `</strong> · revision <code>` + escape(candidate.Item.Revision) + `</code></p><form method="post" action="` + a.path("/skills/reviews/accept") + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="candidate_handle" value="` + escape(candidate.CandidateHandle) + `"><button>Accept</button></form>`
}
