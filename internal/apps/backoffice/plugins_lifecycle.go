package backoffice

import (
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

const reopenInstallLabel = "Reopen install"

const pluginInstallPath = "/plugins/install"
const pluginInstalledPath = "/plugins/installed/{plugin_name}"

func (a *httpApp) pluginDetailPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.pluginBasePage(r, p)
	page.RestartURL = a.path(webui.PluginDetailPath(r.PathValue("plugin_name")))
	page.RestartLabel = reopenPluginLabel
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	detail, err := a.plugins.Detail(r.Context(), r.PathValue("plugin_name"), pluginAuthority(p))
	if err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	page.Plugins.PluginDetail = webui.ProjectPluginDetail(detail)
	a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
}

func (a *httpApp) pluginReviewPage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := a.pluginBasePage(r, p)
	review := &webui.PluginReview{ActionPath: pluginInstallPath}
	page.Plugins.Review = review
	page.RestartURL = a.path(pluginInstallPath)
	page.RestartLabel = reopenInstallLabel
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	name := r.PathValue("plugin_name")
	if name != "" {
		page.RestartURL = a.path(webui.PluginDetailPath(name))
		page.RestartLabel = reopenPluginLabel
	}
	marketplace := r.URL.Query().Get("marketplace")
	if name != "" {
		detail, err := a.plugins.Detail(r.Context(), name, pluginAuthority(p))
		if err != nil {
			a.pluginError(w, r, page, err)
			return
		}
		review.Upgrade = true
		review.Current = webui.ProjectPluginDetail(detail)
		review.ActionPath = review.Current.DetailPath + "/upgrade"
		if detail.Removed || detail.RecoveryPending || !detail.OriginAvailable {
			a.pluginError(w, r, page, plugincmd.ErrUnavailable)
			return
		}
		marketplace = ""
	} else {
		var err error
		review.Marketplaces, err = a.plugins.Marketplaces(r.Context(), pluginAuthority(p))
		if err != nil {
			a.pluginError(w, r, page, err)
			return
		}
		review.Marketplace = marketplace
		if marketplace == "" {
			a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
			return
		}
		detail, err := a.plugins.Marketplace(r.Context(), marketplace, pluginAuthority(p))
		if err != nil {
			a.pluginError(w, r, page, err)
			return
		}
		if detail.LastAttemptStatus != "succeeded" {
			a.pluginError(w, r, page, plugincmd.ErrUnavailable)
			return
		}
		for _, candidate := range detail.Candidates {
			if candidate.CandidateToken != "" {
				review.Candidates = append(review.Candidates, candidate)
			}
		}
		name = r.URL.Query().Get("plugin")
		if name != "" {
			installed, err := a.plugins.Inventory(r.Context(), pluginAuthority(p))
			if err != nil {
				a.pluginError(w, r, page, err)
				return
			}
			for _, item := range installed {
				if item.Name == name {
					review.ExistingPath = webui.PluginDetailPath(name)
					a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
					return
				}
			}
		}
		if name == "" {
			a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
			return
		}
	}
	candidate, err := a.plugins.Candidate(r.Context(), name, marketplace, pluginAuthority(p))
	if err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	if candidate.CandidateToken == "" || candidate.ExpectedState == "" {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	if review.Current != nil && candidate.Revision == review.Current.Revision {
		a.pluginError(w, r, page, plugincmd.ErrConflict)
		return
	}
	review.Candidate = &candidate
	review.CandidateDiagnostics = webui.ProjectPluginDiagnostics(candidate.DiagnosticCodes)
	a.render(w, r, http.StatusOK, webui.TemplatePlugins, page)
}

func (a *httpApp) pluginLifecycleMutation(w http.ResponseWriter, r *http.Request) {
	form, p, ok := a.browser.AdministratorMutationLimit(w, r, 1<<20)
	if !ok {
		return
	}
	page := a.pluginBasePage(r, p)
	operation := plugincmd.Operation(r.PathValue("plugin_operation"))
	name := r.PathValue("plugin_name")
	if r.URL.Path == a.path(pluginInstallPath) {
		operation = plugincmd.Install
		name = form.Get("plugin_name")
	}
	page.RestartURL = a.path(webui.PluginDetailPath(name))
	page.RestartLabel = reopenPluginLabel
	if operation == plugincmd.Install {
		page.RestartURL = a.path(pluginInstallPath)
		page.RestartLabel = reopenInstallLabel
	}
	request := plugincmd.PluginMutation{Operation: operation, PluginName: name, MarketplaceName: form.Get("marketplace_name"), CandidateToken: form.Get("candidate_token"), Revision: form.Get("revision"), ExpectedState: strings.TrimSpace(form.Get("expected_state")), Confirm: form.Get("confirm") == checkedFormValue, Authority: pluginAuthority(p)}
	if a.plugins == nil {
		a.pluginError(w, r, page, plugincmd.ErrUnavailable)
		return
	}
	if err := request.Validate(); err != nil {
		a.pluginError(w, r, page, err)
		return
	}
	outcome, err := a.plugins.MutatePlugin(r.Context(), request)
	if err == nil && outcome.RecoveryPending {
		err = plugincmd.ErrRecoveryPending
	}
	if err != nil {
		page.RestartURL = a.path(webui.PluginDetailPath(name))
		page.RestartLabel = reopenPluginLabel
		if operation == plugincmd.Install && !outcome.Accepted {
			page.RestartURL = a.path(pluginInstallPath)
			page.RestartLabel = reopenInstallLabel
		}
		if outcome.Accepted {
			if detail, readErr := a.plugins.Detail(r.Context(), name, pluginAuthority(p)); readErr == nil {
				page.Plugins.PluginDetail = webui.ProjectPluginDetail(detail)
				page.Plugins.PluginDetail.MutationBlocked = true
			}
		}
		a.pluginError(w, r, page, err)
		return
	}
	location := webui.PluginDetailPath(name)
	if operation == plugincmd.Remove {
		location = "/plugins"
	}
	a.pluginResult(w, r, page, location)
}
