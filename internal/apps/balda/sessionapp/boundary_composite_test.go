package sessionapp

import (
	"context"
	"errors"
	"testing"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

func TestBoundaryObserverCompositeRunsEveryObserverAndJoinsErrors(t *testing.T) {
	t.Parallel()
	firstErr := errors.New("first observer failed")
	first := &boundaryObserverStub{err: firstErr}
	second := &boundaryObserverStub{}
	observer := NewBoundaryObserverComposite([]baldasession.BoundaryObserver{first, nil, second})
	boundary := baldasession.SessionBoundary{Reason: baldasession.BoundaryReasonClose}
	err := observer.BeforeSessionBoundary(context.Background(), boundary)
	if !errors.Is(err, firstErr) {
		t.Fatalf("error = %v, want joined first error", err)
	}
	if len(first.boundaries) != 1 || len(second.boundaries) != 1 {
		t.Fatalf("observer calls = (%d, %d), want (1, 1)", len(first.boundaries), len(second.boundaries))
	}
}

type boundaryObserverStub struct {
	boundaries []baldasession.SessionBoundary
	err        error
}

func (o *boundaryObserverStub) BeforeSessionBoundary(_ context.Context, boundary baldasession.SessionBoundary) error {
	o.boundaries = append(o.boundaries, boundary)
	return o.err
}
