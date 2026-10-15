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

func TestMCPOAuthProjectionRequiresEligibleManagedDefinition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		source     mcpcmd.Source
		transport  mcpcmd.Transport
		oauth      bool
		deleted    bool
		applicable bool
	}{
		{"managed HTTP OAuth", mcpcmd.SourceManaged, mcpcmd.TransportHTTP, true, false, true},
		{"managed SSE OAuth", mcpcmd.SourceManaged, mcpcmd.TransportSSE, true, false, true},
		{"managed OAuth disabled", mcpcmd.SourceManaged, mcpcmd.TransportHTTP, false, false, false},
		{"configuration OAuth", mcpcmd.SourceConfig, mcpcmd.TransportHTTP, true, false, false},
		{"managed stdio", mcpcmd.SourceManaged, mcpcmd.TransportStdio, true, false, false},
		{"deleted managed OAuth", mcpcmd.SourceManaged, mcpcmd.TransportHTTP, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := mcpcmd.Item{
				Connection:    mcpcmd.Connection{ID: "remote", PublicID: "public-tools", Source: tc.source, Deleted: tc.deleted, Enabled: true, CurrentRevisionID: "revision"},
				Definition:    mcpcmd.Definition{Transport: tc.transport, URL: "https://tools.example/mcp", OAuth: tc.oauth},
				Status:        mcpcmd.StatusUnavailable,
				Authorization: mcpcmd.GrantAuthorized,
				Recovery:      mcpcmd.RecoveryAuthorizationRequired,
			}
			e := ProjectMCPEditor(item, nil, false)
			if e.AuthorizationAvailable != tc.applicable || e.CanDisconnect != tc.applicable || e.CanRetry != tc.applicable {
				t.Fatalf("OAuth controls = available %t, disconnect %t, retry %t; want %t", e.AuthorizationAvailable, e.CanDisconnect, e.CanRetry, tc.applicable)
			}
			if tc.applicable {
				if e.Row.Authorization != "Authorized" || e.Row.Recovery == "" {
					t.Fatalf("eligible OAuth metadata missing: %+v", e.Row)
				}
			} else if e.Row.Authorization != "" || e.Row.Recovery != "" {
				t.Fatalf("inapplicable OAuth metadata was projected: %+v", e.Row)
			}
		})
	}
}

func TestMCPPendingPresentationDependsOnTransport(t *testing.T) {
	for _, tc := range []struct {
		transport mcpcmd.Transport
		want      string
	}{
		{mcpcmd.TransportHTTP, "Not checked"},
		{mcpcmd.TransportSSE, "Not checked"},
		{mcpcmd.TransportStdio, "Starting"},
	} {
		item := mcpcmd.Item{Definition: mcpcmd.Definition{Transport: tc.transport}, Status: mcpcmd.StatusPending}
		if got := ProjectMCPRow(item).Status; got != tc.want {
			t.Errorf("%s Pending = %q, want %q", tc.transport, got, tc.want)
		}
	}
	for _, tc := range []struct {
		status mcpcmd.Status
		want   string
	}{
		{mcpcmd.StatusReady, "Available"},
		{mcpcmd.StatusDisabled, "Disabled"},
		{mcpcmd.StatusDeleted, "Deleted"},
		{mcpcmd.StatusConflict, "Conflict"},
		{mcpcmd.StatusUnavailable, "Tools unavailable"},
	} {
		item := mcpcmd.Item{Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP}, Status: tc.status}
		if got := ProjectMCPRow(item).Status; got != tc.want {
			t.Errorf("%s = %q, want %q", tc.status, got, tc.want)
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
			item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "saved", PublicID: "tools", Source: mcpcmd.SourceManaged, Enabled: true, CurrentRevisionID: "revision"}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, OAuth: true}, Status: tc.status, Authorization: mcpcmd.GrantAuthorized}
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
