//go:build integration && (sqlite || postgres)

package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkMemoryManagement(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	base := contractMCPMutation(t, p)
	a := memorycmd.Authority{UserID: base.Authority.UserID, UserVersion: base.Authority.UserVersion,
		CredentialVersion: base.Authority.CredentialVersion, SessionID: base.Authority.SessionID,
		SessionVersion: base.Authority.SessionVersion, At: base.Authority.At}
	m := memorycmd.Mutation{Record: memorycmd.Record{Version: 1, UpdatedAt: a.At.Format(time.RFC3339Nano),
		Entries: []memorycmd.Entry{{Version: 1, CreatedAt: a.At.Format(time.RFC3339Nano), Fact: "private-fact-canary"}}},
		Authority: a, Audit: usercmd.AuditEvent{ID: "memory-create", Action: usercmd.AuditActionMemoryFactCreated,
			Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID, ActorSessionID: a.SessionID,
			TargetType: usercmd.AuditTargetMemoryFact, TargetID: "1", Source: "provider-contract", OccurredAt: a.At}}
	if err := p.MemoryManagement().CheckAuthority(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := p.MemoryManagement().Commit(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := p.MemoryManagement().Commit(t.Context(), m); !errors.Is(err, memorycmd.ErrConflict) {
		t.Fatalf("stale baseline = %v, want conflict", err)
	}
	var got memorycmd.Record
	read := func() {
		t.Helper()
		value, found, err := p.AppKV().GetJSON(t.Context(), memorycmd.Key)
		if err != nil || !found {
			t.Fatalf("memory read found=%t, error=%v", found, err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err = decodeMemoryRecord(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	read()
	if got.Version != 1 || got.Entries[0].Fact != "private-fact-canary" {
		t.Fatal("committed memory differs from candidate")
	}
	// Updating the global key removes accidental expiry without changing its namespace.
	if err := p.AppKV().SetWithTTL(t.Context(), memorycmd.Key, got, time.Hour); err != nil {
		t.Fatal(err)
	}
	// A duplicate audit ID must roll back the preceding KV write.
	m.ExpectedVersion, m.Record.Version = 1, 2
	m.Record.UpdatedAt = a.At.Add(time.Nanosecond).Format(time.RFC3339Nano)
	m.Record.Entries = nil
	m.Audit.Action = usercmd.AuditActionMemoryFactDeleted
	if err := p.MemoryManagement().Commit(t.Context(), m); !errors.Is(err, memorycmd.ErrUnavailable) {
		t.Fatalf("audit failure = %v, want unavailable", err)
	}
	read()
	if got.Version != 1 || len(got.Entries) != 1 {
		t.Fatal("audit failure changed memory")
	}
	m.Audit.ID = "memory-delete"
	m.Audit.Reason, m.Audit.RequestID, m.Audit.CorrelationID = "private-fact-canary", "private-fact-canary", "private-fact-canary"
	if err := p.MemoryManagement().Commit(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	read()
	var expiry sql.NullString
	if err := contractDatabase(p).db.QueryRowContext(t.Context(), `SELECT expires_at FROM balda_app_kv WHERE namespace = 'balda.app' AND key = 'memory/global'`).Scan(&expiry); err != nil || expiry.Valid {
		t.Fatalf("expiry after commit=%v error=%v", expiry, err)
	}
	if got.Version != 2 || got.Entries != nil {
		t.Fatal("initialized nil empty boundary was not preserved")
	}
	page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.TargetType == usercmd.AuditTargetMemoryFact &&
			(event.Reason == "private-fact-canary" || event.RequestID != "" || event.CorrelationID != "" || event.Source != "backoffice") {
			t.Fatalf("unsafe audit metadata: %s", event.ID)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.ExpectedVersion, m.Record.Version = 2, 3
	m.Record.UpdatedAt = a.At.Add(2 * time.Nanosecond).Format(time.RFC3339Nano)
	m.Audit.ID = "canceled-write"
	if err := p.MemoryManagement().Commit(ctx, m); !errors.Is(err, memorycmd.ErrUnavailable) {
		t.Fatalf("backend cancellation=%v,want unavailable", err)
	}
	read()
	if got.Version != 2 {
		t.Fatal("failed backend transaction changed memory")
	}
	for i, raw := range []any{nil, []any{}, "private-fact-canary", map[string]any{"version": "private-fact-canary"},
		memorycmd.Record{Version: -1}, memorycmd.Record{Version: 3, UpdatedAt: "private-fact-canary"}} {
		if err := p.AppKV().SetJSON(t.Context(), memorycmd.Key, raw); err != nil {
			t.Fatal(err)
		}
		m.ExpectedVersion, m.Record.Version = 2, 3
		m.Record.UpdatedAt, m.Audit.ID = a.At.Add(2*time.Nanosecond).Format(time.RFC3339Nano), fmt.Sprintf("malformed-%d", i)
		if err := p.MemoryManagement().Commit(t.Context(), m); !errors.Is(err, memorycmd.ErrUnavailable) {
			t.Fatalf("malformed durable record %d = %v, want unavailable", i, err)
		}
	}
}

func checkMemoryAuthorityFences(t *testing.T, factory func(*testing.T) contractOpener) {
	for _, name := range []string{"stale user", "stale credential", "stale mfa", "stale session", "unknown session", "request expired", "access expired", "refresh expired", "revoked session", "disabled user", "disabled credential", "operator", "mfa mismatch", "audit actor", "audit time", "audit target", "audit outcome", "candidate version", "candidate entry"} {
		t.Run(name, func(t *testing.T) {
			p := newContractProvider(t, factory(t))
			defer closeContractProvider(t, p)
			now := time.Now().UTC().Truncate(time.Second)
			if name == "refresh expired" {
				now = now.Add(-48 * time.Hour)
			}
			base := contractMCPMutationAt(t, p, now)
			a := memorycmd.Authority{UserID: base.Authority.UserID, UserVersion: base.Authority.UserVersion, CredentialVersion: base.Authority.CredentialVersion, SessionID: base.Authority.SessionID, SessionVersion: base.Authority.SessionVersion, At: base.Authority.At}
			store := p.MemoryManagement().(*sqlMemoryManagementStore)
			want := memorycmd.ErrConflict
			var query string
			switch name {
			case "stale user":
				a.UserVersion++
			case "stale credential":
				a.CredentialVersion++
			case "stale mfa":
				a.MFAVersion++
			case "stale session":
				a.SessionVersion++
			case "unknown session":
				a.SessionID = "missing"
				want = memorycmd.ErrForbidden
			case "request expired":
				a.At = a.At.Add(time.Hour)
				want = memorycmd.ErrForbidden
			case "access expired":
				query = "UPDATE balda_backoffice_sessions SET access_expires_at = '2000-01-01T00:00:00Z'"
				want = memorycmd.ErrForbidden
			case "refresh expired":
				want = memorycmd.ErrForbidden
			case "revoked session":
				query = "UPDATE balda_backoffice_sessions SET revoked_at = '2000-01-01T00:00:00Z'"
				want = memorycmd.ErrForbidden
			case "disabled user":
				query = "UPDATE balda_users SET status = 'disabled'"
				want = memorycmd.ErrForbidden
			case "disabled credential":
				query = "UPDATE balda_users SET credential_state = 'disabled'"
				want = memorycmd.ErrForbidden
			case "operator":
				query = "UPDATE balda_users SET role = 'operator'"
				want = memorycmd.ErrForbidden
			case "mfa mismatch":
				enrolled := contractMCPEnrolledAuthority(t, p, base.Authority)
				a.UserVersion, a.CredentialVersion, a.MFAVersion, a.SessionID = enrolled.UserVersion, enrolled.CredentialVersion, enrolled.MFAVersion, enrolled.SessionID
				query = "UPDATE balda_backoffice_sessions SET mfa_factor_id = '', webauthn_verified_at = ''"
				want = memorycmd.ErrForbidden
			default:
				want = memorycmd.ErrInvalid
			}
			if query != "" {
				if _, err := store.users.db.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			m := memorycmd.Mutation{Authority: a, Record: memorycmd.Record{Version: 1, UpdatedAt: a.At.Format(time.RFC3339Nano), Entries: []memorycmd.Entry{{Version: 1, CreatedAt: a.At.Format(time.RFC3339Nano), Fact: "private-fact-canary"}}}, Audit: usercmd.AuditEvent{ID: "memory-fence", Action: usercmd.AuditActionMemoryFactCreated, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID, ActorSessionID: a.SessionID, TargetType: usercmd.AuditTargetMemoryFact, TargetID: "1", Source: "provider-contract", OccurredAt: a.At}}
			switch name {
			case "audit actor":
				m.Audit.ActorUserID = "other"
			case "audit time":
				m.Audit.OccurredAt = m.Audit.OccurredAt.Add(time.Second)
			case "audit target":
				m.Audit.TargetID = "private-fact-canary"
			case "audit outcome":
				m.Audit.Outcome = usercmd.AuditOutcomeDenied
			case "candidate version":
				m.Record.Version = 2
			case "candidate entry":
				m.Record.Entries[0].CreatedAt = "private-fact-canary"
			}
			if want != memorycmd.ErrInvalid {
				if err := p.MemoryManagement().CheckAuthority(t.Context(), a); !errors.Is(err, want) {
					t.Fatalf("preflight = %v, want %v", err, want)
				}
			}
			if err := p.MemoryManagement().Commit(t.Context(), m); !errors.Is(err, want) {
				t.Fatalf("commit = %v, want %v", err, want)
			}
			if _, found, err := p.AppKV().GetJSON(t.Context(), memorycmd.Key); err != nil || found {
				t.Fatalf("rejected mutation persisted found=%t error=%v", found, err)
			}
			page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range page.Events {
				if event.TargetType == usercmd.AuditTargetMemoryFact {
					t.Fatalf("rejected mutation recorded success audit %s", event.ID)
				}
			}
		})
	}
}
