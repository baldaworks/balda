package balda

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivebackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestBackofficeChatArchiveBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, base := range []string{"", "/balda/backoffice"} {
		for _, width := range []int{1440, 1024, 768, 390} {
			t.Run(fmt.Sprintf("%s/%d", base, width), func(t *testing.T) { runArchiveBrowser(t, base, width) })
		}
	}
}

type archiveBrowserModes struct {
	backoffice.ChatArchiveOperations
	unavailable atomic.Bool
}

func (m *archiveBrowserModes) Inventory(ctx context.Context, q chatarchivecmd.BrowseQuery, a chatarchivecmd.Authority) (chatarchivecmd.Inventory, error) {
	if m.unavailable.Load() {
		return chatarchivecmd.Inventory{}, chatarchivecmd.ErrUnavailable
	}
	return m.ChatArchiveOperations.Inventory(ctx, q, a)
}
func (m *archiveBrowserModes) Transcript(ctx context.Context, id, cursor string, a chatarchivecmd.Authority) (chatarchivecmd.Transcript, error) {
	if m.unavailable.Load() {
		return chatarchivecmd.Transcript{}, chatarchivecmd.ErrUnavailable
	}
	return m.ChatArchiveOperations.Transcript(ctx, id, cursor, a)
}

func runArchiveBrowser(t *testing.T, base string, width int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	p, e := state.NewSQLiteProvider(t.Context(), path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = p.Close() }()
	createSkillsBrowserUsers(t, p)
	hash, e := skillsBrowserPasswordHash()
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	reader := usercmd.User{ID: "reader", Username: "reader", NormalizedUsername: "reader", DisplayName: "Synthetic reader", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	audit := func(action usercmd.AuditAction, target string) usercmd.AuditEvent {
		return usercmd.AuditEvent{ID: uuid.NewString(), Action: action, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: target, Source: "archive-browser-fixture", OccurredAt: time.Now().UTC()}
	}
	if e = p.Users().CreateUser(t.Context(), reader, usercmd.CredentialSecret{UserID: reader.ID, PasswordHash: hash}, audit(usercmd.AuditActionUserCreated, reader.ID)); e != nil {
		t.Fatal(e)
	}
	// Normal session lifecycle and execution produce the browser's primary chat.
	f := newArchiveIntegrationRuntime(t, p, true)
	m := f.manager(t, p, true)
	loc := archiveIntegrationLocator()
	sc := session.SessionContext{Locator: loc, UserID: "human-one"}
	first, e := m.EnsureSession(t.Context(), sc, "Synthetic browser first")
	if e != nil {
		t.Fatal(e)
	}
	inputs := chatarchiveapp.New(p.ChatArchive())
	input := archiveIntegrationInput()
	for n := range 105 {
		v := input
		v.Source = fmt.Sprintf("browser-input-%d", n)
		v.Text = fmt.Sprintf("Synthetic line-%03d\n<script>harmless user</script> %s", n, strings.Repeat("longtext", 30))
		v.Attachments = []chatarchivecmd.Attachment{{Name: "<report>.txt", Kind: "document"}}
		if n%2 != 0 {
			v.Speaker = chatarchivecmd.Speaker{Principal: "human-two", Name: "Synthetic speaker two"}
		}
		if _, e = inputs.RecordInput(t.Context(), first.GetArchiveID(), v); e != nil {
			t.Fatal(e)
		}
	}
	bus := &archiveIntegrationBus{}
	execute := sessionturnapp.NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	execute.SetFinalArchive(inputs)
	if e = execute.Execute(t.Context(), sessionturnapp.ExecutionRequest{ArchiveID: first.GetArchiveID(), Runner: first.GetRunner(), AgentSessionID: first.GetAgentSessionID(), UserID: first.GetUserID(), SessionID: first.GetSessionID(), Text: "hidden provider prompt marker", DedupeKey: "browser-final", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: loc}); e != nil {
		t.Fatal(e)
	}
	providerCalls, deliveries := f.calls.Load(), bus.sends
	if providerCalls != 1 || deliveries == 0 {
		t.Fatal("deterministic provider produced no final execution/delivery")
	}
	if e = m.ResetSession(t.Context(), loc); e != nil {
		t.Fatal(e)
	}
	second, e := m.EnsureSession(t.Context(), sc, "Synthetic browser replacement")
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Stop(t.Context()); e != nil {
		t.Fatal(e)
	}
	// Fill only pagination history with clearly synthetic, older SQL lifetimes.
	for n := range 51 {
		id := uuid.NewString()
		at := now.Add(-time.Duration(n+1) * time.Hour)
		row := chatarchivecmd.Session{ID: id, StableID: id, RuntimeID: id, Persistence: "sqlite", BootID: "synthetic-browser", Label: fmt.Sprintf("Synthetic older %02d", n), Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: id, Locator: "telegram:" + id, Audience: "unknown", Subdivision: "unknown"}, StartedAt: at, ObservedAt: at}
		token := uuid.NewString()
		if _, e = p.ChatArchive().Begin(t.Context(), chatarchivecmd.Intent{Token: token, Kind: "fresh", Session: row, At: at}); e != nil {
			t.Fatal(e)
		}
		if _, e = p.ChatArchive().Activate(t.Context(), token, nil); e != nil {
			t.Fatal(e)
		}
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	p, e = state.NewSQLiteProvider(t.Context(), path)
	if e != nil {
		t.Fatal(e)
	}
	modes := &archiveBrowserModes{ChatArchiveOperations: chatarchivebackofficeapp.New(chatarchiveapp.NewReader(p.ChatArchive()))}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	if e = listener.Close(); e != nil {
		t.Fatal(e)
	}
	app, e := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{ListenAddr: address, PublicURL: "http://" + address, BasePath: base, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, p)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.ConfigureChatArchiveOperations(modes); e != nil {
		t.Fatal(e)
	}
	if e = app.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = app.Stop(context.Background()) }()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var err error
		switch r.URL.Query().Get("action") {
		case "unavailable":
			modes.unavailable.Store(true)
		case "available":
			modes.unavailable.Store(false)
		case "demote", "disable", "restore":
			var u usercmd.User
			var found bool
			u, found, err = p.Users().GetUser(r.Context(), "reader")
			if err == nil && !found {
				err = fmt.Errorf("reader missing")
			}
			if err == nil {
				version := u.Version
				u.Version++
				u.UpdatedAt = time.Now().UTC()
				u.Role = usercmd.RoleAdministrator
				u.Status = usercmd.StatusActive
				if r.URL.Query().Get("action") == "demote" {
					u.Role = usercmd.RoleOperator
				}
				if r.URL.Query().Get("action") == "disable" {
					u.Status = usercmd.StatusDisabled
				}
				err = p.Users().UpdateUser(r.Context(), u, version, audit(usercmd.AuditActionUserUpdated, u.ID))
			}
		case "expire":
			// Expire the real persisted browser family; do not fabricate cookies
			// or replace production authentication with a fake principal.
			db, openErr := sql.Open("sqlite", path)
			if openErr != nil {
				err = openErr
				break
			}
			_, err = db.ExecContext(r.Context(), `UPDATE balda_backoffice_sessions SET created_at = ?, last_seen_at = ?, access_expires_at = ?, refresh_expires_at = ? WHERE user_id = ?`, time.Now().UTC().Add(-3*time.Minute).Format(time.RFC3339Nano), time.Now().UTC().Add(-3*time.Minute).Format(time.RFC3339Nano), time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "reader")
			if closeErr := db.Close(); err == nil {
				err = closeErr
			}
		case "revoke":
			err = p.Users().RevokeUserSessions(r.Context(), "reader", time.Now().UTC(), "synthetic-test", audit(usercmd.AuditActionSessionRevoked, "reader"))
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "synthetic fixture failure", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer controller.Close()
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "qa/backoffice-e2e/chat-archive.cjs", "http://"+address, base, fmt.Sprint(width), first.GetArchiveID(), second.GetArchiveID(), controller.URL)
	cmd.Dir = root
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("archive application browser: %v\n%s", e, output)
	}
	t.Log(string(output))
	if f.calls.Load() != providerCalls || bus.sends != deliveries {
		t.Fatal("browser invoked model or sent chat")
	}
	record, found, e := p.Sessions().GetBySessionID(t.Context(), loc.SessionID)
	if e != nil || !found || record.ArchiveID != second.GetArchiveID() {
		t.Fatal("browser mutated lazy session", e)
	}
	summary, e := p.ChatArchive().Summary(t.Context(), first.GetArchiveID())
	if e != nil || summary.MessageCount != 106 {
		t.Fatalf("browser mutated archive count=%d error=%v", summary.MessageCount, e)
	}
}
