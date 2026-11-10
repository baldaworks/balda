package browseredge

import (
	"errors"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) aliasesPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(AliasService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	inventory, err := service.Aliases(r.Context(), a.auth(r))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a> · <a href="` + a.path("/aliases/new") + `">Add alias</a></p><ul>`
	for _, record := range inventory.Items {
		body += `<li><a href="` + a.path("/aliases/") + url.PathEscape(record.Name) + `">` + escape(record.Name) + `</a> → <code>` + escape(record.LocatorRef) + `</code></li>`
	}
	a.page(w, http.StatusOK, "Aliases", body+`</ul>`)
}
func (a *App) newAliasPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	a.page(w, http.StatusOK, "Add alias", a.aliasForm(r, managementv1.AliasRecord{}, true))
}
func (a *App) aliasPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(AliasService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	record, err := service.Alias(r.Context(), a.auth(r), r.PathValue("alias_name"))
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.page(w, http.StatusOK, record.Name, `<p><a href="`+a.path("/aliases")+`">Aliases</a></p>`+a.aliasForm(r, record, false))
}
func (a *App) createAlias(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	name, locator := strings.TrimSpace(form.Get("name")), strings.TrimSpace(form.Get("locator_ref"))
	if name == "" || locator == "" {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(AliasService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	record, err := service.CreateAlias(r.Context(), a.auth(r), managementv1.AliasCreateRequest{Name: name, LocatorRef: locator})
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/aliases/"+url.PathEscape(record.Name))
}
func (a *App) retargetAlias(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := aliasVersionFromForm(form)
	locator := strings.TrimSpace(form.Get("locator_ref"))
	if err != nil || locator == "" {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(AliasService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("alias_name")
	if _, err := service.RetargetAlias(r.Context(), a.auth(r), name, managementv1.AliasRetargetRequest{ExpectedVersion: version, LocatorRef: locator}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/aliases/"+url.PathEscape(name))
}
func (a *App) deleteAlias(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	version, err := aliasVersionFromForm(form)
	if err != nil || form.Get("confirm") != formYesValue {
		a.failure(w, http.StatusBadRequest)
		return
	}
	service, ok := a.service.(AliasService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	if err := service.DeleteAlias(r.Context(), a.auth(r), r.PathValue("alias_name"), managementv1.AliasDeleteRequest{ExpectedVersion: version}); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/aliases")
}
func (a *App) aliasForm(r *http.Request, record managementv1.AliasRecord, create bool) string {
	action := a.path("/aliases")
	if !create {
		action += "/" + url.PathEscape(record.Name)
	}
	body := `<form method="post" action="` + action + `"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `">`
	if create {
		body += nameInputLabel
	} else {
		body += `<input type="hidden" name="expected_version" value="` + strconv.FormatUint(record.Version, 10) + `">`
	}
	body += `<label>Locator <input name="locator_ref" value="` + escape(record.LocatorRef) + `" maxlength="1024" required></label><button>Save</button></form>`
	if !create {
		body += `<form method="post" action="` + action + `/delete"><input type="hidden" name="csrf_token" value="` + escape(cookieValue(r, csrfCookie)) + `"><input type="hidden" name="expected_version" value="` + strconv.FormatUint(record.Version, 10) + `"><label><input type="checkbox" name="confirm" value="yes" required> Confirm deletion</label><button>Delete</button></form>`
	}
	return body
}
func aliasVersionFromForm(form url.Values) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version == 0 {
		return 0, errors.New("invalid alias version")
	}
	return version, nil
}
