package sessionapp

import (
	"context"
	"testing"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
)

type boundaryTurnStatusStore struct{ cleared []string }

func (*boundaryTurnStatusStore) Start(context.Context, turnstatuscmd.Start) (bool, error) {
	return false, nil
}
func (*boundaryTurnStatusStore) Observe(context.Context, turnstatuscmd.Activity) (bool, error) {
	return false, nil
}
func (*boundaryTurnStatusStore) Complete(context.Context, turnstatuscmd.Completion) (bool, error) {
	return false, nil
}
func (*boundaryTurnStatusStore) Snapshot(context.Context, string, string) (turnstatuscmd.Snapshot, bool, error) {
	return turnstatuscmd.Snapshot{}, false, nil
}
func (s *boundaryTurnStatusStore) ClearSession(_ context.Context, sessionID string) error {
	s.cleared = append(s.cleared, sessionID)
	return nil
}

func TestTurnStatusBoundaryObserver(t *testing.T) {
	store := &boundaryTurnStatusStore{}
	observer := TurnStatusBoundaryObserver{Store: store}
	for _, reason := range []baldasession.BoundaryReason{
		baldasession.BoundaryReasonReset,
		baldasession.BoundaryReasonClose,
		baldasession.BoundaryReasonRotation,
	} {
		if err := observer.BeforeSessionBoundary(t.Context(), baldasession.SessionBoundary{SessionID: "session", Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	if err := observer.BeforeSessionBoundary(t.Context(), baldasession.SessionBoundary{SessionID: "session", Reason: baldasession.BoundaryReasonShutdown}); err != nil {
		t.Fatal(err)
	}
	if len(store.cleared) != 3 {
		t.Fatalf("cleared %d times, want 3", len(store.cleared))
	}
}
