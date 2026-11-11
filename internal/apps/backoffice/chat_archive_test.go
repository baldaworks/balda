package backoffice

import (
	"errors"
	"fmt"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivebackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestChatArchiveHTTPAuthority(t *testing.T) {
	for _, base := range []string{"", "/balda/backoffice"} {
		t.Run(base, func(t *testing.T) {
			provider, config := newHTTPAppTestState(t)
			config.Server.BasePath = base
			now := time.Now().UTC()
			for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
				createAccessTestUser(t, provider.Users(), usercmd.User{ID: string(role), Username: string(role), NormalizedUsername: string(role), DisplayName: string(role), Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
			}
			app, err := newHTTPApp(provider.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "administrator")
			operator := loginHTTPAppSession(t, handler, config, "operator")
			for _, fragment := range []bool{false, true} {
				for _, route := range []string{"/chat-sessions", "/chat-sessions/79b22054-a1f1-42f1-a075-e4c52731cafd"} {
					for _, tc := range []struct {
						access string
						status int
					}{{admin.access, 503}, {operator.access, 403}, {"", 401}} {
						r := httptest.NewRequest(http.MethodGet, base+route, nil)
						if tc.access != "" {
							r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: tc.access})
						}
						if fragment {
							r.Header.Set("HX-Request", "true")
							r.Header.Set("HX-Target", "main-content")
						}
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, r)
						if w.Code != tc.status {
							t.Fatalf("GET %s fragment=%v = %d, want %d", route, fragment, w.Code, tc.status)
						}
						if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
							t.Fatal("private read missing no-store")
						}
					}
				}
			}
			r := httptest.NewRequest(http.MethodGet, base+"/overview", nil)
			r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: operator.access})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if strings.Contains(w.Body.String(), `href="`+base+`/chat-sessions"`) {
				t.Fatal("operator sees archive navigation")
			}
		})
	}
}

