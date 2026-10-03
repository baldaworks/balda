package auth

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestDestinationResolver_ResolveSession(t *testing.T) {
	record := state.SessionRecord{
		SessionID:   "mm-c-target",
		UserID:      "owner",
		ChannelType: "mattermost",
		AddressKey:  "c:channel:target-root",
		AddressJSON: `{"type":"channel","channel_id":"channel","root_id":"target-root"}`,
		Status:      state.SessionStatusActive,
	}
	lookup := func(_ context.Context, id string) (envelopetarget.Resolved, bool, error) {
		if id != record.SessionID || record.Status != state.SessionStatusActive {
			return envelopetarget.Resolved{}, false, nil
		}
		locator, err := deliverycmd.NewLocator(record.ChannelType, record.AddressKey, record.AddressJSON, record.SessionID)
		return envelopetarget.Resolved{Locator: locator, Principal: record.UserID}, true, err
	}
	resolver := NewDestinationResolverWithSessions(nil, lookup)
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
	closed := NewDestinationResolverWithSessions(nil, lookup)
	if _, err := closed.ResolveSession(context.Background(), record.SessionID); err == nil {
		t.Fatal("closed session resolved")
	}
}
