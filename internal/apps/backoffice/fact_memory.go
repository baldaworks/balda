package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// FactMemoryOperations is Backoffice's consuming port for global fact management.
type FactMemoryOperations interface {
	Inventory(ctx context.Context, query string, authority memorycmd.Authority) (memorycmd.Inventory, error)
	Get(ctx context.Context, id int64, authority memorycmd.Authority) (memorycmd.Editor, error)
	Create(ctx context.Context, request memorycmd.Create) (memorycmd.Fact, error)
	Edit(ctx context.Context, request memorycmd.Edit) (memorycmd.Fact, error)
	Delete(ctx context.Context, request memorycmd.Delete) error
}

// ConfigureFactMemoryOperations binds the host's shared fact owner before serving.
func (r *Runtime) ConfigureFactMemoryOperations(operations FactMemoryOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("fact memory operations must be configured before Backoffice start")
	}
	r.factMemory = operations
	return nil
}

func (a *httpApp) factMemoryBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Fact memory · Balda", ViewerUsername: p.User.Username,
		CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationFactMemory,
		Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationFactMemory)}
}

func factMemoryAuthority(p security.Principal) memorycmd.Authority {
	return memorycmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version,
		CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion,
		SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
}

func (a *httpApp) factMemoryPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.factMemoryBasePage(r, p)
	if a.factMemory == nil {
		a.factMemoryError(w, r, page, memorycmd.ErrUnavailable)
		return
	}
	authority := factMemoryAuthority(p)
	if raw := r.PathValue("fact_id"); raw != "" {
		id, err := factMemoryID(raw)
		if err != nil {
			a.factMemoryError(w, r, page, err)
			return
		}
		editor, err := a.factMemory.Get(r.Context(), id, authority)
		if err != nil {
			a.factMemoryError(w, r, page, err)
			return
		}
		page.FactMemory = &webui.FactMemoryView{Enabled: true, Editor: webui.ProjectFactEditor(editor, false)}
		page.Title = "Edit fact · Balda"
	} else {
		query := r.URL.Query().Get("q")
		inv, err := a.factMemory.Inventory(r.Context(), query, authority)
		if err != nil {
			a.factMemoryError(w, r, page, err)
			return
		}
		page.FactMemory = webui.ProjectFactInventory(inv, query)
		if inv.Enabled && r.URL.Query().Get("new") == "1" {
			page.FactMemory.Editor = webui.ProjectFactEditor(memorycmd.Editor{}, true)
			page.Title = "Add fact · Balda"
		}
	}
	a.render(w, r, http.StatusOK, webui.TemplateFactMemory, page)
}

func factMemoryID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, memorycmd.ErrInvalid
	}
	return id, nil
}

func (a *httpApp) factMemoryMutation(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.factMemoryBasePage(r, p)
	raw := r.PathValue("fact_id")
	create := raw == ""
	editor := webui.ProjectFactEditor(memorycmd.Editor{}, create)
	editor.Fact = form.Get("fact")
	page.FactMemory = &webui.FactMemoryView{Enabled: true, Editor: editor}
	page.Title = "Add fact · Balda"
	if !create {
		id, err := factMemoryID(raw)
		if err != nil {
			a.factMemoryError(w, r, page, err)
			return
		}
		editor.ID = id
		editor.Action = "/memory/" + raw
		editor.ReopenPath = editor.Action
		version, err := strconv.ParseInt(form.Get("expected_version"), 10, 64)
		if err != nil || version <= 0 {
			a.factMemoryError(w, r, page, memorycmd.ErrInvalid)
			return
		}
		editor.ID, editor.ExpectedVersion = id, version
		editor.Action = "/memory/" + raw
		editor.ReopenPath = editor.Action
		page.Title = "Edit fact · Balda"
	}
	if a.factMemory == nil {
		a.factMemoryError(w, r, page, memorycmd.ErrUnavailable)
		return
	}
	authority := factMemoryAuthority(p)
	var err error
	switch {
	case create:
		_, err = a.factMemory.Create(r.Context(), memorycmd.Create{Fact: editor.Fact, Authority: authority})
	case r.PathValue("memory_action") == "delete":
		if form.Get("confirm") != checkedFormValue {
			err = memorycmd.ErrInvalid
		} else {
			err = a.factMemory.Delete(r.Context(), memorycmd.Delete{ID: editor.ID, ExpectedVersion: editor.ExpectedVersion, Authority: authority})
		}
	default:
		_, err = a.factMemory.Edit(r.Context(), memorycmd.Edit{ID: editor.ID, Fact: editor.Fact, ExpectedVersion: editor.ExpectedVersion, Authority: authority})
	}
	if err != nil {
		if !create && r.PathValue("memory_action") == "delete" {
			if current, readErr := a.factMemory.Get(r.Context(), editor.ID, authority); readErr == nil {
				editor.Fact = current.Fact.Fact
			}
		}
		a.factMemoryError(w, r, page, err)
		return
	}
	if err := webui.RespondMutationPath(w, r, a.path("/memory")); err != nil {
		a.factMemoryError(w, r, page, memorycmd.ErrUnavailable)
	}
}

func (a *httpApp) factMemoryDelete(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("memory_action", "delete")
	a.factMemoryMutation(w, r)
}

func (a *httpApp) factMemoryError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := factMemoryFailure(err)
	page.Error = &webui.ErrorView{Heading: "Fact memory operation could not complete", Message: message}
	name := webui.TemplateFactMemory
	if errors.Is(err, memorycmd.ErrDisabled) {
		page.FactMemory = &webui.FactMemoryView{}
	}
	if page.FactMemory == nil {
		name = webui.TemplateError
		page.RestartURL, page.RestartLabel = a.path("/memory"), "Open Fact memory"
	}
	a.render(w, r, status, name, page)
}

func factMemoryFailure(err error) (int, string) {
	switch {
	case errors.Is(err, memorycmd.ErrInvalid):
		return http.StatusBadRequest, "Enter a non-empty Fact and confirm deletion when deleting. Reopen Fact memory if this form is outdated."
	case errors.Is(err, memorycmd.ErrDisabled):
		return http.StatusForbidden, "Fact memory is disabled in configuration."
	case errors.Is(err, memorycmd.ErrForbidden):
		return http.StatusForbidden, "Your administrator authority changed. Reopen Fact memory or sign in again."
	case errors.Is(err, memorycmd.ErrNotFound):
		return http.StatusNotFound, "This fact is no longer available. Open Fact memory."
	case errors.Is(err, memorycmd.ErrConflict):
		return http.StatusConflict, "Fact memory changed. Reopen this fact before saving or deleting."
	default:
		return http.StatusServiceUnavailable, "The change could not be confirmed. Reopen Fact memory and check its current state before retrying."
	}
}
