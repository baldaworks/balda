package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func TestMCPDeletedDetailRetainsMetadataWithoutMutationForms(t *testing.T) {
	renderer, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "retained", PublicID: "retained-worker", Source: mcpcmd.SourceManaged, Deleted: true, Version: 3}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp"}, Status: mcpcmd.StatusDeleted}
	page := Page{Title: "Retained MCP connection", MCP: &MCPView{Editor: ProjectMCPEditor(item, nil, false)}}
	w := httptest.NewRecorder()
	if err := renderer.Render(w, httptest.NewRequest(http.MethodGet, "/mcp/connections/retained", nil), 200, TemplateMCP, page); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Retained definition") || strings.Contains(body, `<form`) {
		t.Fatal("deleted connection advertises mutations instead of retained metadata")
	}
}

func TestMCPRemoteOAuthIsAvailableWithoutRecovery(t *testing.T) {
	for _, source := range []mcpcmd.Source{mcpcmd.SourceManaged, mcpcmd.SourceConfig} {
		for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
			item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "remote", PublicID: "public-tools", Source: source}, Definition: mcpcmd.Definition{Transport: transport, URL: "https://tools.example/mcp"}, Status: mcpcmd.StatusReady, ToolCount: 2}
			e := ProjectMCPEditor(item, nil, false)
			if !e.AuthorizationAvailable || e.Row.Authorization != "Not configured" || e.Row.Status != "Available" || e.Row.Recovery != "" {
				t.Fatalf("public remote must offer optional OAuth and truthful availability: %+v", e.Row)
			}
			renderer, err := NewRenderer("/balda")
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			if err := renderer.Render(w, httptest.NewRequest(http.MethodGet, "/balda/mcp/connections/remote", nil), 200, TemplateMCP, Page{Title: "MCP", MCP: &MCPView{Editor: e}}); err != nil {
				t.Fatal(err)
			}
			for _, visible := range []string{"Client settings — optional", "Authorize in browser", "Authorize with device", "Not configured", "2 tools"} {
				if !strings.Contains(w.Body.String(), visible) {
					t.Errorf("missing remote OAuth/availability control %q", visible)
				}
			}
		}
	}
}

func TestMCPCreationOffersNativeOAuthActions(t *testing.T) {
	renderer, err := NewRenderer("/balda")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	page := Page{Title: "MCP", MCP: &MCPView{Editor: ProjectMCPEditor(mcpcmd.Item{}, nil, true)}}
	if err := renderer.Render(w, httptest.NewRequest(http.MethodGet, "/balda/mcp/new", nil), 200, TemplateMCP, page); err != nil {
		t.Fatal(err)
	}
	for _, control := range []string{`data-mcp-create`, `hx-boost="false"`, `formaction="/balda/mcp/connections/oauth/browser"`, `formaction="/balda/mcp/connections/oauth/device"`, "Create and authorize in browser", "Client settings — optional", `name="scopes"`} {
		if !strings.Contains(w.Body.String(), control) {
			t.Errorf("missing native onboarding control %q", control)
		}
	}
}

func TestMCPAttachmentRetryMatchesRecoverableAvailability(t *testing.T) {
	for _, tc := range []struct {
		status mcpcmd.Status
		retry  bool
	}{
		{mcpcmd.StatusUnavailable, true}, {mcpcmd.StatusPending, true}, {mcpcmd.StatusReady, false}, {mcpcmd.StatusDisabled, false}, {mcpcmd.StatusConflict, false},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "saved", PublicID: "tools", Source: mcpcmd.SourceConfig, Enabled: true, CurrentRevisionID: "revision"}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP}, Status: tc.status, Authorization: mcpcmd.GrantAuthorized}
			renderer, err := NewRenderer("")
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			if err := renderer.Render(w, httptest.NewRequest(http.MethodGet, "/mcp/connections/saved", nil), 200, TemplateMCP, Page{Title: "MCP", MCP: &MCPView{Editor: ProjectMCPEditor(item, nil, false)}}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w.Body.String(), ">Retry tool attachment</button>") != tc.retry {
				t.Fatal("attachment retry does not match recoverable availability")
			}
		})
	}
}
