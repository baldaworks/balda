package sessionmemorytest

import (
	"context"
	"sync"

	"github.com/normahq/balda/sessionmemory"
)

// Provider is a concurrency-safe, callback-configurable in-process fake.
type Provider struct {
	SyncTurnFunc func(context.Context, sessionmemory.Turn) error
	BoundaryFunc func(context.Context, sessionmemory.Boundary) error
	CloseFunc    func(context.Context) error

	mu         sync.Mutex
	turns      []sessionmemory.Turn
	boundaries []sessionmemory.Boundary
	closeCalls int
}

// SyncTurn records turn and invokes SyncTurnFunc when configured.
func (p *Provider) SyncTurn(ctx context.Context, turn sessionmemory.Turn) error {
	p.mu.Lock()
	p.turns = append(p.turns, cloneTurn(turn))
	fn := p.SyncTurnFunc
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, turn)
}

// OnSessionBoundary records boundary and invokes BoundaryFunc when configured.
func (p *Provider) OnSessionBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	p.mu.Lock()
	p.boundaries = append(p.boundaries, boundary)
	fn := p.BoundaryFunc
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, boundary)
}

// Close records the call and invokes CloseFunc when configured.
func (p *Provider) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closeCalls++
	fn := p.CloseFunc
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// Turns returns a snapshot of recorded completed turns.
func (p *Provider) Turns() []sessionmemory.Turn {
	p.mu.Lock()
	defer p.mu.Unlock()
	turns := make([]sessionmemory.Turn, len(p.turns))
	for index := range p.turns {
		turns[index] = cloneTurn(p.turns[index])
	}
	return turns
}

// Boundaries returns a snapshot of recorded lifecycle boundaries.
func (p *Provider) Boundaries() []sessionmemory.Boundary {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sessionmemory.Boundary(nil), p.boundaries...)
}

// CloseCalls returns the number of recorded Close calls.
func (p *Provider) CloseCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeCalls
}

func cloneTurn(turn sessionmemory.Turn) sessionmemory.Turn {
	turn.Messages = append([]sessionmemory.Message(nil), turn.Messages...)
	return turn
}
