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
	err = adapter.BeforeSessionBoundary(context.Background(), baldasession.SessionBoundary{
		Locator:           locator,
		SessionID:         locator.SessionID,
		AgentSessionID:    "adk-1",
		LineageID:         "lineage-1",
		PreviousSessionID: "old-session",
		TransitionID:      "close-1",
		Reason:            baldasession.BoundaryReasonClose,
		OccurredAt:        when,
	})
	if err != nil {
		t.Fatalf("BeforeSessionBoundary() error = %v", err)
	}
	if len(publisher.exports) != 1 || publisher.exports[0].Boundary == nil {
		t.Fatalf("exports = %+v, want one boundary", publisher.exports)
	}
	boundary := publisher.exports[0].Boundary
	if boundary.Scope.Key != "telegram:123:0" || boundary.Reason != sessionmemory.BoundaryReasonClose ||
		boundary.Session.AgentSessionID != "adk-1" || boundary.Session.LineageID != "lineage-1" ||
		boundary.Session.PreviousSessionID != "old-session" || !boundary.OccurredAt.Equal(when) {
		t.Fatalf("boundary = %+v", *boundary)
	}
}
