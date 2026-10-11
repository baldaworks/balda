package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestPluginsNavigationAndMarketplaceRender(t *testing.T) {
	nav := Navigation(usercmd.BackofficeCapabilities{ManagePlugins: true}, LocationPlugins)
	if len(nav) != 1 || nav[0].Label != "Operations" || len(nav[0].Items) != 1 || nav[0].Items[0].Label != "Plugins" {
		t.Fatalf("navigation %+v", nav)
	}
	renderer, err := NewRenderer("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []*PluginsView{{Marketplaces: true, MarketplaceRows: []PluginMarketplaceRow{ProjectPluginMarketplaceRow(plugincmd.MarketplaceView{Name: "2.Team", Source: "local"})}}, {Marketplaces: true, MarketplaceAdd: &PluginMarketplaceAdd{}}} {
		response := httptest.NewRecorder()
		if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/balda/backoffice/plugins/marketplaces", nil), http.StatusOK, TemplatePlugins, Page{Title: "Plugins", Plugins: view}); err != nil {
			t.Fatal(err)
		}
		body := response.Body.String()
		if strings.Count(body, `id="main-content"`) != 1 || !strings.Contains(body, `hx-history="false"`) {
			t.Fatal("shared private shell missing")
		}
		if view.MarketplaceAdd != nil {
			for _, label := range []string{"Name", "Source", "Ref", "Sparse"} {
				if !strings.Contains(body, ">"+label+"</label>") {
					t.Fatalf("missing field %s", label)
				}
			}
			if !strings.Contains(body, `pattern="(?!.*\.\.)[A-Za-z0-9][A-Za-z0-9._\-]{0,63}"`) {
				t.Fatal("Name field excludes supported identities")
			}
			if !strings.Contains(body, `name="ref" maxlength="256"`) {
				t.Fatal("Ref field exceeds supported bound")
			}
			if !strings.Contains(body, `data-plugin-source`) || !strings.Contains(body, `href="/balda/backoffice/plugins/marketplaces"`) {
				t.Fatal("transient source/cancel missing")
			}
		} else {
			for _, label := range []string{"Name", "Source", "Ref", "Sparse"} {
				if !strings.Contains(body, `<th scope="col">`+label+`</th>`) {
					t.Fatalf("missing column %s", label)
				}
			}
			if !strings.Contains(body, `href="/balda/backoffice/plugins/marketplaces/definitions/2.Team"`) {
				t.Fatal("valid new identity lost")
			}
		}
	}
}

func TestPluginMarketplaceDiscoveryPresentation(t *testing.T) {
	renderer, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		detail plugincmd.MarketplaceDetail
		want   []string
	}{
		{name: "invalid entry", detail: plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "demo", LastAttemptStatus: "succeeded"}, DiagnosticCodes: []string{"invalid_plugin_entry", "secret-canary"}}, want: []string{"Some marketplace entries could not be validated and were excluded."}},
		{name: "invalid package", detail: plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "demo", LastAttemptStatus: "succeeded"}, Candidates: []plugincmd.CandidateView{{Name: "demo", DiagnosticCodes: []string{"package_validation_failed"}}}}, want: []string{"Unavailable", "This package could not be validated."}},
		{name: "actionable warning", detail: plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "demo", LastAttemptStatus: "succeeded"}, Candidates: []plugincmd.CandidateView{{Name: "demo", CandidateToken: "review", DiagnosticCodes: []string{"secret-canary"}}}}, want: []string{"Available"}},
		{name: "failed index", detail: plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "demo", LastAttemptStatus: "failed", FailureCode: "invalid_index"}, Candidates: []plugincmd.CandidateView{{Name: "demo", CandidateToken: "review"}}}, want: []string{"Invalid marketplace index", "Refresh required"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projected := ProjectPluginMarketplaceDetail(tc.detail)
			response := httptest.NewRecorder()
			if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/plugins/marketplaces/definitions/demo", nil), http.StatusOK, TemplatePlugins, Page{Title: "Plugins", Plugins: &PluginsView{Marketplaces: true, MarketplaceDetail: projected}}); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("missing %q", want)
				}
			}
			if strings.Contains(body, "secret-canary") {
				t.Fatal("raw diagnostic reflected")
			}
			if tc.name == "actionable warning" && strings.Contains(body, "could not be validated") {
				t.Fatal("actionable warning classified as invalid")
			}
		})
	}
}
