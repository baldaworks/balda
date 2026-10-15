//go:build integration && (sqlite || postgres)

package state

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkUserStoreAuditTimeOrder(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	store := provider.Users()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, entry := range []struct {
		id string
		at time.Time
	}{
		{"audit-z", now.Add(100 * time.Millisecond)},
		{"audit-a", now.Add(200 * time.Millisecond)},
		{"audit-b", now},
		{"audit-c", now.Add(200 * time.Millisecond)},
	} {
		user := contractUser(entry.id, entry.id, entry.id == "audit-z", now.Add(-time.Hour))
		if err := store.CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit(entry.id, usercmd.AuditActionUserCreated, user.ID, entry.at)); err != nil {
			t.Fatalf("CreateUser(%s): %v", entry.id, err)
		}
	}
	first, err := store.ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 2})
	if err != nil || len(first.Events) != 2 || first.Events[0].ID != "audit-c" || first.Events[1].ID != "audit-a" || first.NextAfterID != "audit-a" {
		t.Fatalf("first audit page: %+v, %v", first, err)
	}
	second, err := store.ListAuditEvents(t.Context(), usercmd.PageRequest{AfterID: first.NextAfterID, Limit: 2})
	if err != nil || len(second.Events) != 2 || second.Events[0].ID != "audit-z" || second.Events[1].ID != "audit-b" || second.NextAfterID != "" {
		t.Fatalf("second audit page: %+v, %v", second, err)
	}
}
