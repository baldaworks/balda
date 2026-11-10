package browseredge

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) memoryPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(MemoryService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	query := r.URL.Query().Get("q")
	if len(query) > 4096 {
		a.failure(w, http.StatusBadRequest)
		return
	}
	inventory, err := service.MemoryFacts(r.Context(), a.auth(r), query)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/memory/new") + `">Add fact</a></p>`
	if !inventory.Enabled {
		a.page(w, http.StatusOK, "Fact memory", body+`<p>Fact memory is disabled.</p>`)
		return
	}
	body += `<form method="get" action="` + a.path("/memory") + `"><label>Filter <input name="q" value="` + escape(query) + `"></label><button>Search</button></form><ul>`
	for _, fact := range inventory.Facts {
		body += `<li><a href="` + a.path("/memory/") + strconv.FormatInt(fact.ID, 10) + `">` + escape(fact.Fact) + `</a></li>`
	}
	if len(inventory.Facts) == 0 {
		body += `<li>No facts yet.</li>`
	}
	a.page(w, http.StatusOK, "Fact memory", body+`</ul>`)
}

func (a *App) newMemoryFactPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(MemoryService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.MemoryFacts(r.Context(), a.auth(r), "")
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	if !inventory.Enabled {
		a.page(w, http.StatusOK, "Fact memory", `<p><a href="`+a.path("/memory")+`">Fact memory</a></p><p>Fact memory is disabled.</p>`)
		return
	}
	a.page(w, http.StatusOK, "Add fact", a.memoryFactForm(r, 0, "", 0, true, ""))
}

func (a *App) memoryFactPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	id, err := parseMemoryFactID(r.PathValue("fact_id"))
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(MemoryService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	editor, err := service.MemoryFact(r.Context(), a.auth(r), id)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, "Edit fact", a.memoryFactForm(r, id, editor.Fact.Fact, editor.ExpectedVersion, false, ""))
}

func (a *App) createMemoryFact(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationFormLimit(w, r, (1<<20)+(4<<10))
	if !ok {
		return
	}
	fact := form.Get("fact")
	service, ok := a.service.(MemoryService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if _, err := service.CreateMemoryFact(r.Context(), a.auth(r), managementv1.MemoryFactCreateRequest{Fact: fact}); err != nil {
		a.memoryMutationFailure(w, r, err, 0, fact, 0, true)
		return
	}
	a.redirect(w, "/memory")
}

func (a *App) updateMemoryFact(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationFormLimit(w, r, (1<<20)+(4<<10))
	if !ok {
		return
	}
	id, version, err := memoryMutationIdentity(r.PathValue("fact_id"), form)
	if err != nil {
		a.failure(w, http.StatusBadRequest)
		return
	}
	fact := form.Get("fact")
	service, ok := a.service.(MemoryService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if _, err := service.UpdateMemoryFact(r.Context(), a.auth(r), id, managementv1.MemoryFactUpdateRequest{Fact: fact, ExpectedVersion: version}); err != nil {
		a.memoryMutationFailure(w, r, err, id, fact, version, false)
		return
	}
	a.redirect(w, "/memory/"+strconv.FormatInt(id, 10))
}

func (a *App) deleteMemoryFact(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	id, version, err := memoryMutationIdentity(r.PathValue("fact_id"), form)
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(MemoryService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.DeleteMemoryFact(r.Context(), a.auth(r), id, managementv1.MemoryFactDeleteRequest{ExpectedVersion: version, Confirm: true}); err != nil {
		a.memoryMutationFailure(w, r, err, id, "", version, false)
		return
	}
	a.redirect(w, "/memory")
}

func (a *App) memoryFactForm(r *http.Request, id int64, fact string, version int64, create bool, message string) string {
	action := a.path("/memory")
	if !create {
		action += "/" + strconv.FormatInt(id, 10)
	}
	body := `<p><a href="` + a.path("/memory") + `">Fact memory</a></p>`
	if message != "" {
		body += `<p>` + escape(message) + `</p>`
	}
	body += `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `">`
	if !create {
		body += `<input type="hidden" name="expected_version" value="` + strconv.FormatInt(version, 10) + `">`
	}
	body += `<label>Fact <textarea name="fact" maxlength="1048576" required>` + escape(fact) + `</textarea></label><button>Save</button></form>`
	if !create {
		body += `<form method="post" action="` + action + `/delete"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatInt(version, 10) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm deletion</label><button>Delete</button></form>`
	}
	return body
}

func (a *App) memoryMutationFailure(w http.ResponseWriter, r *http.Request, err error, id int64, fact string, version int64, create bool) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, managementclient.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, managementclient.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, managementclient.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, managementclient.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, managementclient.ErrUnavailable):
		status = http.StatusServiceUnavailable
	}
	a.page(w, status, "Fact memory operation failed", a.memoryFactForm(r, id, fact, version, create, "Reopen Fact memory and check its current state before retrying."))
}

func parseMemoryFactID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, errors.New("invalid fact id")
	}
	return id, nil
}

func memoryMutationIdentity(raw string, form url.Values) (int64, int64, error) {
	id, err := parseMemoryFactID(raw)
	if err != nil {
		return 0, 0, err
	}
	version, err := strconv.ParseInt(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version <= 0 {
		return 0, 0, errors.New("invalid memory version")
	}
	return id, version, nil
}
