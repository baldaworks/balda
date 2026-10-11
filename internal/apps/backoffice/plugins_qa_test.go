package backoffice

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPluginMarketplaceRenderPreviews(t *testing.T) {
	handler, err := QAHandler("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path   string
		status int
		want   string
	}{
		{"plugins/marketplaces", 200, "Marketplaces"},
		{"plugins/marketplaces/new", 200, "data-plugin-source"},
		{"plugins/marketplaces/definitions/new", 200, "Refresh marketplace"},
		{"plugins-marketplace-failed", 200, "Earlier discovery is shown for reference"},
		{"plugins-marketplaces-empty", 200, "No marketplaces yet"},
		{"plugins-marketplace-long", 200, "Synthetic-description-"},
		{"plugins-marketplace-invalid", 400, "Check the definition fields"},
		{"plugins-marketplace-conflict", 409, "This item changed"},
		{"plugins-marketplace-unavailable", 503, "The operation could not complete"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/balda/backoffice/qa/ui/"+check.path, nil))
		if response.Code != check.status || !strings.Contains(response.Body.String(), check.want) || strings.Count(response.Body.String(), `id="main-content"`) != 1 {
			t.Fatalf("preview %s status=%d", check.path, response.Code)
		}
	}
}

func TestPluginLifecycleRenderPreviews(t *testing.T) {
	handler, err := QAHandler("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path, want string
		status     int
	}{
		{"plugins-installed", "Open", 200}, {"plugins/installed/demo", "Retained revisions", 200}, {"plugins/install", "qa-reviewed", 200}, {"plugins/installed/demo/upgrade", "Current Revision", 200}, {"plugins-install-empty", "No marketplaces available", 200}, {"plugins-disabled", "Enable plugin", 200}, {"plugins-degraded", "Degraded", 200}, {"plugins-pending-remove", "Removed current selection", 503}, {"plugins-detail-long", "Synthetic-description-", 200}, {"plugins-conflict", "Reopen plugin", 409}, {"plugins-unavailable", "Plugins unavailable", 503},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/balda/backoffice/qa/ui/"+check.path, nil))
		if response.Code != check.status || !strings.Contains(response.Body.String(), check.want) || strings.Count(response.Body.String(), `id="main-content"`) != 1 {
			t.Fatalf("preview %s status=%d", check.path, response.Code)
		}
	}
}

func TestPluginLifecycleRenderRecoveryLinks(t *testing.T) {
	for _, base := range []string{"", "/balda/backoffice"} {
		handler, err := QAHandler(base)
		if err != nil {
			t.Fatal(err)
		}
		for _, scene := range []string{"plugins-conflict", "plugins-pending-remove"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/qa/ui/"+scene, nil))
			if !strings.Contains(response.Body.String(), `href="`+base+`/qa/ui/plugins/installed/demo"`) {
				t.Fatalf("%s %s recovery does not reopen exact plugin", base, scene)
			}
		}
	}
}
