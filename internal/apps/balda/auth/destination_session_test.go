package auth

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type notificationSessionStore struct {
	record state.SessionRecord
}

func (s notificationSessionStore) Upsert(context.Context, state.SessionRecord) error { return nil }
func (s notificationSessionStore) GetByAddress(context.Context, string, string) (state.SessionRecord, bool, error) {
	return state.SessionRecord{}, false, nil
}
func (s notificationSessionStore) GetBySessionID(_ context.Context, id string) (state.SessionRecord, bool, error) {
	return s.record, id == s.record.SessionID, nil
}
func (s notificationSessionStore) DeleteBySessionID(context.Context, string) error { return nil }
func (s notificationSessionStore) List(context.Context) ([]state.SessionRecord, error) {
	return nil, nil
}

func TestDestinationResolver_ResolveSession(t *testing.T) {
	record := state.SessionRecord{
		SessionID:   "mm-c-target",
		UserID:      "owner",
		ChannelType: "mattermost",
		AddressKey:  "c:channel:target-root",
		AddressJSON: `{"type":"channel","channel_id":"channel","root_id":"target-root"}`,
		Status:      state.SessionStatusActive,
	}
	resolver := NewDestinationResolverWithSessions(nil, notificationSessionStore{record: record})
	got, err := resolver.ResolveSession(context.Background(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Locator.AddressKey != record.AddressKey || got.Locator.SessionID != record.SessionID {
		t.Fatalf("destination = %+v", got.Locator)
	}
	if _, err := resolver.ResolveSession(context.Background(), "mm-c-other"); err == nil {
		t.Fatal("unknown session resolved")
	}
	record.Status = "closed"
	closed := NewDestinationResolverWithSessions(nil, notificationSessionStore{record: record})
	if _, err := closed.ResolveSession(context.Background(), record.SessionID); err == nil {
		t.Fatal("closed session resolved")
	}
}
