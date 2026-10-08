package backoffice

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/aliasbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/aliases"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestAliasesBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{
		ID: "administrator", Username: "administrator", NormalizedUsername: "administrator",
		DisplayName: "administrator", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive,
		Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	server := httptest.NewUnstartedServer(nil)
	config.Server.PublicURL = "http://" + server.Listener.Addr().String()
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	app.aliases = aliasbackofficeapp.New(aliases.New(provider.Aliases()))
	server.Config.Handler, err = app.handler()
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Start()
	defer server.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/aliases.cjs", server.URL)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("authenticated alias browser workflow: %v\n%s", err, output)
	}
	t.Log(string(output))
}
