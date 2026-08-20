package handlers

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/session"
)

type boundaryAwareSessionResetter interface {
	ResetSessionWithReason(ctx context.Context, locator session.SessionLocator, reason session.BoundaryReason) error
}

type sessionResetter interface {
	ResetSession(ctx context.Context, locator session.SessionLocator) error
}

// resetSessionWithReason keeps handler test doubles compatible while allowing
// the concrete session manager to classify explicit close transitions.
func resetSessionWithReason(ctx context.Context, manager sessionResetter, locator session.SessionLocator, reason session.BoundaryReason) error {
	if aware, ok := manager.(boundaryAwareSessionResetter); ok {
		return aware.ResetSessionWithReason(ctx, locator, reason)
	}
	return manager.ResetSession(ctx, locator)
}
