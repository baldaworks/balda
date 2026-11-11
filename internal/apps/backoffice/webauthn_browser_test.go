package backoffice

import (
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestHTTPAppWebAuthnBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, basePath := range []string{"", testBackofficeBasePath} {
		t.Run("base="+basePath, func(t *testing.T) {
			p, config := newHTTPAppTestState(t)
			for _, width := range []int{1440, 1024, 768, 390} {
				username := fmt.Sprintf("admin%d", width)
				now := time.Now().UTC()
				createAccessTestUser(t, p.Users(), usercmd.User{ID: username, Username: username, NormalizedUsername: username, DisplayName: username, Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
			}
			server := httptest.NewUnstartedServer(nil)
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			config.Server.PublicURL = "http://localhost:" + port
			config.Server.BasePath = basePath
			config.Server.CeremonyTTL = 5 * time.Second
			app, err := newHTTPApp(p.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			server.Config.Handler, err = app.handler()
			if err != nil {
				t.Fatal(err)
			}
			server.Start()
			root, err := filepath.Abs("../../..")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/webauthn.cjs", config.Server.PublicURL+basePath)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("WebAuthn browser workflow: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}
