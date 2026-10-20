package session

import (
	"strings"
	"testing"
)

func TestResolveProgressTargetReturnsServerOwnedContext(t *testing.T) {
	t.Parallel()

	conversation := testTelegramLocator(10, 42)
	service, err := NewSessionLocator("schedule", "sch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		`{"run_id":"sch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "sch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{sessions: map[string]*TopicSession{
		conversation.SessionID: {
			sessionID: conversation.SessionID, locator: conversation, kind: KindDM,
		},
		service.SessionID: {
			sessionID: service.SessionID, locator: service, kind: KindSchedule, ownerJobID: "scheduled-job",
		},
	}}

	for _, test := range []struct {
		name  string
		input SessionLocator
		kind  Kind
		owner string
	}{
		{name: "conversation", input: conversation, kind: KindDM},
		{name: "service", input: service, kind: KindSchedule, owner: "scheduled-job"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, err := manager.ResolveProgressTarget(t.Context(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if target.Locator != test.input || target.SessionID != test.input.SessionID ||
				target.Kind != test.kind || target.OwnerJobID != test.owner {
				t.Fatalf("target = %+v", target)
			}
		})
	}
}

func TestResolveProgressTargetRejectsInvalidTarget(t *testing.T) {
	t.Parallel()

	locator := testTelegramLocator(10, 42)
	manager := &Manager{sessions: map[string]*TopicSession{
		locator.SessionID: {
			sessionID: locator.SessionID, locator: locator, kind: KindSchedule,
		},
	}}

	tests := []struct {
		name string
		edit func(SessionLocator) SessionLocator
		want string
	}{
		{name: "inactive", edit: func(in SessionLocator) SessionLocator { in.SessionID = "tg-99-0"; return in }, want: "not found"},
		{name: "mismatched address", edit: func(in SessionLocator) SessionLocator { in.AddressKey = "11:42"; return in }, want: "does not match"},
		{name: "invalid owner", edit: func(in SessionLocator) SessionLocator { return in }, want: "requires an owner job"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := manager.ResolveProgressTarget(t.Context(), test.edit(locator))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
