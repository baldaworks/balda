package balda

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/skillbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"golang.org/x/crypto/bcrypt"
)

const skillsBrowserValidPassword = "correct horse battery staple"

var skillsBrowserPasswordHash = sync.OnceValues(func() (string, error) {
	b, e := bcrypt.GenerateFromPassword([]byte(skillsBrowserValidPassword), bcrypt.MinCost)
	return string(b), e
})

func TestBackofficeSkillsBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, base := range []string{"", "/balda/backoffice"} {
		for _, width := range []int{1440, 390} {
			t.Run(fmt.Sprintf("%s/%d", base, width), func(t *testing.T) { runSkillsBrowserWorkflow(t, base, width) })
		}
	}
}
func runSkillsBrowserWorkflow(t *testing.T, base string, width int) {
	t.Helper()
	browserHome := os.Getenv("HOME")
	f := newSkillsRuntimeFixture(t, width == 390)
	f.write(t, "v1")
	installSkillsFixturePlugin(t, f)
	createSkillsBrowserUsers(t, f.provider)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	if e = listener.Close(); e != nil {
		t.Fatal(e)
	}
	app, e := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{ListenAddr: address, PublicURL: "http://" + address, BasePath: base, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, f.provider)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.ConfigurePluginsOperations(f.ui); e != nil {
		t.Fatal(e)
	}
	if e = app.ConfigureSkillsOperations(skillbackofficeapp.New(f.skills)); e != nil {
		t.Fatal(e)
	}
	if e = app.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	stages := make(chan pluginsBrowserStage)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s := pluginsBrowserStage{action: r.URL.Query().Get("action"), result: make(chan any, 1)}
		select {
		case stages <- s:
		case <-r.Context().Done():
			return
		}
		select {
		case result := <-s.result:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-r.Context().Done():
		}
	}))
	defer controller.Close()
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "qa/backoffice-e2e/skills.cjs", "http://"+address, base, fmt.Sprint(width), f.definitionSource(), f.sourceRef(), controller.URL)
	command.Dir = root
	// Chromium tooling uses the developer's installed browser cache; catalog
	// discovery remains isolated in the parent Go process's temporary HOME.
	command.Env = append(os.Environ(), "HOME="+browserHome)
	type execution struct {
		output []byte
		err    error
	}
	finished := make(chan execution, 1)
	go func() { o, e := command.CombinedOutput(); finished <- execution{o, e} }()
	var pinned state.SessionRecord
	var releaseCatalog func()
	defer func() {
		if releaseCatalog != nil {
			releaseCatalog()
		}
	}()
	for {
		select {
		case result := <-finished:
			if result.err != nil {
				t.Fatalf("Skills browser: %v\n%s", result.err, result.output)
			}
			t.Log(string(result.output))
			if pinned.RuntimeSnapshotID == "" {
				t.Fatal("browser did not pin real session")
			}
			return
		case s := <-stages:
			switch s.action {
			case "pin":
				l := f.createSession(t, "browser-skills-pin")
				pinned = f.record(t, l)
			case "version2":
				f.write(t, "v2")
			case "audit-fail":
				f.store.fail = true
			case "audit-clear":
				f.store.fail = false
			case "publish-fail":
				f.gate.failPublish = true
			case "publish-clear":
				f.gate.failPublish = false
			case "source-remove":
				if e := os.RemoveAll(f.source); e != nil {
					t.Fatal(e)
				}
			case "external-disable":
				i := f.inventory(t)
				for _, entry := range i.Entries {
					if entry.Ownership == skillcmd.OwnershipManaged {
						f.change(t, skillcmd.OperationDisable, entry.ID.Source)
					}
				}
			case "catalog-block":
				releaseCatalog, e = f.catalog.LockCatalogMutation(ctx)
				if e != nil {
					t.Fatal(e)
				}
				f.gate.entered = make(chan struct{}, 1)
			case "revoke-blocked":
				select {
				case <-f.gate.entered:
				case <-ctx.Done():
					t.Fatal("no real catalog gate entry")
				}
				at := time.Now().UTC()
				event := usercmd.AuditEvent{ID: "skills-browser-revoke", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: "administrator", Source: "skills-browser-fixture", OccurredAt: at}
				if e = f.provider.Users().RevokeUserSessions(ctx, "administrator", at, "synthetic-test", event); e != nil {
					t.Fatal(e)
				}
				releaseCatalog()
				releaseCatalog = nil
			case "status":
			default:
				t.Fatalf("unknown stage %s", s.action)
			}
			i, e := f.provider.SkillManagement().Observe(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			snapshot, e := f.catalog.Store().Application()
			if e != nil {
				t.Fatal(e)
			}
			result := map[string]any{"pending": i.Record.Pending, "generation": i.Record.Generation, "installed": false, "enabled": false, "selected": false, "pinned": pinned.RuntimeSnapshotID}
			for _, entry := range i.Record.Installations {
				if !entry.Removed {
					result["installed"] = true
					result["enabled"] = entry.Enabled
					result["id"] = entry.Descriptor.ID.Name
					_, selected := snapshot.Sources[entry.Descriptor.ID]
					result["selected"] = selected
					if pinned.RuntimeSnapshotID != "" {
						f.assertSkill(t, pinned.RuntimeSnapshotID, entry.Descriptor.ID, "v1")
					}
				}
			}
			if pinned.RuntimeSnapshotID != "" {
				saved, found, e := f.provider.Sessions().GetBySessionID(t.Context(), pinned.SessionID)
				if e != nil || !found || saved.RuntimeSnapshotID != pinned.RuntimeSnapshotID {
					t.Fatal("browser management reset or repinned session")
				}
			}
			s.result <- result
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func createSkillsBrowserUsers(t *testing.T, p state.Provider) {
	t.Helper()
	hash, e := skillsBrowserPasswordHash()
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		id := string(role)
		user := usercmd.User{ID: id, Username: id, NormalizedUsername: id, DisplayName: id, Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
		audit := usercmd.AuditEvent{ID: "skills-browser-" + id, Action: usercmd.AuditActionUserCreated, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: id, Source: "skills-browser-fixture", OccurredAt: now}
		if e := p.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: id, PasswordHash: hash}, audit); e != nil {
			t.Fatal(e)
		}
	}
}
