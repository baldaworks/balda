package backoffice

import (
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

func qaPlugins() webui.Page {
	return webui.Page{Title: "Plugins · QA", Current: webui.LocationPlugins, Navigation: qaAdminNavigation(webui.LocationPlugins), CSRFToken: "qa-csrf", Plugins: &webui.PluginsView{}}
}
func qaPluginMarketplaces() webui.Page {
	p := qaPlugins()
	p.Plugins.Marketplaces = true
	p.Plugins.MarketplaceRows = []webui.PluginMarketplaceRow{webui.ProjectPluginMarketplaceRow(qaPluginMarketplace().MarketplaceView)}
	return p
}
func qaPluginMarketplace() plugincmd.MarketplaceDetail {
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "new", Source: "https://example.test/team/plugins.git", Kind: "git", Ref: "main", Sparse: []string{"plugins", ".agents/plugins"}, ExpectedState: "qa-observation", LastAttemptAt: qaNow, LastAttemptStatus: "succeeded", LastSuccessfulAt: qaNow, AvailablePlugins: 2, ResolvedRevision: "synthetic-resolved-revision"}, Candidates: []plugincmd.CandidateView{{Name: "demo", Version: "1.0.0", Description: "Synthetic plugin for release planning.", CandidateToken: "qa-candidate"}, {Name: "installed-demo", Version: "1.0.0", Description: "Synthetic installed plugin.", CandidateToken: "qa-installed"}}}
}
func qaPluginMarketplaceDetail(failed bool) webui.Page {
	p := qaPluginMarketplaces()
	item := qaPluginMarketplace()
	if failed {
		item.LastAttemptStatus = "failed"
		item.LastAttemptAt = qaNow.AddDate(0, 0, 1)
		item.FailureCode = "invalid_index"
	}
	p.Plugins.MarketplaceDetail = webui.ProjectPluginMarketplaceDetail(item, []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: "installed-demo"}}})
	return p
}
func qaPluginMarketplaceFailure(err error, add bool) webui.Page {
	p := qaPluginMarketplaceDetail(false)
	if add {
		p = qaPluginMarketplaces()
		p.Plugins.MarketplaceAdd = &webui.PluginMarketplaceAdd{}
	}
	_, message := pluginOperationFailure(err)
	p.Error = &webui.ErrorView{Heading: "Marketplace operation could not complete", Message: message}
	return p
}

var pluginMarketplaceQAEntries = []qaEntry{
	{name: "plugins", label: "Plugins · Installed", templateName: webui.TemplatePlugins, page: qaPlugins, gallery: true},
	{name: "plugins/marketplaces", label: "Plugins · Marketplaces", templateName: webui.TemplatePlugins, page: qaPluginMarketplaces, gallery: true},
	{name: "plugins-marketplaces-empty", label: "Plugins · Empty marketplaces", templateName: webui.TemplatePlugins, page: func() webui.Page { p := qaPluginMarketplaces(); p.Plugins.MarketplaceRows = nil; return p }, gallery: true},
	{name: "plugins/marketplaces/new", label: "Plugins · Add marketplace", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaPluginMarketplaces()
		p.Plugins.MarketplaceAdd = &webui.PluginMarketplaceAdd{}
		return p
	}, gallery: true},
	{name: "plugins/marketplaces/definitions/new", label: "Plugins · Marketplace definition", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginMarketplaceDetail(false) }, gallery: true},
	{name: "plugins-marketplace-failed", label: "Plugins · Failed refresh with stale discovery", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginMarketplaceDetail(true) }, gallery: true},
	{name: "plugins-marketplace-long", label: "Plugins · Long marketplace content", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaPluginMarketplaceDetail(false)
		p.Plugins.MarketplaceDetail.Row.Source = "https://example.test/" + strings.Repeat("synthetic-repository-", 40)
		p.Plugins.MarketplaceDetail.Candidates[0].Description = strings.Repeat("Synthetic-description-", 60)
		return p
	}, gallery: true},
	{name: "plugins-marketplace-invalid", label: "Plugins · Invalid definition (400)", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginMarketplaceFailure(plugincmd.ErrInvalid, true) }, status: http.StatusBadRequest, gallery: true},
	{name: "plugins-marketplace-conflict", label: "Plugins · Marketplace conflict (409)", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginMarketplaceFailure(plugincmd.ErrConflict, false) }, status: http.StatusConflict, gallery: true},
	{name: "plugins-marketplace-unavailable", label: "Plugins · Marketplace unavailable (503)", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginMarketplaceFailure(plugincmd.ErrUnavailable, false) }, status: http.StatusServiceUnavailable, gallery: true},
}
