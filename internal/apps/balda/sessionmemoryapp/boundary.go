package sessionmemoryapp

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/baldaworks/balda/sessionmemory"
)

// BoundaryCaptureRequest contains the old session identity that must be
// exported before its runtime history is reset, closed, rotated, or discarded
// during shutdown.
type BoundaryCaptureRequest struct {
	Locator           deliverycmd.Locator
	SessionID         string
	AgentSessionID    string
	LineageID         string
	PreviousSessionID string
	TransitionID      string
	Reason            sessionmemory.BoundaryReason
	OccurredAt        time.Time
}

// BoundaryCapture normalizes lifecycle boundaries and publishes a neutral
// export to the durable handoff. It never invokes native derivation directly.
type BoundaryCapture struct {
	publisher ExportPublisher
	resolver  ScopeResolver
	now       func() time.Time
}

// NewBoundaryCapture creates a lifecycle capture service. A nil publisher is
// a deterministic disabled-mode no-op.
func NewBoundaryCapture(publisher ExportPublisher, resolver ScopeResolver) *BoundaryCapture {
	return &BoundaryCapture{
		publisher: publisher,
		resolver:  resolver,
		now:       time.Now,
	}
}

// Capture validates one boundary and hands it to the durable publisher.
func (c *BoundaryCapture) Capture(ctx context.Context, req BoundaryCaptureRequest) (CaptureResult, error) {
	if c == nil || c.publisher == nil {
		return CaptureResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CaptureResult{}, err
	}
	scope, err := c.resolver.Resolve(req.Locator)
	if err != nil {
		return CaptureResult{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.Locator.SessionID)
	}
	agentSessionID := strings.TrimSpace(req.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = sessionID
	}
	occurredAt := req.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = c.currentTime()
	}
	boundary, err := sessionmemory.NewBoundary(
		scope,
		sessionmemory.SessionRef{
			SessionID:         sessionID,
			AgentSessionID:    agentSessionID,
			LineageID:         strings.TrimSpace(req.LineageID),
			PreviousSessionID: strings.TrimSpace(req.PreviousSessionID),
		},
		req.TransitionID,
		req.Reason,
		occurredAt.UTC(),
	)
	if err != nil {
		return CaptureResult{}, err
	}
	export, err := sessionmemorycmd.NewBoundary(boundary)
	if err != nil {
		return CaptureResult{}, err
	}
	result := CaptureResult{Attempted: true, ExportID: boundary.ExportID, Scope: scope}
	if err := c.publisher.Publish(ctx, export); err != nil {
		return result, err
	}
	return result, nil
}

// CaptureSessionBoundary is the error-only form used by lifecycle wiring.
func (c *BoundaryCapture) CaptureSessionBoundary(ctx context.Context, req BoundaryCaptureRequest) error {
	_, err := c.Capture(ctx, req)
	return err
}

func (c *BoundaryCapture) currentTime() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}
	return c.now()
}
