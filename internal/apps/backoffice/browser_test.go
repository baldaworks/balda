package backoffice

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// This opt-in browser gate exercises the real HTTP/security stack with isolated
// SQLite state. It requires the optional qa/backoffice-e2e Playwright tooling.
func TestHTTPAppBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		username := string(role)
		createAccessTestUser(t, provider.Users(), usercmd.User{
			ID: username, DisplayName: username, Username: username, NormalizedUsername: username,
			Status: usercmd.StatusActive, Role: role, Primary: role == usercmd.RoleAdministrator,
			Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
			Version:    1, CreatedAt: now, UpdatedAt: now,
		})
	}
	server := httptest.NewUnstartedServer(nil)
	config.Server.PublicURL = "http://" + server.Listener.Addr().String()
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	var refreshPosts atomic.Int32
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session/refresh" && r.Method == http.MethodPost {
			refreshPosts.Add(1)
		}
		handler.ServeHTTP(w, r)
	})
	server.Start()
	defer server.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/runtime.cjs", server.URL)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("authenticated browser workflow: %v\n%s", err, output)
	}
	if got := refreshPosts.Load(); got != 4 {
		t.Fatalf("refresh POST count = %d, want one per browser context (4)", got)
	}
	t.Log(string(output))
}
