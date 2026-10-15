//go:build integration && (sqlite || postgres)

package state

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkHookManagement(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	base := contractMCPMutation(t, p)
	a := hookcmd.Authority{UserID: base.Authority.UserID, UserVersion: base.Authority.UserVersion, CredentialVersion: base.Authority.CredentialVersion, SessionID: base.Authority.SessionID, SessionVersion: base.Authority.SessionVersion, At: base.Authority.At}
	store := p.HookManagement()
	definition := hookcmd.Definition{Name: "guard", Event: hookcmd.UserPromptSubmit, Command: "/bin/true", Args: []string{"private-command-canary"}, TimeoutSeconds: 10}
	m := hookcmd.Mutation{Record: hookcmd.Record{Version: 1, UpdatedAt: a.At.Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}, Authority: a, Audit: usercmd.AuditEvent{ID: "hook-change", Action: usercmd.AuditActionHookChanged, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID, ActorSessionID: a.SessionID, TargetType: usercmd.AuditTargetHook, TargetID: "guard", Source: "contract", OccurredAt: a.At}}
	if err := store.CheckAuthority(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(t.Context(), m); !errors.Is(err, hookcmd.ErrConflict) {
		t.Fatalf("stale commit = %v, want conflict", err)
	}
	read := func() hookcmd.Record {
		t.Helper()
		record, err := p.HookManagement().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	if record := read(); record.Version != 1 || len(record.Definitions) != 1 || record.Definitions[0].Args[0] != "private-command-canary" {
		t.Fatalf("durable record = %+v", record)
	}
	m.ExpectedVersion, m.Record.Version = 1, 2
	m.Record.UpdatedAt = a.At.Add(time.Nanosecond).Format(time.RFC3339Nano)
	m.Record.Definitions = nil
	if err := store.Commit(t.Context(), m); !errors.Is(err, hookcmd.ErrUnavailable) {
		t.Fatalf("duplicate audit = %v, want unavailable", err)
	}
	if record := read(); record.Version != 1 || len(record.Definitions) != 1 {
		t.Fatal("audit failure changed the record")
	}
	m.Audit.ID, m.Audit.Reason = "hook-remove", "private-command-canary"
	m.Audit.RequestID, m.Audit.CorrelationID = "private-command-canary", "private-command-canary"
	if err := store.Commit(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if record := read(); record.Version != 2 || len(record.Definitions) != 0 {
		t.Fatal("removal did not retain an initialized empty record")
	}
	audit, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range audit.Events {
		if event.TargetType != usercmd.AuditTargetHook {
			continue
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-command-canary") || event.Source != "backoffice" {
			t.Fatal("hook content leaked into audit")
		}
	}
	stale := a
	stale.UserVersion++
	if err := store.CheckAuthority(t.Context(), stale); !errors.Is(err, hookcmd.ErrConflict) {
		t.Fatalf("stale authority = %v, want conflict", err)
	}
	if err := p.AppKV().SetJSON(t.Context(), hookcmd.Key, map[string]any{"version": "invalid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context()); !errors.Is(err, hookcmd.ErrUnavailable) {
		t.Fatalf("corrupt load = %v, want unavailable", err)
	}
}
