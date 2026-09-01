package sessionapp

import (
	"context"
	"errors"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

type BoundaryObserverComposite struct {
	observers []baldasession.BoundaryObserver
}

func NewBoundaryObserverComposite(observers []baldasession.BoundaryObserver) baldasession.BoundaryObserver {
	filtered := make([]baldasession.BoundaryObserver, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &BoundaryObserverComposite{observers: filtered}
}

func (c *BoundaryObserverComposite) BeforeSessionBoundary(ctx context.Context, boundary baldasession.SessionBoundary) error {
	if c == nil {
		return nil
	}
	var joined error
	for _, observer := range c.observers {
		if err := observer.BeforeSessionBoundary(ctx, boundary); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}
