package sessionmemory

import (
	"context"
	"errors"
)

// BoundaryStore is the narrow persistence port required by boundary
// synthesis. It deliberately contains only the exact-scope stage replay,
// snapshot, and commit operations.
type BoundaryStore interface {
	LookupOperation(ctx context.Context, lookup OperationLookup) (OperationLookupResult, error)
	LoadScope(ctx context.Context, scope Scope) (ScopeSnapshot, error)
	Commit(ctx context.Context, request CommitRequest) (OperationOutcome, error)
}

type boundaryConfig struct {
	Derivation            DerivationRef
	MaxCandidateCount     int
	MaxSourcesPerRevision int
	MaxDerivedTextBytes   int
	MaxSnapshotItems      int
}

// BoundaryProcessor owns scenario/profile synthesis after a validated
// boundary reaches typed ingest. It uses only the narrow BoundaryStore port
// and keeps stage idempotency and provenance grounding in the memory layer.
type BoundaryProcessor struct {
	store               BoundaryStore
	scenarioSynthesizer ScenarioSynthesizer
	profileSynthesizer  ProfileSynthesizer
	config              boundaryConfig
}

// NewBoundaryProcessor constructs the memory-owned boundary application
// service with package hard limits and the supplied derivation identity.
func NewBoundaryProcessor(store BoundaryStore, scenarios ScenarioSynthesizer, profiles ProfileSynthesizer, derivation DerivationRef) (*BoundaryProcessor, error) {
	if store == nil || scenarios == nil || profiles == nil {
		return nil, PermanentError(CodeStoreFailure, "boundary processor dependencies are required", nil)
	}
	if err := derivation.Validate(); err != nil {
		return nil, err
	}
	return &BoundaryProcessor{
		store:               store,
		scenarioSynthesizer: scenarios,
		profileSynthesizer:  profiles,
		config: boundaryConfig{
			Derivation:            derivation,
			MaxCandidateCount:     MaxCandidateCount,
			MaxSourcesPerRevision: MaxSourcesPerRevision,
			MaxDerivedTextBytes:   MaxDerivedTextBytes,
			MaxSnapshotItems:      MaxSnapshotItems,
		},
	}, nil
}

func checkBoundaryContext(ctx context.Context) error {
	if ctx == nil {
		return invalidDerived("context is required")
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return RetryableError(CodeTimeout, "derived memory operation timed out", nil)
		}
		return RetryableError(CodeTimeout, "derived memory operation was canceled", nil)
	}
	return nil
}

func boundaryStoreFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return checkBoundaryContext(ctx)
	}
	if code, class, ok := ClassifyError(err); ok {
		return newError(code, class, "derived memory store operation failed", nil)
	}
	return RetryableError(CodeStoreFailure, "derived memory store operation failed", nil)
}

func boundaryModelFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return checkBoundaryContext(ctx)
	}
	if code, class, ok := ClassifyError(err); ok {
		return newError(code, class, "derived memory model operation failed", nil)
	}
	return RetryableError(CodeModelFailure, "derived memory model operation failed", nil)
}
