package app

import (
	"context"

	"github.com/baldaworks/balda/sessionmemory"
)

// TurnProcessor owns canonical semantic processing after a host has supplied
// a validated terminal turn.  The canonical store remains the source of truth;
// projection work is a post-commit side effect.
type TurnProcessor struct {
	processor  *sessionmemory.CanonicalTurnProcessor
	projection ProjectionSyncer
	derivation sessionmemory.DerivationRef
}

// NewTurnProcessor constructs the portable turn application service.
func NewTurnProcessor(store sessionmemory.CanonicalStore, extractor sessionmemory.CanonicalSemanticExtractor, policy sessionmemory.PolicyRegistry, derivation sessionmemory.DerivationRef, projection ProjectionSyncer) (*TurnProcessor, error) {
	if store == nil || extractor == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical turn dependencies are required", nil)
	}
	if policy.Version == "" {
		policy.Version = "policy-v1"
	}
	if derivation == (sessionmemory.DerivationRef{}) {
		derivation = sessionmemory.LegacyDerivationRef()
	}
	if err := derivation.Validate(); err != nil {
		return nil, err
	}
	processor, err := sessionmemory.NewCanonicalTurnProcessor(store, extractor, policy)
	if err != nil {
		return nil, err
	}
	return &TurnProcessor{processor: processor, projection: projection, derivation: derivation}, nil
}

// ProcessTurn returns the canonical outcome for callers that need the
// committed watermark.  IngestTurn below intentionally exposes only the
// capability required by a delivery worker.
func (p *TurnProcessor) ProcessTurn(ctx context.Context, turn sessionmemory.Turn) (sessionmemory.CanonicalMutationOutcome, error) {
	if p == nil || p.processor == nil {
		return sessionmemory.CanonicalMutationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical turn processor is unavailable", nil)
	}
	outcome, err := p.processor.ProcessTurn(ctx, turn, p.derivation)
	if err != nil {
		return sessionmemory.CanonicalMutationOutcome{}, err
	}
	if p.projection != nil {
		if err := p.projection.Sync(ctx, turn.Scope); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// IngestTurn implements TurnIngestor.
func (p *TurnProcessor) IngestTurn(ctx context.Context, turn sessionmemory.Turn) error {
	_, err := p.ProcessTurn(ctx, turn)
	return err
}

var _ TurnIngestor = (*TurnProcessor)(nil)

// BoundaryService coordinates a host-provided boundary semantic processor
// with the rebuildable projection.  The semantic processor itself is a narrow
// port so a future service adapter can replace it without changing Runtime.
type BoundaryService struct {
	processor  BoundaryProcessor
	projection ProjectionSyncer
}

// NewBoundaryService constructs the boundary application service.
func NewBoundaryService(processor BoundaryProcessor, projection ProjectionSyncer) (*BoundaryService, error) {
	if processor == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "boundary processor is required", nil)
	}
	return &BoundaryService{processor: processor, projection: projection}, nil
}

// ApplyBoundary implements BoundaryIngestor.
func (s *BoundaryService) ApplyBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	if s == nil || s.processor == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "boundary processor is unavailable", nil)
	}
	if err := boundary.Validate(); err != nil {
		return err
	}
	if _, err := s.processor.ProcessBoundary(ctx, boundary); err != nil {
		return err
	}
	if s.projection != nil {
		return s.projection.Sync(ctx, boundary.Scope)
	}
	return nil
}

var _ BoundaryIngestor = (*BoundaryService)(nil)
