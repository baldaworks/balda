package sessionapp

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
)

type boundaryObserverPublisher struct {
	exports []sessionmemorycmd.Export
}

func (p *boundaryObserverPublisher) Publish(_ context.Context, export sessionmemorycmd.Export) error {
	p.exports = append(p.exports, export)
	return nil
}

func TestSessionBoundaryObserverAdapterMapsPortableBoundary(t *testing.T) {
	tests := []struct {
		name         string
		reason       baldasession.BoundaryReason
		wantReason   sessionmemory.BoundaryReason
		transitionID string
	}{
		{name: "reset", reason: baldasession.BoundaryReasonReset, wantReason: sessionmemory.BoundaryReasonReset, transitionID: "reset-1"},
		{name: "close", reason: baldasession.BoundaryReasonClose, wantReason: sessionmemory.BoundaryReasonClose, transitionID: "close-1"},
		{name: "rotation", reason: baldasession.BoundaryReasonRotation, wantReason: sessionmemory.BoundaryReasonRotation, transitionID: "rotation-1"},
		{name: "shutdown", reason: baldasession.BoundaryReasonShutdown, wantReason: sessionmemory.BoundaryReasonShutdown, transitionID: "shutdown-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator, err := deliverycmd.NewLocator("telegram", "123:0", `{"chat_id":123,"topic_id":0}`, "tg-123-0")
			if err != nil {
				t.Fatalf("NewLocator() error = %v", err)
			}
			publisher := &boundaryObserverPublisher{}
			resolver := sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
				"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
					return deliverycmd.LocatorScopePersonal, nil
				},
			})
			adapter := SessionBoundaryObserverAdapter{
				Capture: sessionmemoryapp.NewBoundaryCapture(publisher, resolver),
			}
			when := time.Date(2026, 8, 3, 4, 5, 6, 0, time.UTC)
			wantSession := sessionmemory.SessionRef{
				SessionID:         "old-session",
				AgentSessionID:    "old-agent-session",
				LineageID:         "old-lineage",
				PreviousSessionID: "previous-session",
			}
			err = adapter.BeforeSessionBoundary(context.Background(), baldasession.SessionBoundary{
				Locator:           locator,
				SessionID:         wantSession.SessionID,
				AgentSessionID:    wantSession.AgentSessionID,
				LineageID:         wantSession.LineageID,
				PreviousSessionID: wantSession.PreviousSessionID,
				TransitionID:      test.transitionID,
				Reason:            test.reason,
				OccurredAt:        when,
			})
			if err != nil {
				t.Fatalf("BeforeSessionBoundary() error = %v", err)
			}
			if len(publisher.exports) != 1 || publisher.exports[0].Boundary == nil {
				t.Fatalf("export count = %d, want one boundary", len(publisher.exports))
			}
			boundary := publisher.exports[0].Boundary
			wantScope := sessionmemory.Scope{Key: "telegram:123:0", Kind: sessionmemory.ScopeKindPersonal}
			if boundary.Scope != wantScope || boundary.Reason != test.wantReason ||
				boundary.Session != wantSession || boundary.TransitionID != test.transitionID ||
				!boundary.OccurredAt.Equal(when) {
				t.Fatalf("boundary identity = scope %q kind %q session %q agent %q lineage %q previous %q transition %q reason %q at %s",
					boundary.Scope.Key, boundary.Scope.Kind, boundary.Session.SessionID, boundary.Session.AgentSessionID,
					boundary.Session.LineageID, boundary.Session.PreviousSessionID, boundary.TransitionID,
					boundary.Reason, boundary.OccurredAt.Format(time.RFC3339Nano))
			}
		})
	}
}
