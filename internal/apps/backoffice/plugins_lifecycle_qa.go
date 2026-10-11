package backoffice

import (
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

func qaInstalledPlugin() plugincmd.PluginDetail {
	return plugincmd.PluginDetail{InstalledView: plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: "demo", Version: "1.0.0", Marketplace: "team", Revision: "sha256:synthetic-current", Enabled: true, Runtime: plugincmd.RuntimeStatus{SnapshotID: "synthetic-catalog", RevisionCurrent: true, DesiredMCPServers: 1, ReadyMCPServers: 1}}, ExpectedState: "qa-plugin-state", OriginAvailable: true}, Commands: []plugincmd.Contribution{{Name: "plan", Description: "Plan a release."}}, Skills: []plugincmd.Contribution{{Name: "review", Description: "Review a proposed change."}}, MCPServers: []plugincmd.Contribution{{Name: "knowledge", Transport: "stdio"}}, Revisions: []plugincmd.RetainedRevision{{Version: "1.0.0", Revision: "sha256:synthetic-current", RetainedAt: qaNow, Available: true, Current: true}, {Version: "0.9.0", Revision: "sha256:synthetic-retained", RetainedAt: qaNow.AddDate(0, 0, -1), Available: true}, {Version: "0.8.0", Revision: "sha256:synthetic-unavailable", RetainedAt: qaNow.AddDate(0, 0, -2)}}}
}
func qaInstalledDetail() webui.Page {
	p := qaPlugins()
	p.Plugins.PluginDetail = webui.ProjectPluginDetail(qaInstalledPlugin())
	return p
}
func qaPluginReview(upgrade bool) webui.Page {
	p := qaPlugins()
	candidate := plugincmd.CandidateView{Name: "demo", Version: "2.0.0", Marketplace: "team", Revision: "sha256:synthetic-candidate", Description: "Synthetic reviewed release.", CandidateToken: "qa-reviewed", ExpectedState: "qa-plugin-state", Capabilities: plugincmd.CapabilitySummary{Commands: 1, Skills: 1, MCPServers: 1}}
	review := &webui.PluginReview{Upgrade: upgrade, Marketplace: "team", Marketplaces: []plugincmd.MarketplaceView{{Name: "team"}}, Candidates: []plugincmd.CandidateView{candidate}, Candidate: &candidate, ActionPath: pluginInstallPath}
	if upgrade {
		review.Current = webui.ProjectPluginDetail(qaInstalledPlugin())
		review.ActionPath = review.Current.DetailPath + "/upgrade"
	}
	p.Plugins.Review = review
	return p
}

var pluginLifecycleQAEntries = []qaEntry{
	{name: "plugins-installed", label: "Plugins · Installed inventory", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaPlugins()
		p.Plugins.InstalledRows = []plugincmd.InstalledView{qaInstalledPlugin().InstalledView}
		return p
	}, gallery: true},
	{name: "plugins/installed/demo", label: "Plugins · Plugin detail and confirmations", templateName: webui.TemplatePlugins, page: qaInstalledDetail, gallery: true},
	{name: "plugins/install", label: "Plugins · Install review", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginReview(false) }, gallery: true},
	{name: "plugins/installed/demo/upgrade", label: "Plugins · Upgrade review", templateName: webui.TemplatePlugins, page: func() webui.Page { return qaPluginReview(true) }, gallery: true},
	{name: "plugins-install-empty", label: "Plugins · Install without marketplaces", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaPlugins()
		p.Plugins.Review = &webui.PluginReview{ActionPath: pluginInstallPath}
		return p
	}, gallery: true},
	{name: "plugins-disabled", label: "Plugins · Disabled retained plugin", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaInstalledDetail()
		p.Plugins.PluginDetail.Enabled = false
		p.Plugins.PluginDetail.CurrentCatalog = "Selected revision is not current"
		p.Plugins.PluginDetail.RuntimeHealth = "Unknown"
		return p
	}, gallery: true},
	{name: "plugins-degraded", label: "Plugins · Degraded runtime", templateName: webui.TemplatePlugins, page: func() webui.Page {
		item := qaInstalledPlugin()
		item.Runtime.ReadyMCPServers = 0
		item.Runtime.DegradedMCPServers = 1
		item.DiagnosticCodes = []string{"mcp_config_invalid"}
		p := qaPlugins()
		p.Plugins.PluginDetail = webui.ProjectPluginDetail(item)
		return p
	}, gallery: true},
	{name: "plugins-pending-remove", label: "Plugins · Accepted removal awaiting recovery", templateName: webui.TemplatePlugins, page: func() webui.Page {
		item := qaInstalledPlugin()
		item.Removed = true
		item.RecoveryPending = true
		item.Enabled = false
		item.Runtime = plugincmd.RuntimeStatus{}
		item.OriginAvailable = false
		for i := range item.Revisions {
			item.Revisions[i].Current = false
		}
		p := qaPlugins()
		p.Plugins.PluginDetail = webui.ProjectPluginDetail(item)
		_, message := pluginOperationFailure(plugincmd.ErrRecoveryPending)
		p.Error = &webui.ErrorView{Heading: "Activation pending", Message: message}
		p.RestartURL = "/plugins/installed/demo"
		p.RestartLabel = reopenPluginLabel
		return p
	}, status: http.StatusServiceUnavailable, gallery: true},
	{name: "plugins-detail-long", label: "Plugins · Long plugin content", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaInstalledDetail()
		p.Plugins.PluginDetail.Revision = strings.Repeat("synthetic-revision-", 30)
		p.Plugins.PluginDetail.ContributionTables[0].Items[0].Description = strings.Repeat("Synthetic-description-", 60)
		return p
	}, gallery: true},
	{name: "plugins-conflict", label: "Plugins · Stale plugin action (409)", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaInstalledDetail()
		p.Plugins.PluginDetail.MutationBlocked = true
		_, message := pluginOperationFailure(plugincmd.ErrConflict)
		p.Error = &webui.ErrorView{Heading: "Plugin changed", Message: message}
		p.RestartURL = "/plugins/installed/demo"
		p.RestartLabel = reopenPluginLabel
		return p
	}, status: http.StatusConflict, gallery: true},
	{name: "plugins-unavailable", label: "Plugins · Unavailable installation (503)", templateName: webui.TemplatePlugins, page: func() webui.Page {
		p := qaPlugins()
		p.Plugins.Unavailable = true
		_, message := pluginOperationFailure(plugincmd.ErrUnavailable)
		p.Error = &webui.ErrorView{Heading: "Plugins unavailable", Message: message}
		return p
	}, status: http.StatusServiceUnavailable, gallery: true},
}
