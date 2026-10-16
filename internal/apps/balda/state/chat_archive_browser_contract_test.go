//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkChatArchiveBrowser(t *testing.T, factory func(*testing.T) contractOpener) {
	for _, condition := range []string{"current", "demoted", "disabled", "credential-disabled", "expired-access", "expired-family", "revoked", "stale-user", "stale-session", "stale-mfa"} {
		t.Run(condition, func(t *testing.T) {
			p := newContractProvider(t, factory(t))
			defer closeContractProvider(t, p)
			base := contractMCPMutation(t, p)
			a := chatarchivecmd.Authority{UserID: base.Authority.UserID, UserVersion: base.Authority.UserVersion, CredentialVersion: base.Authority.CredentialVersion, MFAVersion: base.Authority.MFAVersion, SessionID: base.Authority.SessionID, SessionVersion: base.Authority.SessionVersion, At: base.Authority.At}
			if err := p.ChatArchive().CheckAuthority(t.Context(), a); err != nil {
				t.Fatalf("initial authority=%v", err)
			}
			store := p.Users().(*sqlUserStore)
			switch condition {
			case "demoted", "disabled":
				keeper := contractUser("archive-keeper", "archive-keeper", false, a.At)
				if err := p.Users().CreateUser(t.Context(), keeper, contractSecret(keeper.ID), contractAudit("archive-keeper-create", usercmd.AuditActionUserCreated, keeper.ID, a.At)); err != nil {
					t.Fatal(err)
				}
				u, found, err := p.Users().GetUser(t.Context(), a.UserID)
				if err != nil || !found {
					t.Fatal(err)
				}
				switch condition {
				case "demoted":
					u.Role = usercmd.RoleOperator
				case "disabled":
					u.Status = usercmd.StatusDisabled

				}
				u.Version++
				if err := p.Users().UpdateUser(t.Context(), u, u.Version-1, contractAudit("archive-user-change", usercmd.AuditActionUserRoleChanged, u.ID, a.At)); err != nil {
					t.Fatal(err)
				}
				a.UserVersion = u.Version
			case "credential-disabled":
				if _, err := store.db.ExecContext(t.Context(), store.bind("UPDATE balda_users SET credential_state = ? WHERE user_id = ?"), string(usercmd.CredentialStateDisabled), a.UserID); err != nil {
					t.Fatal(err)
				}
			case "expired-access":
				if _, err := store.db.ExecContext(t.Context(), store.bind("UPDATE balda_backoffice_sessions SET access_expires_at = ? WHERE session_id = ?"), formatMFATime(time.Now().Add(-time.Minute)), a.SessionID); err != nil {
					t.Fatal(err)
				}
			case "expired-family":
				if _, err := store.db.ExecContext(t.Context(), store.bind("UPDATE balda_backoffice_sessions SET created_at = ?, last_seen_at = ?, access_expires_at = ?, refresh_expires_at = ? WHERE session_id = ?"), formatMFATime(time.Now().Add(-3*time.Minute)), formatMFATime(time.Now().Add(-3*time.Minute)), formatMFATime(time.Now().Add(-2*time.Minute)), formatMFATime(time.Now().Add(-time.Minute)), a.SessionID); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, time.Now().UTC(), "test", contractAudit("archive-revoke", usercmd.AuditActionSessionRevoked, a.SessionID, a.At)); err != nil {
					t.Fatal(err)
				}
			case "stale-user":
				a.UserVersion++
			case "stale-session":
				a.SessionVersion++
			case "stale-mfa":
				a.MFAVersion++
			}
			err := p.ChatArchive().CheckAuthority(t.Context(), a)
			if condition == "current" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, chatarchivecmd.ErrForbidden) {
				t.Fatalf("%s read authority=%v", condition, err)
			}
		})
	}
	p := newContractProvider(t, factory(t))
	defer closeContractProvider(t, p)
	now := time.Now().UTC()
	first := chatarchivecmd.Session{ID: "70f4bc27-399a-40f0-bdeb-9023f82bd2b0", StableID: "archive-browser", RuntimeID: "archive-browser", BootID: "private-boot-needle", Persistence: "sqlite", Label: "Planning 100%_! Планирование", Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "browser:0", Locator: "telegram:browser:0", Audience: "personal", Subdivision: "topic", Title: "Personal planning"}, StartedAt: now, ObservedAt: now}
	store := p.ChatArchive()
	if _, err := store.Begin(t.Context(), chatarchivecmd.Intent{Token: "browser-first", Kind: "fresh", Session: first, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Activate(t.Context(), "browser-first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(t.Context(), chatarchivecmd.Message{SessionID: first.ID, Source: "browser-message", Role: "user", Text: "private-message-needle", Speaker: chatarchivecmd.Speaker{Name: "Anna", Principal: "anna"}, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary(t.Context(), first.ID)
	if err != nil || summary.MessageCount != 1 || summary.Speaker.Name != "Anna" {
		t.Fatalf("summary=%+v %v", summary, err)
	}
	for query, want := range map[string]int{"100%_!": 1, "Planning": 1, "Планирование": 1, "private-boot-needle": 0, "private-message-needle": 0} {
		rows, err := store.List(t.Context(), chatarchivecmd.Filter{Query: query, Limit: 50})
		if err != nil || len(rows) != want {
			t.Fatalf("query %q=%d %v", query, len(rows), err)
		}
	}
	if next, err := store.Successor(t.Context(), first.ID); err != nil || next != "" {
		t.Fatalf("missing successor=%q %v", next, err)
	}
	second := first
	second.ID = "44323ced-59af-476a-959d-e79c1a54b896"
	second.StableID = "archive-browser-next"
	second.RuntimeID = second.StableID
	second.Location.AddressKey = "browser:next"
	second.PredecessorID = first.ID
	if _, err := store.Begin(t.Context(), chatarchivecmd.Intent{Token: "browser-second", Kind: "fresh", Session: second, At: now}); err != nil {
		t.Fatal(err)
	}
	if next, err := store.Successor(t.Context(), first.ID); err != nil || next != second.ID {
		t.Fatalf("stored successor=%q %v", next, err)
	}
}
