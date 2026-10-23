package sessionapp

import (
	"context"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type TurnStatusBoundaryObserver struct {
	Store state.TurnStatusStore
}

func (o TurnStatusBoundaryObserver) BeforeSessionBoundary(ctx context.Context, boundary baldasession.SessionBoundary) error {
	if o.Store == nil || boundary.Reason == baldasession.BoundaryReasonShutdown {
		return nil
	}
	return o.Store.ClearSession(ctx, boundary.SessionID)
}

var _ baldasession.BoundaryObserver = TurnStatusBoundaryObserver{}
