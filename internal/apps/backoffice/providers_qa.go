package backoffice

import (
	"net/http"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

var providerQAEntries = []qaEntry{
	{name: "providers", label: "Providers · inventory", templateName: webui.TemplateProviders, page: qaProviders, gallery: true},
	{name: "providers-empty", label: "Providers · empty", templateName: webui.TemplateProviders, page: func() webui.Page { p := qaProviders(); p.Providers.Rows = nil; return p }, gallery: true},
	{name: "providers/new", label: "Providers · create", templateName: webui.TemplateProviders, page: func() webui.Page { return qaProviderEditor(providers.Item{}, true) }, gallery: true},
	{name: "providers/managed", label: "Providers · edit managed", templateName: webui.TemplateProviders, page: func() webui.Page { return qaProviderEditor(qaManagedProvider(), false) }, gallery: true},
	{name: "providers/configured", label: "Providers · configured read-only", templateName: webui.TemplateProviders, page: func() webui.Page { return qaProviderEditor(qaConfiguredProvider(), false) }, gallery: true},
	{name: "providers-conflict", label: "Providers · conflict", templateName: webui.TemplateProviders, page: func() webui.Page {
		p := qaProviderEditor(qaManagedProvider(), false)
		p.Error = &webui.ErrorView{Heading: "Provider operation could not complete", Message: "Provider state changed. Reopen Providers before trying again."}
		return p
	}, gallery: true, status: http.StatusConflict},
}

func qaProviders() webui.Page {
	managed, configured := qaManagedProvider(), qaConfiguredProvider()
	return webui.Page{Title: "Providers · QA", Current: webui.LocationProviders, ViewerUsername: "qa-superuser", CSRFToken: "qa-csrf", Navigation: qaAdminNavigation(webui.LocationProviders),
		Providers: &webui.ProvidersView{Version: 7, Rows: []webui.ProviderRow{projectProviderRow(configured, 7), projectProviderRow(managed, 7)}}}
}

func qaProviderEditor(item providers.Item, create bool) webui.Page {
	p := qaProviders()
	p.Providers.Editor = projectProviderEditor(item, 7, create)
	if create {
		p.Title = "Add provider · QA"
	} else {
		p.Title = item.Record.ID + " · Providers · QA"
	}
	return p
}

func qaManagedProvider() providers.Item {
	inherit := false
	return providers.Item{Source: providers.SourceManaged,
		Record: providers.Record{ID: "managed-codex", CurrentRevisionID: "synthetic-managed-revision", Enabled: true},
		Revision: providers.Revision{ID: "synthetic-managed-revision", ProviderID: "managed-codex", DriverType: agentconfig.AgentTypeCodexACP,
			Definition: providers.Definition{Config: agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Model: "gpt-5.4", ReasoningEffort: "high", InheritEnv: &inherit}}, EnvironmentNames: []string{"CODEX_HOME"}, Reproducible: true}}}
}

func qaConfiguredProvider() providers.Item {
	return providers.Item{Source: providers.SourceConfigured,
		Record: providers.Record{ID: "configured-pool", CurrentRevisionID: "configured-v1:synthetic", Enabled: true},
		Revision: providers.Revision{ID: "configured-v1:synthetic", ProviderID: "configured-pool", DriverType: agentconfig.AgentTypePool,
			Definition: providers.Definition{Config: agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"managed-codex", "configured-fallback"}}}}}}
}
