package audit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestServiceFiltersBoundedTypedSecurityEvents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{events: []usercmd.AuditEvent{
		{ID: "event-1", Action: usercmd.AuditActionRefreshSucceeded, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: "family-1", Source: "security", OccurredAt: now},
		{ID: "event-2", Action: usercmd.AuditActionRefreshReplay, Outcome: usercmd.AuditOutcomeDenied, TargetType: usercmd.AuditTargetSession, TargetID: "family-2", Source: "security", OccurredAt: now.Add(time.Minute)},
		{ID: "event-3", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: "family-3", Source: "security", OccurredAt: now.Add(2 * time.Minute)},
	}}
	service := NewService(store)
	admin := usercmd.User{Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator}
	page, err := service.List(t.Context(), admin, Request{Limit: 10, Action: usercmd.AuditActionRefreshReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != "event-2" || page.NextAfterID != "" {
		t.Fatalf("filtered page = %+v", page)
	}

	operator := usercmd.User{Status: usercmd.StatusActive, Role: usercmd.RoleOperator}
	if _, err := service.List(t.Context(), operator, Request{Limit: 10}); !errors.Is(err, usercmd.ErrForbidden) {
		t.Fatalf("operator error = %v", err)
	}
	if _, err := service.List(t.Context(), admin, Request{Limit: 10, AfterID: strings.Repeat("x", 257)}); !errors.Is(err, usercmd.ErrInvalid) {
		t.Fatalf("oversized cursor error = %v", err)
	}
	if _, err := service.List(t.Context(), admin, Request{Limit: 10, Action: "unknown.action"}); !errors.Is(err, usercmd.ErrInvalid) {
		t.Fatalf("unknown action error = %v", err)
	}
}

type fakeStore struct {
	events []usercmd.AuditEvent
}

func (s *fakeStore) ListAuditEvents(_ context.Context, page usercmd.PageRequest) (usercmd.AuditPage, error) {
	start := 0
	for start < len(s.events) && s.events[start].ID <= page.AfterID {
		start++
	}
	end := min(start+page.Limit, len(s.events))
	result := usercmd.AuditPage{Events: append([]usercmd.AuditEvent(nil), s.events[start:end]...)}
	if end < len(s.events) {
		result.NextAfterID = s.events[end-1].ID
	}
	return result, nil
}
