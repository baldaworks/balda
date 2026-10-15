package state

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestMemoryManagementValidation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	a := memorycmd.Authority{UserID: "admin", UserVersion: 1, CredentialVersion: 1, SessionID: "session", SessionVersion: 1, At: now}
	m := memorycmd.Mutation{Authority: a, Record: memorycmd.Record{Version: 1, UpdatedAt: now.Format(time.RFC3339Nano), Entries: []memorycmd.Entry{{Version: 1, CreatedAt: now.Format(time.RFC3339Nano), Fact: "one\nmultiline fact"}}}, Audit: usercmd.AuditEvent{ID: "audit", Action: usercmd.AuditActionMemoryFactCreated, ActorUserID: a.UserID, ActorSessionID: a.SessionID, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetMemoryFact, TargetID: "1", Source: "backoffice", OccurredAt: now}}
	if err := validateMemoryMutation(m); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*memorycmd.Mutation)
	}{
		{"absent identity", func(m *memorycmd.Mutation) { m.Authority.UserID = "" }},
		{"absent session", func(m *memorycmd.Mutation) { m.Authority.SessionID = "" }},
		{"absent user version", func(m *memorycmd.Mutation) { m.Authority.UserVersion = 0 }},
		{"absent credential version", func(m *memorycmd.Mutation) { m.Authority.CredentialVersion = 0 }},
		{"absent session version", func(m *memorycmd.Mutation) { m.Authority.SessionVersion = 0 }},
		{"absent time", func(m *memorycmd.Mutation) { m.Authority.At = time.Time{} }},
		{"overflow", func(m *memorycmd.Mutation) { m.ExpectedVersion = math.MaxInt64; m.Record.Version = math.MinInt64 }},
		{"negative expected version", func(m *memorycmd.Mutation) { m.ExpectedVersion = -1; m.Record.Version = 0 }},
		{"invalid action", func(m *memorycmd.Mutation) { m.Audit.Action = usercmd.AuditActionUserCreated }},
		{"invalid target", func(m *memorycmd.Mutation) { m.Audit.TargetType = usercmd.AuditTargetUser }},
		{"noncanonical target", func(m *memorycmd.Mutation) { m.Audit.TargetID = "01" }},
		{"empty audit ID", func(m *memorycmd.Mutation) { m.Audit.ID = "" }},
		{"duplicate entry identity", func(m *memorycmd.Mutation) { m.Record.Entries = append(m.Record.Entries, m.Record.Entries[0]) }},
		{"invalid created time", func(m *memorycmd.Mutation) {
			m.Record.Entries = []memorycmd.Entry{{Version: 1, CreatedAt: "canary", Fact: "fact"}}
		}},
		{"empty fact", func(m *memorycmd.Mutation) {
			m.Record.Entries = []memorycmd.Entry{{Version: 1, CreatedAt: now.Format(time.RFC3339Nano), Fact: " \n"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := m
			tc.change(&bad)
			if err := validateMemoryMutation(bad); !errors.Is(err, memorycmd.ErrInvalid) {
				t.Fatalf("validation=%v,want invalid", err)
			}
		})
	}
	for _, entries := range [][]memorycmd.Entry{nil, {}} {
		m.Record.Entries = entries
		if err := validateMemoryMutation(m); err != nil {
			t.Fatalf("initialized empty candidate: %v", err)
		}
	}
}
