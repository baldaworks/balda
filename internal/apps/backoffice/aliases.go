package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// AliasesOperations is Backoffice's consuming port for managed destinations.
type AliasesOperations interface {
	List(ctx context.Context, authority aliascmd.Authority) ([]aliascmd.Record, error)
	Get(ctx context.Context, name string, authority aliascmd.Authority) (aliascmd.Record, error)
	Create(ctx context.Context, request aliascmd.Create) (aliascmd.Record, error)
	Retarget(ctx context.Context, request aliascmd.Retarget) (aliascmd.Record, error)
	Delete(ctx context.Context, request aliascmd.Delete) error
}

// ConfigureAliasesOperations binds host policy before Backoffice starts.
func (r *Runtime) ConfigureAliasesOperations(operations AliasesOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("alias operations must be configured before Backoffice start")
	}
	r.aliases = operations
	return nil
}

func (a *httpApp) aliasesPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page, err := a.aliasesView(r, p)
	if err != nil {
		a.aliasError(w, r, page, err)
		return
	}
	a.render(w, r, http.StatusOK, webui.TemplateAliases, page)
}

func (a *httpApp) aliasBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Aliases · Balda", ViewerUsername: p.User.Username,
		CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationAliases,
		Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationAliases)}
}

func (a *httpApp) aliasesView(r *http.Request, p security.Principal) (webui.Page, error) {
	page := a.aliasBasePage(r, p)
	if a.aliases == nil {
		return page, aliascmd.ErrUnavailable
	}
	name := r.PathValue("alias_name")
	if name != "" {
		record, err := a.aliases.Get(r.Context(), name, a.aliasAuthority(p))
		if err != nil {
			return page, err
		}
		page.Title = record.Name + " · Aliases · Balda"
		page.Aliases = &webui.AliasesView{Editor: webui.ProjectAliasEditor(record, false)}
		return page, nil
	}
	if r.Method == http.MethodPost || r.URL.Query().Get("new") == "1" {
		page.Title = "Add alias · Balda"
		page.Aliases = &webui.AliasesView{Editor: webui.ProjectAliasEditor(aliascmd.Record{}, true)}
		return page, nil
	}
	records, err := a.aliases.List(r.Context(), a.aliasAuthority(p))
	if err != nil {
		return page, err
	}
	view := &webui.AliasesView{}
	for _, record := range records {
		view.Rows = append(view.Rows, webui.ProjectAliasRow(record))
	}
	page.Aliases = view
	return page, nil
}

func (a *httpApp) aliasAuthority(p security.Principal) aliascmd.Authority {
	return aliascmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version,
		CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion,
		SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
}

func (a *httpApp) aliasMutation(w http.ResponseWriter, r *http.Request) (url.Values, webui.Page, aliascmd.Authority, bool) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return nil, webui.Page{}, aliascmd.Authority{}, false
	}
	page, err := a.aliasesView(r, p)
	if err != nil {
		a.aliasError(w, r, page, err)
		return nil, page, aliascmd.Authority{}, false
	}
	return form, page, a.aliasAuthority(p), true
}

func (a *httpApp) aliasCreate(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.aliasMutation(w, r)
	if !ok {
		return
	}
	record, err := a.aliases.Create(r.Context(), aliascmd.Create{Name: form.Get("name"), LocatorRef: form.Get("locator_ref"), Authority: authority})
	a.aliasResult(w, r, page, record.Name, err)
}

func (a *httpApp) aliasRetarget(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.aliasMutation(w, r)
	if !ok {
		return
	}
	version, err := aliasVersion(form)
	if err != nil {
		a.aliasError(w, r, page, err)
		return
	}
	name := r.PathValue("alias_name")
	_, err = a.aliases.Retarget(r.Context(), aliascmd.Retarget{Name: name, LocatorRef: form.Get("locator_ref"), ExpectedVersion: version, Authority: authority})
	a.aliasResult(w, r, page, name, err)
}

func (a *httpApp) aliasDelete(w http.ResponseWriter, r *http.Request) {
	form, page, authority, ok := a.aliasMutation(w, r)
	if !ok {
		return
	}
	version, err := aliasVersion(form)
	if err != nil || form.Get("confirm") != checkedFormValue {
		a.aliasError(w, r, page, aliascmd.ErrInvalid)
		return
	}
	err = a.aliases.Delete(r.Context(), aliascmd.Delete{Name: r.PathValue("alias_name"), ExpectedVersion: version, Authority: authority})
	a.aliasResult(w, r, page, "", err)
}

func aliasVersion(form url.Values) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(form.Get("expected_version")), 10, 64)
	if err != nil || version == 0 {
		return 0, aliascmd.ErrInvalid
	}
	return version, nil
}

func (a *httpApp) aliasResult(w http.ResponseWriter, r *http.Request, page webui.Page, name string, err error) {
	if err != nil {
		a.aliasError(w, r, page, err)
		return
	}
	location := "/aliases"
	if name != "" {
		location += "/" + url.PathEscape(name)
	}
	if err := webui.RespondMutationPath(w, r, a.path(location)); err != nil {
		a.aliasError(w, r, page, aliascmd.ErrUnavailable)
	}
}

func (a *httpApp) aliasError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := aliasOperationFailure(err)
	page.Error = &webui.ErrorView{Heading: "Alias operation could not complete", Message: message}
	templateName := webui.TemplateAliases
	if page.Aliases == nil {
		page.Title = "Aliases unavailable · Balda"
		templateName = webui.TemplateError
	}
	a.render(w, r, status, templateName, page)
}

func aliasOperationFailure(err error) (int, string) {
	switch {
	case errors.Is(err, aliascmd.ErrInvalid):
		return http.StatusBadRequest, "Use a lowercase name beginning with a letter and a valid public locator."
	case errors.Is(err, aliascmd.ErrForbidden):
		return http.StatusForbidden, "Your administrator authority changed. Reopen Aliases."
	case errors.Is(err, aliascmd.ErrNotFound):
		return http.StatusNotFound, "This alias is no longer available. Reopen Aliases."
	case errors.Is(err, aliascmd.ErrConflict):
		return http.StatusConflict, "This alias changed. Reopen it before trying again."
	default:
		return http.StatusServiceUnavailable, "The operation could not be completed. Reopen Aliases and check its current state before retrying."
	}
}
