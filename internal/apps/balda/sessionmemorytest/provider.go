package sessionmemorytest

import (
	"context"
	"sync"

	"github.com/baldaworks/balda/sessionmemory"
)

// Provider is a concurrency-safe, callback-configurable in-process ingest
// capability fake for worker tests. Runtime lifecycle is intentionally not
// part of this delivery test double.
type Provider struct {
	IngestTurnFunc    func(context.Context, sessionmemory.Turn) error
	ApplyBoundaryFunc func(context.Context, sessionmemory.Boundary) error

	mu         sync.Mutex
	turns      []sessionmemory.Turn
	boundaries []sessionmemory.Boundary
}

// IngestTurn records turn and invokes IngestTurnFunc when configured.
func (p *Provider) IngestTurn(ctx context.Context, turn sessionmemory.Turn) error {
	p.mu.Lock()
	p.turns = append(p.turns, cloneTurn(turn))
	fn := p.IngestTurnFunc
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, turn)
}

// ApplyBoundary records boundary and invokes ApplyBoundaryFunc when configured.
func (p *Provider) ApplyBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	p.mu.Lock()
	p.boundaries = append(p.boundaries, boundary)
	fn := p.ApplyBoundaryFunc
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, boundary)
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

func cloneTurn(turn sessionmemory.Turn) sessionmemory.Turn {
	turn.Messages = append([]sessionmemory.Message(nil), turn.Messages...)
	return turn
}
