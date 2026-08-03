package session

import (
	"context"
	"time"
)

// BoundaryReason identifies the lifecycle transition observed by a session
// boundary adapter. The values intentionally mirror the portable memory
// contract without making the session package depend on that integration.
type BoundaryReason string

const (
	BoundaryReasonReset    BoundaryReason = "reset"
	BoundaryReasonClose    BoundaryReason = "close"
	BoundaryReasonRotation BoundaryReason = "rotation"
	BoundaryReasonShutdown BoundaryReason = "shutdown"
)

// SessionBoundary carries the old session identity before destructive runtime
// history cleanup. TransitionID is allocated once per lifecycle operation so
// a downstream publisher can retry the same boundary idempotently.
type SessionBoundary struct {
	Locator           SessionLocator
	SessionID         string
	AgentSessionID    string
	LineageID         string
	PreviousSessionID string
	TransitionID      string
	Reason            BoundaryReason
	OccurredAt        time.Time
}

// BoundaryObserver is a narrow session-owned lifecycle port. Implementations
// must not re-enter Manager lifecycle methods; observer failure is surfaced to
// the caller while reset/close/shutdown semantics remain intact.
type BoundaryObserver interface {
	BeforeSessionBoundary(ctx context.Context, boundary SessionBoundary) error
}
