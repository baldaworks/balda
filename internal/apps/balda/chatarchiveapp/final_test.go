package chatarchiveapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

type finalStoreStub struct {
	InputStore
	message chatarchivecmd.Message
	calls   int
}

func (s *finalStoreStub) CommitFinal(_ context.Context, m chatarchivecmd.Message, members []string) (chatarchivecmd.Message, error) {
	s.calls++
	s.message = m
	return m, nil
}
func (s *finalStoreStub) FindFinal(context.Context, string, string) (chatarchivecmd.Message, error) {
	if s.calls == 0 {
		return chatarchivecmd.Message{}, chatarchivecmd.ErrNotFound
	}
	return s.message, nil
}
func TestFinalPolicyPreservesExactCandidateAndEpoch(t *testing.T) {
	store := &finalStoreStub{}
	svc := New(store)
	final := chatarchivecmd.Message{SessionID: "old-epoch", Source: "reply", Role: "agent", Text: "  exact final\n ", ObservedAt: time.Now().UTC(), Replay: []byte(`{"delivery":"stable"}`)}
	got, err := svc.CommitFinal(t.Context(), final, []string{"first", "second"})
	if err != nil || got.Text != final.Text || got.SessionID != "old-epoch" {
		t.Fatalf("commit=%+v %v", got, err)
	}
	replay, err := svc.FindFinal(t.Context(), "old-epoch", "first")
	if err != nil || replay.Text != final.Text {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	final.Text = ""
	if _, err := svc.CommitFinal(t.Context(), final, []string{"first"}); !errors.Is(err, chatarchivecmd.ErrInvalid) {
		t.Fatalf("empty=%v", err)
	}
}
