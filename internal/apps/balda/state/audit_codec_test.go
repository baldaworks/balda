package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestAuditCodecDualReadAndLegacyWrite(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	canonical := usercmd.AuditEvent{ID: "event", Action: usercmd.AuditActionWebAuthLoginSucceeded,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorWebAuthSessionID: "web-auth", TargetType: usercmd.AuditTargetWebAuthSession,
		TargetID: "web-auth", Source: "test", OccurredAt: now}
	stored, err := encodeStoredAuditLegacy(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Action != legacyAuditActionWebAuthLoginSucceeded || stored.TargetType != legacyAuditTargetWebAuthSession || stored.Encoding != "" {
		t.Fatalf("legacy encoding = %+v", stored)
	}
	decoded, err := decodeStoredAudit(stored)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Action != canonical.Action || decoded.TargetType != canonical.TargetType || decoded.Encoding != usercmd.AuditEncodingLegacyWebAuth {
		t.Fatalf("legacy decode = %+v", decoded)
	}
	decoded, err = decodeStoredAudit(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Encoding != usercmd.AuditEncodingCanonical {
		t.Fatalf("canonical provenance = %q", decoded.Encoding)
	}
}

func TestSQLiteAuditStoreWritesLegacyAndReadsMixedHistory(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	sqlite := provider.(*sqliteProvider)
	store := sqlite.users.(*sqlUserStore)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	legacyWrite := usercmd.AuditEvent{ID: "legacy-write", Action: usercmd.AuditActionWebAuthLoginSucceeded,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorWebAuthSessionID: "web-auth", TargetType: usercmd.AuditTargetWebAuthSession,
		TargetID: "web-auth", Source: "test", OccurredAt: now}
	tx, err := store.begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.insertAudit(t.Context(), tx, legacyWrite); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var rawAction, rawTarget string
	if err := sqlite.db.QueryRowContext(t.Context(), `SELECT action,target_type FROM balda_security_audit_events WHERE event_id='legacy-write'`).Scan(&rawAction, &rawTarget); err != nil {
		t.Fatal(err)
	}
	if rawAction != string(legacyAuditActionWebAuthLoginSucceeded) || rawTarget != string(legacyAuditTargetWebAuthSession) {
		t.Fatalf("stored action=%q target=%q", rawAction, rawTarget)
	}
	if _, err := sqlite.db.ExecContext(t.Context(), `INSERT INTO balda_security_audit_events
		(event_id,actor_web_auth_session_id,action,target_type,target_id,outcome,source,occurred_at)
		VALUES (?,?,?,?,?,?,?,?)`, "canonical-row", "web-auth", usercmd.AuditActionWebAuthLogout,
		usercmd.AuditTargetWebAuthSession, "web-auth", usercmd.AuditOutcomeSucceeded, "test", formatUserTime(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	page, err := provider.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].ID != "canonical-row" || page.Events[0].Encoding != usercmd.AuditEncodingCanonical ||
		page.Events[1].ID != "legacy-write" || page.Events[1].Encoding != usercmd.AuditEncodingLegacyWebAuth {
		t.Fatalf("mixed history = %+v", page.Events)
	}
}

func TestAuditCodecLeavesUnrelatedEventsUnchanged(t *testing.T) {
	event := usercmd.AuditEvent{ID: "event", Action: usercmd.AuditActionUserUpdated,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: "user", TargetType: usercmd.AuditTargetUser,
		TargetID: "user", Source: "test", OccurredAt: time.Now().UTC()}
	stored, err := encodeStoredAuditLegacy(event)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Action != event.Action || stored.TargetType != event.TargetType {
		t.Fatalf("unrelated event changed: before=%+v after=%+v", event, stored)
	}
}
