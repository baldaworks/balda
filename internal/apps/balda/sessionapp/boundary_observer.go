package sessionapp

import (
	"context"
	"fmt"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/sessionmemory"
)

// SessionBoundaryObserverAdapter maps the session lifecycle port to the
// session-memory capture use case at the composition root.
type SessionBoundaryObserverAdapter struct {
	Capture *sessionmemoryapp.BoundaryCapture
}

// BeforeSessionBoundary publishes the old session identity before cleanup.
func (a SessionBoundaryObserverAdapter) BeforeSessionBoundary(ctx context.Context, boundary baldasession.SessionBoundary) error {
	if a.Capture == nil {
		return nil
	}
	reason, err := boundaryReason(boundary.Reason)
	if err != nil {
		return err
	}
	return a.Capture.CaptureSessionBoundary(ctx, sessionmemoryapp.BoundaryCaptureRequest{
		Locator:           boundary.Locator,
		SessionID:         boundary.SessionID,
		AgentSessionID:    boundary.AgentSessionID,
		LineageID:         boundary.LineageID,
		PreviousSessionID: boundary.PreviousSessionID,
		TransitionID:      boundary.TransitionID,
		Reason:            reason,
		OccurredAt:        boundary.OccurredAt,
	})
}

func boundaryReason(reason baldasession.BoundaryReason) (sessionmemory.BoundaryReason, error) {
	switch reason {
	case baldasession.BoundaryReasonReset:
		return sessionmemory.BoundaryReasonReset, nil
	case baldasession.BoundaryReasonClose:
		return sessionmemory.BoundaryReasonClose, nil
	case baldasession.BoundaryReasonRotation:
		return sessionmemory.BoundaryReasonRotation, nil
	case baldasession.BoundaryReasonShutdown:
		return sessionmemory.BoundaryReasonShutdown, nil
	default:
		return "", fmt.Errorf("unsupported session boundary reason %q", reason)
	}
}

var _ baldasession.BoundaryObserver = SessionBoundaryObserverAdapter{}
