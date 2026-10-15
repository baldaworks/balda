package backoffice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

func TestHooksApplicationBrowser(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("this command fixture requires a POSIX shell")
	}
	for _, base := range []string{"", "/balda/backoffice"} {
		for _, width := range []int{1440, 390} {
			t.Run(fmt.Sprintf("%s/%d", base, width), func(t *testing.T) { runHooksApplicationBrowser(t, base, width) })
		}
	}
}

func runHooksApplicationBrowser(t *testing.T, base string, width int) {
	t.Helper()
	directory := t.TempDir()
	database := filepath.Join(directory, "state.db")
	provider, err := state.NewSQLiteProvider(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		createAccessTestUser(t, provider.Users(), usercmd.User{ID: string(role), Username: string(role), NormalizedUsername: string(role), DisplayName: string(role), Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	}
	script := filepath.Join(directory, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >> observed-events.jsonl\nprintf '\\n' >> observed-events.jsonl\nprintf '{\"decision\":\"allow\"}'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	config := ResolvedConfig{Server: ResolvedServerConfig{PublicURL: "http://" + server.Listener.Addr().String(), BasePath: base, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour}}
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	management := hooks.NewManagement(provider.HookManagement())
	native, err := hookprovider.New(hooks.NewRunner(provider.HookManagement()), provider.HookManagement(), "provider", map[string]agentconfig.Config{"provider": {Type: agentconfig.AgentTypeClaudeCodeACP, ClaudeCodeACP: &agentconfig.ACPConfig{Cmd: []string{"fixture-acp"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = native.Close() })
	management.ConfigureValidator(native)
	app.hooks = hookbackofficeapp.New(management)
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	runner := hooks.NewRunner(provider.HookManagement())
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__hook_test/run" {
			handler.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, err := runner.Run(r.Context(), hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "synthetic-browser-session", CWD: directory, Source: "chat", Prompt: "synthetic-hook-prompt"})
		if err != nil {
			http.Error(w, "synthetic hook execution failed", http.StatusInternalServerError)
		}
	})
	server.Start()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/hooks.cjs", server.URL, base, fmt.Sprint(width), script)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook application browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	data, err := os.ReadFile(filepath.Join(directory, "observed-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "synthetic-hook-prompt") != 1 {
		t.Fatal("disabled hook executed or enabled hook did not receive the prompt")
	}
	server.Close()
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.NewSQLiteProvider(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	record, err := reopened.HookManagement().Load(t.Context())
	if err != nil || record.Version != 6 || len(record.Definitions) != 2 || record.Definitions[0].Name != "retained" || record.Definitions[0].Enabled || record.Definitions[1].Event != hookcmd.PostToolUse || record.Definitions[1].Enabled || record.Definitions[1].TimeoutSeconds != 0 {
		t.Fatalf("reopened configuration = %+v, %v", record, err)
	}
	audit, err := reopened.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range audit.Events {
		if event.TargetType != usercmd.AuditTargetHook {
			continue
		}
		count++
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), script) || strings.Contains(string(encoded), "synthetic-hook-prompt") {
			t.Fatal("private hook data entered audit")
		}
	}
	if count != 6 {
		t.Fatalf("accepted hook audit events = %d, want 6", count)
	}
}
