package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestVerifyAssets(t *testing.T) {
	t.Parallel()
	if err := VerifyAssets(); err != nil {
		t.Fatalf("VerifyAssets() error = %v", err)
	}
}

func TestRendererFullFragmentAndHistoryContracts(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	page := Page{
		Title: "Overview · Balda", Current: LocationOverview,
		Navigation:   Navigation(usercmd.BackofficeCapabilities{Overview: true, Account: true}, LocationOverview),
		Capabilities: []CapabilityCard{{ID: "telegram", Name: "Telegram", Mode: "webhook", Endpoint: "/telegram"}},
	}
	tests := []struct {
		name        string
		headers     map[string]string
		wantFull    bool
		wantNavItem string
	}{
		{name: "document", wantFull: true, wantNavItem: "Account"},
		{name: "fragment", headers: map[string]string{"HX-Request": "true", "HX-Target": "main-content"}},
		{name: "wrong target", headers: map[string]string{"HX-Request": "true", "HX-Target": "sidebar"}, wantFull: true},
		{name: "history restore", headers: map[string]string{"HX-Request": "true", "HX-Target": "main-content", "HX-History-Restore-Request": "true"}, wantFull: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/overview", nil)
			for key, value := range tt.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			if err := renderer.Render(response, request, http.StatusOK, TemplateOverview, page); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if strings.Count(body, `id="main-content"`) != 1 {
				t.Fatalf("main-content count in %q = %d", body, strings.Count(body, `id="main-content"`))
			}
			full := strings.Contains(body, "<!doctype html>")
			if full != tt.wantFull {
				t.Fatalf("full document = %t, want %t: %q", full, tt.wantFull, body)
			}
			if !tt.wantFull && !strings.HasPrefix(body, "<title>Overview · Balda</title><main") {
				t.Fatalf("fragment shape = %q", body)
			}
			if tt.wantNavItem != "" && !strings.Contains(body, tt.wantNavItem) {
				t.Fatalf("authorized navigation %q absent", tt.wantNavItem)
			}
			if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
				t.Fatalf("rendered document has remote runtime asset: %q", body)
			}
		})
	}
}

func TestRendererPreservesErrorStatusAndDoesNotCommitRejectedModel(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/access", nil)
	request.Header.Set("HX-Request", "true")
	request.Header.Set("HX-Target", "main-content")
	response := httptest.NewRecorder()
	page := Page{Title: "Forbidden", Current: LocationAccess, Error: &ErrorView{Heading: "Forbidden", Message: "Access denied."}}
	if err := renderer.Render(response, request, http.StatusForbidden, TemplateError, page); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "<!doctype") {
		t.Fatalf("error response = %d %q", response.Code, response.Body.String())
	}

	rejected := httptest.NewRecorder()
	if err := renderer.Render(rejected, request, http.StatusOK, "not-allowlisted", page); err == nil {
		t.Fatal("Render(not-allowlisted) error = nil")
	}
	if rejected.Code != http.StatusOK || rejected.Body.Len() != 0 || len(rejected.Header()) != 0 {
		t.Fatalf("rejected render committed response: code=%d headers=%v body=%q", rejected.Code, rejected.Header(), rejected.Body.String())
	}
}

func TestRespondMutation(t *testing.T) {
	t.Parallel()
	ordinaryRequest := httptest.NewRequest(http.MethodPost, "/account", nil)
	ordinary := httptest.NewRecorder()
	if err := RespondMutation(ordinary, ordinaryRequest, LocationAccount); err != nil {
		t.Fatal(err)
	}
	if ordinary.Code != http.StatusSeeOther || ordinary.Header().Get("Location") != "/account" {
		t.Fatalf("ordinary mutation = %d %v", ordinary.Code, ordinary.Header())
	}
	htmxRequest := httptest.NewRequest(http.MethodPost, "/account", nil)
	htmxRequest.Header.Set("HX-Request", "true")
	htmxRequest.Header.Set("HX-Target", "main-content")
	htmx := httptest.NewRecorder()
	if err := RespondMutation(htmx, htmxRequest, LocationAccount); err != nil {
		t.Fatal(err)
	}
	if htmx.Code != http.StatusNoContent || htmx.Header().Get("HX-Location") != "/account" || htmx.Body.Len() != 0 {
		t.Fatalf("HTMX mutation = %d %v %q", htmx.Code, htmx.Header(), htmx.Body.String())
	}
}

func TestSafeViewProjectionUsesOneBindingAndFamilyLevelSessions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	user := usercmd.User{
		ID: "user-1", DisplayName: "Admin", Username: "admin", Status: usercmd.StatusActive,
		Role: usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateTemporary, MustChange: true},
		Binding: &usercmd.Binding{ChannelType: "telegram", Principal: "42", DisplayName: "Admin TG", Provenance: "legacy"},
	}
	view := ProjectUser(user)
	if view.ID != user.ID || view.Role != "administrator" || view.Binding == nil || view.Binding.ChannelType != "telegram" || !view.MustChange {
		t.Fatalf("ProjectUser() = %+v", view)
	}
	session := ProjectSession(usercmd.SessionSummary{
		ID: "family-1", Assurance: usercmd.SessionAssuranceRestricted, CreatedAt: now,
		LastSeenAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour), Version: 3,
	}, "family-1")
	if session.ID != "family-1" || !session.Current || session.Assurance != "restricted" || session.Version != 3 {
		t.Fatalf("ProjectSession() = %+v", session)
	}
}

func TestEmbeddedAssetsServeOffline(t *testing.T) {
	t.Parallel()
	handler, err := Assets()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/vendor/htmx/htmx.min.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() == 0 || response.Header().Get("Cache-Control") == "" {
		t.Fatalf("asset response = %d %v (%d bytes)", response.Code, response.Header(), response.Body.Len())
	}
}
