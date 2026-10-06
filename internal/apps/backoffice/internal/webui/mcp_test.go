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
