package backoffice

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const pluginMarketplacesPath = "/plugins/marketplaces"
const pluginMarketplaceNewPath = pluginMarketplacesPath + "/new"
const pluginMarketplaceDefinitionsPath = pluginMarketplacesPath + "/definitions/"

func (a *httpApp) pluginBasePage(r *http.Request, p security.Principal) webui.Page {
	return webui.Page{Title: "Plugins · Balda", ViewerUsername: p.User.Username, CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationPlugins, Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationPlugins), Plugins: &webui.PluginsView{}}
}

func pluginAuthority(p security.Principal) plugincmd.Authority {
	return plugincmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion, SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
}

func (a *httpApp) pluginsPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.pluginBasePage(r, p)
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	items, err := a.plugins.Inventory(r.Context(), pluginAuthority(p))
	if err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	page.Plugins.InstalledRows = items
	a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
}

func (a *httpApp) pluginMarketplacesPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.pluginBasePage(r, p)
	page.Plugins.Marketplaces = true
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	name := r.PathValue("marketplace_name")
	switch {
	case name != "":
		detail, err := a.plugins.Marketplace(r.Context(), name, pluginAuthority(p))
		if err != nil {
			a.pluginError(w, r, page, err)
			return
		}
		page.Title = detail.Name + " · Marketplaces · Balda"
		page.Plugins.MarketplaceDetail = webui.ProjectPluginMarketplaceDetail(detail)
	case r.URL.Path == a.path(pluginMarketplaceNewPath):
		page.Title = "Add marketplace · Balda"
		page.Plugins.MarketplaceAdd = &webui.PluginMarketplaceAdd{}
	default:
		items, err := a.plugins.Marketplaces(r.Context(), pluginAuthority(p))
		if err != nil {
			a.pluginError(w, r, page, err)
			return
		}
		for _, item := range items {
			page.Plugins.MarketplaceRows = append(page.Plugins.MarketplaceRows, webui.ProjectPluginMarketplaceRow(item))
		}
	}
	a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
}

func (a *httpApp) pluginMarketplaceCreate(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.pluginBasePage(r, p)
	page.Title = "Add marketplace · Balda"
	page.Plugins.Marketplaces = true
	page.Plugins.MarketplaceAdd = &webui.PluginMarketplaceAdd{}
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	var sparse []string
	for _, line := range strings.Split(form.Get("sparse"), "\n") {
		if path := strings.TrimSpace(line); path != "" {
			sparse = append(sparse, path)
		}
	}
	result, err := a.plugins.AddMarketplace(r.Context(), plugincmd.MarketplaceAdd{Name: form.Get("name"), Source: form.Get("source"), Ref: form.Get("ref"), Sparse: sparse, Authority: pluginAuthority(p)})
	if err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	a.pluginResult(w, r, page, pluginMarketplaceDefinitionsPath+url.PathEscape(result.Name))
}

func (a *httpApp) pluginMarketplaceRefresh(w http.ResponseWriter, r *http.Request) {
	a.pluginMarketplaceMutation(w, r, false)
}
func (a *httpApp) pluginMarketplaceRemove(w http.ResponseWriter, r *http.Request) {
	a.pluginMarketplaceMutation(w, r, true)
}

func (a *httpApp) pluginMarketplaceMutation(w http.ResponseWriter, r *http.Request, remove bool) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.pluginBasePage(r, p)
	page.Plugins.Marketplaces = true
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	name := r.PathValue("marketplace_name")
	expected := strings.TrimSpace(form.Get("expected_state"))
	detail, err := a.plugins.Marketplace(r.Context(), name, pluginAuthority(p))
	if err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	detail.ExpectedState = expected
	page.Plugins.MarketplaceDetail = webui.ProjectPluginMarketplaceDetail(detail)
	request := plugincmd.MarketplaceMutation{Name: name, ExpectedState: expected, Confirm: form.Get("confirm") == checkedFormValue, Authority: pluginAuthority(p)}
	if expected == "" || remove && !request.Confirm {
		a.pluginError(w, r, page, plugincmd.ErrInvalid)
		return
	}
	if remove {
		err = a.plugins.RemoveMarketplace(r.Context(), request)
	} else {
		result, refreshErr := a.plugins.RefreshMarketplace(r.Context(), request)
		err = refreshErr
		if result.Name != "" {
			page.Plugins.MarketplaceDetail = webui.ProjectPluginMarketplaceDetail(result)
		}
	}
	if err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	location := pluginMarketplacesPath
	if !remove {
		location = pluginMarketplaceDefinitionsPath + url.PathEscape(name)
	}
	a.pluginResult(w, r, page, location)
}

func (a *httpApp) pluginResult(w http.ResponseWriter, r *http.Request, page webui.Page, location string) {
	if err := webui.RespondMutationPath(w, r, a.path(location)); err != nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
	}
}

func (a *httpApp) pluginError(w http.ResponseWriter, r *http.Request, page webui.Page, err error) {
	status, message := pluginOperationFailure(err)
	if page.Plugins != nil && page.Plugins.MarketplaceAdd == nil && page.Plugins.MarketplaceDetail == nil {
		page.Plugins.Unavailable = true
	}
	page.Error = &webui.ErrorView{Heading: "Plugin operation could not complete", Message: message}
	if page.Plugins != nil && page.Plugins.MarketplaceDetail != nil {
		page.RestartURL = a.path(page.Plugins.MarketplaceDetail.Row.DetailPath)
		page.RestartLabel = "Reopen marketplace"
	} else {
		page.RestartURL = a.path(pluginMarketplacesPath)
		page.RestartLabel = "Reopen marketplaces"
	}
	a.render(w, r, status, webui.TemplatePlugins, page)
}

func pluginOperationFailure(err error) (int, string) {
	switch {
	case errors.Is(err, plugincmd.ErrInvalid):
		return http.StatusBadRequest, "Check the definition fields and confirm the requested action."
	case errors.Is(err, plugincmd.ErrForbidden):
		return http.StatusForbidden, "Your administrator authority changed. Reopen Plugins or sign in again."
	case errors.Is(err, plugincmd.ErrNotFound):
		return http.StatusNotFound, "This item is no longer available. Reopen Plugins."
	case errors.Is(err, plugincmd.ErrConflict):
		return http.StatusConflict, "This item changed. Reopen it and review its current state before trying again."
	case errors.Is(err, plugincmd.ErrRecoveryPending):
		return http.StatusServiceUnavailable, "The change was accepted, but activation has not completed. Reopen the plugin to check its current state before retrying."
	default:
		return http.StatusServiceUnavailable, "The operation could not complete. Reopen this page and check its current state before retrying."
	}
}