func TestChatArchiveHTTPTranscript(t *testing.T) {
	for _, base := range []string{"", "/balda/backoffice"} {
		t.Run(base, func(t *testing.T) {
			provider, config := newHTTPAppTestState(t)
			config.Server.BasePath = base
			now := time.Now().UTC()
			createAccessTestUser(t, provider.Users(), usercmd.User{ID: "administrator", Username: "administrator", NormalizedUsername: "administrator", DisplayName: "Administrator", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
			id := "79b22054-a1f1-42f1-a075-e4c52731cafd"
			store := provider.ChatArchive()
			session := chatarchivecmd.Session{ID: id, StableID: "stable-private-marker", RuntimeID: "runtime-private-marker", BootID: "boot-private-marker", Persistence: "sqlite", Label: "<script>label</script>", Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "10:7", Locator: "telegram:10:7", Title: "Personal <location>", Audience: "personal", Subdivision: "topic", SubdivisionLabel: "Planning"}, StartedAt: now, ObservedAt: now, Partial: true, CoverageStart: now}
			if _, err := store.Begin(t.Context(), chatarchivecmd.Intent{Token: "test-create", Kind: "fresh", Session: session, At: now}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Activate(t.Context(), "test-create", nil); err != nil {
				t.Fatal(err)
			}
			for n := range 105 {
				m := chatarchivecmd.Message{SessionID: id, Source: fmt.Sprintf("user-%d", n), Role: "user", Text: fmt.Sprintf("line-%03d\n<script>message</script>", n), Speaker: chatarchivecmd.Speaker{Principal: "user-actual", Name: "Anna <name>"}, ObservedAt: now.Add(time.Duration(n) * time.Second), Attachments: []chatarchivecmd.Attachment{{Name: "<file>.txt", Kind: "document"}}}
				if _, err := store.Record(t.Context(), m); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.CommitFinal(t.Context(), chatarchivecmd.Message{SessionID: id, Source: "final-private-source", Role: "agent", Text: "Visible final", ObservedAt: now.Add(106 * time.Second), Replay: []byte("private-replay-secret")}, []string{"private-member-secret"}); err != nil {
				t.Fatal(err)
			}
			app, err := newHTTPApp(provider.Users(), config)
			if err != nil {
				t.Fatal(err)
			}
			reader := chatarchiveapp.NewReader(store)
			app.chatArchive = chatarchivebackofficeapp.New(reader)
			handler, err := app.handler()
			if err != nil {
				t.Fatal(err)
			}
			admin := loginHTTPAppSession(t, handler, config, "administrator")
			get := func(path string, fragment bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, base+path, nil)
				r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: admin.access})
				if fragment {
					r.Header.Set("HX-Request", "true")
					r.Header.Set("HX-Target", "main-content")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for _, fragment := range []bool{false, true} {
				list := get("/chat-sessions?q=Planning&channel=telegram&status=active", fragment)
				if list.Code != 200 || !strings.Contains(list.Body.String(), "106") || !strings.Contains(list.Body.String(), "Anna &lt;name&gt;") {
					t.Fatalf("list=%d %s", list.Code, list.Body.String())
				}
				detail := get("/chat-sessions/"+id+"?q=Planning", fragment)
				body := detail.Body.String()
				if detail.Code != 200 || !strings.Contains(body, "Visible final") || !strings.Contains(body, "Partial coverage") || !strings.Contains(body, "&lt;script&gt;message") || !strings.Contains(body, "&lt;file&gt;.txt") {
					t.Fatalf("detail=%d %s", detail.Code, body)
				}
				if strings.Contains(body, "line-005") || !strings.Contains(body, "line-006") || strings.Count(body, "archive-message-user") != 99 {
					t.Fatal("newest transcript window wrong")
				}
				for _, secret := range []string{"private-replay-secret", "private-member-secret", "final-private-source", "runtime-private-marker", "stable-private-marker", "boot-private-marker", "<script>message"} {
					if strings.Contains(body, secret) {
						t.Fatalf("private or executable content exposed: %s", secret)
					}
				}
				if strings.Contains(body, "<html") == fragment {
					t.Fatal("full/fragment rendering wrong")
				}
				if !strings.Contains(detail.Header().Get("Cache-Control"), "no-store") {
					t.Fatal("missing private response")
				}
			}
			principal, err := app.security.ValidateAccess(t.Context(), admin.access)
			if err != nil {
				t.Fatal(err)
			}
			authority := chatarchivecmd.Authority{UserID: principal.User.ID, UserVersion: principal.User.Version, CredentialVersion: principal.User.Credential.Version, MFAVersion: principal.MFAVersion, SessionID: principal.FamilyID, SessionVersion: principal.Version, At: now}
			transcript, err := reader.Transcript(t.Context(), id, "", authority)
			if err != nil {
				t.Fatal(err)
			}
			older, err := reader.Transcript(t.Context(), id, transcript.Older, authority)
			if err != nil || len(older.Messages) != 6 || older.Messages[0].Sequence != 1 {
				t.Fatalf("older messages=%d %v", len(older.Messages), err)
			}
			for _, path := range []string{"/chat-sessions?cursor=garbage", "/chat-sessions?status=made-up", "/chat-sessions/not-a-uuid", "/chat-sessions/" + id + "?cursor=bad"} {
				if got := get(path, true); got.Code != 400 {
					t.Fatalf("invalid %s=%d", path, got.Code)
				}
			}
			if got := get("/chat-sessions/9e26f7b0-13cd-4c4e-9ed6-cd68b760849f", false); got.Code != 404 {
				t.Fatalf("missing=%d", got.Code)
			}
			if got := get("/chat-sessions?q=boot-private-marker", false); strings.Contains(got.Body.String(), "&lt;script&gt;label") {
				t.Fatal("search included private runtime metadata")
			}
			before, err := store.Summary(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Inventory(t.Context(), chatarchivecmd.BrowseQuery{}, authority); err != nil {
				t.Fatal(err)
			}
			after, err := store.Summary(t.Context(), id)
			if err != nil || before.MessageCount != after.MessageCount || before.Session.Status != after.Session.Status {
				t.Fatal("reading mutated archive")
			}

			// Tied inventory timestamps must neither duplicate nor skip lifetimes.
			for n := range 52 {
				candidate := session
				candidate.ID = fmt.Sprintf("a0000000-0000-4000-8000-%012d", n)
				candidate.StableID = fmt.Sprintf("tie-%d", n)
				candidate.RuntimeID = candidate.StableID
				candidate.Location.AddressKey = fmt.Sprintf("tie:%d", n)
				candidate.Location.Locator = "telegram:" + candidate.Location.AddressKey
				candidate.Label = "Tied lifetime"
				if _, err := store.Begin(t.Context(), chatarchivecmd.Intent{Token: fmt.Sprintf("tie-create-%d", n), Kind: "fresh", Session: candidate, At: now}); err != nil {
					t.Fatal(err)
				}
			}
			query := chatarchivecmd.BrowseQuery{Query: "Tied", Channel: "telegram", Status: "pending"}
			first, err := reader.Inventory(t.Context(), query, authority)
			if err != nil || len(first.Rows) != 50 || first.Older == "" {
				t.Fatalf("first inventory=%d %v", len(first.Rows), err)
			}
			query.Cursor = first.Older
			second, err := reader.Inventory(t.Context(), query, authority)
			if err != nil || len(second.Rows) != 2 || second.Older != "" {
				t.Fatalf("second inventory=%d %v", len(second.Rows), err)
			}
			seen := map[string]bool{}
			for _, row := range append(first.Rows, second.Rows...) {
				if seen[row.Session.ID] {
					t.Fatal("duplicate lifetime across keysets")
				}
				seen[row.Session.ID] = true
			}
			query.Channel = "slackagent"
			if _, err := reader.Inventory(t.Context(), query, authority); !errors.Is(err, chatarchivecmd.ErrInvalid) {
				t.Fatalf("cross-filter cursor=%v", err)
			}
			otherID := second.Rows[0].Session.ID
			if _, err := reader.Transcript(t.Context(), otherID, transcript.Older, authority); !errors.Is(err, chatarchivecmd.ErrInvalid) {
				t.Fatalf("cross-session cursor=%v", err)
			}
			stale := authority
			stale.UserVersion++
			if _, err := reader.Inventory(t.Context(), chatarchivecmd.BrowseQuery{}, stale); !errors.Is(err, chatarchivecmd.ErrForbidden) {
				t.Fatalf("stale read=%v", err)
			}
			if got := get("/chat-sessions?status=pending&q=Tied", true); got.Code != 200 || !strings.Contains(got.Body.String(), "Activation pending") || strings.Count(got.Body.String(), `id="main-content"`) != 1 {
				t.Fatalf("pending fragment=%d", got.Code)
			}
			revoke := performAccessMutation(t, handler, config, base+"/account/sessions/"+principal.FamilyID+"/revoke", url.Values{"csrf_token": {admin.csrf}, "confirm_current": {"yes"}}, admin.access, admin.csrf, false)
			if revoke.Code != 303 {
				t.Fatalf("revoke=%d", revoke.Code)
			}
			if _, err := reader.Transcript(t.Context(), id, "", authority); !errors.Is(err, chatarchivecmd.ErrForbidden) {
				t.Fatalf("revoked direct read=%v", err)
			}
			if got := get("/chat-sessions/"+id, true); got.Code != 401 {
				t.Fatalf("revoked HTTP=%d", got.Code)
			}
		})
	}
}
