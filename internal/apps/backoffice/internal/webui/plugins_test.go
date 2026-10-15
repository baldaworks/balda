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
			for _, label := range []string{"Name", "Source", "Ref", "Sparse", "Discovery", "Available plugins", "Last successful refresh", "Actions"} {
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
			projected := ProjectPluginMarketplaceDetail(tc.detail, nil)
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

func TestPluginMarketplaceOperationalRender(t *testing.T) {
	renderer, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	item := plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "team", ResolvedRevision: "resolved-exact", AvailablePlugins: 2, LastAttemptStatus: "succeeded"}, Candidates: []plugincmd.CandidateView{{Name: "installed", CandidateToken: "review"}, {Name: "available", CandidateToken: "review"}}}
	projected := ProjectPluginMarketplaceDetail(item, []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: "installed"}}})
	response := httptest.NewRecorder()
	if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/plugins/marketplaces/definitions/team", nil), 200, TemplatePlugins, Page{Title: "Marketplace", Plugins: &PluginsView{Marketplaces: true, MarketplaceDetail: projected}}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{"Installed", "Actions", "Open installed", "Review install", "resolved-exact", "Available plugins"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !projected.Candidates[0].Installed || projected.Candidates[1].Installed {
		t.Fatal("installed identity not joined by name")
	}
}

func TestPluginsDeclarationMarkerBesidePackageNames(t *testing.T) {
	renderer, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	const name = "demo <tag>"
	installed := plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: name}, DeclaresBaldaExtension: true}
	candidate := plugincmd.CandidateView{Name: name, DeclaresBaldaExtension: true}
	detail := plugincmd.PluginDetail{InstalledView: installed}
	tests := []struct {
		name string
		view *PluginsView
		want string
	}{
		{name: "installed", view: &PluginsView{InstalledRows: []plugincmd.InstalledView{installed}}, want: `<td class="text-break">demo &lt;tag&gt; <span class="badge text-bg-secondary ms-2">Balda extension</span></td>`},
		{name: "discovered", view: &PluginsView{Marketplaces: true, MarketplaceDetail: ProjectPluginMarketplaceDetail(plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "market"}, Candidates: []plugincmd.CandidateView{candidate}}, nil)}, want: `<td class="text-break">demo &lt;tag&gt; <span class="badge text-bg-secondary ms-2">Balda extension</span></td>`},
		{name: "detail", view: &PluginsView{PluginDetail: ProjectPluginDetail(detail)}, want: `<dd class="col-sm-9 text-break">demo &lt;tag&gt; <span class="badge text-bg-secondary ms-2">Balda extension</span></dd>`},
		{name: "installed without declaration", view: &PluginsView{InstalledRows: []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: name}}}}},
		{name: "discovered without declaration", view: &PluginsView{Marketplaces: true, MarketplaceDetail: ProjectPluginMarketplaceDetail(plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: "market"}, Candidates: []plugincmd.CandidateView{{Name: name}}}, nil)}},
		{name: "detail without declaration", view: &PluginsView{PluginDetail: ProjectPluginDetail(plugincmd.PluginDetail{InstalledView: plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: name}}})}},
		{name: "review keeps action screen unchanged", view: &PluginsView{Review: &PluginReview{Candidate: &candidate}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/plugins", nil), http.StatusOK, TemplatePlugins, Page{Title: "Plugins", Plugins: tt.view}); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if strings.Contains(body, "<tag>") || !strings.Contains(body, "demo &lt;tag&gt;") {
				t.Fatal("package name was not HTML escaped")
			}
			if tt.want != "" && !strings.Contains(body, tt.want) {
				t.Fatalf("marker missing beside package name in %s", tt.name)
			}
			want := 0
			if tt.want != "" {
				want = 1
			}
			if got := strings.Count(body, ">Balda extension</span>"); got != want {
				t.Fatalf("marker count = %d, want %d", got, want)
			}
		})
	}
}
