//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

func checkChatArchiveFinal(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "final.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if p != nil {
			_ = p.Close()
		}
	}()
	at := time.Now().UTC()
	epoch := "33c9805d-c079-4f04-a52b-77f0ce015d47"
	session := chatarchivecmd.Session{ID: epoch, StableID: "stable", RuntimeID: "runtime", Persistence: "sqlite", BootID: "boot", Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0"}, StartedAt: at, ObservedAt: at}
	s := p.ChatArchive()
	if _, err := s.Begin(t.Context(), chatarchivecmd.Intent{Token: "create", Kind: "fresh", Session: session, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(t.Context(), "create", nil); err != nil {
		t.Fatal(err)
	}
	final := chatarchivecmd.Message{SessionID: epoch, Source: "final:batch-ab", Role: "agent", Text: "  exact final\n", ObservedAt: at, Replay: []byte(`{"delivery_id":"stable"}`)}
	first, err := s.CommitFinal(t.Context(), final, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CommitFinal(t.Context(), final, []string{"a", "b"})
	if err != nil || again.Sequence != first.Sequence {
		t.Fatalf("retry=%+v %v", again, err)
	}
	// A new batch cannot claim an already committed constituent. The attempted
	// final and sequence allocation must roll back with the conflicting claim.
	conflicting := final
	conflicting.Source = "final:batch-bc"
	conflicting.Text = "different final"
	if _, err := s.CommitFinal(t.Context(), conflicting, []string{"b", "c"}); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("overlap=%v", err)
	}
	if _, err := s.FindFinal(t.Context(), epoch, "c"); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("rolled-back claim=%v", err)
	}
	if _, err := s.FindMessage(t.Context(), conflicting.Source); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("rolled-back final=%v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = nil
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"a", "b"} {
		replay, err := p.ChatArchive().FindFinal(t.Context(), epoch, member)
		if err != nil || replay.Source != first.Source || replay.Text != final.Text || string(replay.Replay) != string(final.Replay) {
			t.Fatalf("reopened=%+v %v", replay, err)
		}
	}
	messages, err := p.ChatArchive().Messages(t.Context(), epoch, 0, 100)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%+v %v", messages, err)
	}
}
