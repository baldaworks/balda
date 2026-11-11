package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

func TestPluginLifecycleRender(t *testing.T) {
	renderer, err := NewRenderer("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	detail := plugincmd.PluginDetail{InstalledView: plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: "install", Version: "1.0", Marketplace: "team", Revision: "rev-one", Enabled: true}, ExpectedState: "observed", OriginAvailable: true}, Commands: []plugincmd.Contribution{{Name: "plan", Description: "<script>escaped</script>"}}, Revisions: []plugincmd.RetainedRevision{{Revision: "rev-one", Current: true, Available: true}, {Revision: "rev-zero", Available: true}}}
	for _, pending := range []bool{false, true} {
		detail.Removed = pending
		detail.RecoveryPending = pending
		response := httptest.NewRecorder()
		page := Page{Title: "Plugin", Plugins: &PluginsView{PluginDetail: ProjectPluginDetail(detail)}}
		if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/plugins/installed/install", nil), 200, TemplatePlugins, page); err != nil {
			t.Fatal(err)
		}
		body := response.Body.String()
		for _, want := range []string{"Enabled", "rev-one", "Current catalog", "Runtime health", "Existing and restored sessions", "&lt;script&gt;"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
		if pending && strings.Contains(body, "/installed/install/remove") {
			t.Fatal("pending removed plugin permits mutation")
		}
		if !pending && !strings.Contains(body, "/balda/backoffice/plugins/installed/install/disable") {
			t.Fatal("missing disable form")
		}
	}
}

func TestPluginLifecycleUnavailableEvidence(t *testing.T) {
	item := plugincmd.PluginDetail{InstalledView: plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: "demo", Runtime: plugincmd.RuntimeStatus{SnapshotID: "other-source", DesiredMCPServers: 1, ReadyMCPServers: 1}}, ExpectedState: "observed"}, DiagnosticCodes: []string{"revision_unavailable", "secret-canary"}}
	view := ProjectPluginDetail(item)
	if view.RuntimeHealth != "Unknown" {
		t.Fatal("unselected revision inherits readiness")
	}
	renderer, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/plugins/installed/demo", nil), 200, TemplatePlugins, Page{Title: "Plugin", Plugins: &PluginsView{PluginDetail: view}}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if strings.Contains(body, `action="/plugins/installed/demo/enable"`) {
		t.Fatal("unavailable revision permits enable")
	}
	if strings.Contains(body, "secret-canary") || !strings.Contains(body, "Selected revision is unavailable") {
		t.Fatal("diagnostics are not bounded")
	}
}
