package chatarchiveapp

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestRecordInputRetainsAttributionAcrossReplacement(t *testing.T) {
	const title = "First title"
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := p.ChatArchive()
	at := time.Now().UTC()
	old := chatarchivecmd.Session{ID: "33c9805d-c079-4f04-a52b-77f0ce015d47", StableID: "stable", RuntimeID: "runtime", Persistence: "sqlite", BootID: "boot", Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0", Audience: "unknown", Subdivision: "unknown"}, StartedAt: at, ObservedAt: at}
	if _, err := store.Begin(t.Context(), chatarchivecmd.Intent{Token: "create", Kind: "fresh", Session: old, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Activate(t.Context(), "create", nil); err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	input := chatarchivecmd.Input{Source: "telegram:1:9", Text: "  original\ntext  ", Speaker: chatarchivecmd.Speaker{Principal: "42", Name: "Anna"}, ReceivedAt: at, Location: old.Location}
	input.Location.Title = title
	input.Location.Audience = "group"
	first, err := svc.RecordInput(t.Context(), "33c9805d-c079-4f04-a52b-77f0ce015d47", input)
	if err != nil {
		t.Fatal(err)
	}
	// A transport retry after reset must return its original immutable attribution.
	input.ReceivedAt = input.ReceivedAt.Add(time.Minute)
	replay, err := svc.RecordInput(t.Context(), "replacement", input)
	if err != nil || replay.SessionID != "33c9805d-c079-4f04-a52b-77f0ce015d47" || replay.Sequence != first.Sequence {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	input.Text = "changed"
	if _, err := svc.RecordInput(t.Context(), "33c9805d-c079-4f04-a52b-77f0ce015d47", input); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("conflicting content = %v", err)
	}
	got, err := store.Get(t.Context(), "33c9805d-c079-4f04-a52b-77f0ce015d47")
	if err != nil || got.Location.Title != title || got.Location.Audience != "group" {
		t.Fatalf("metadata = %+v, %v", got, err)
	}
	input.Source = "telegram:1:10"
	input.Location.Title = "Renamed"
	input.Speaker.Name = "Boris"
	if _, err := svc.RecordInput(t.Context(), "33c9805d-c079-4f04-a52b-77f0ce015d47", input); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(t.Context(), "33c9805d-c079-4f04-a52b-77f0ce015d47")
	messages, _ := store.Messages(t.Context(), "33c9805d-c079-4f04-a52b-77f0ce015d47", 0, 100)
	if got.Location.Title != "First title" || len(messages) != 2 || messages[0].Speaker.Name == messages[1].Speaker.Name {
		t.Fatalf("frozen metadata = %+v, %+v", got, messages)
	}
}
