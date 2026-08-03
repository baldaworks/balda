package sessionmemory

import (
	"context"
	"errors"
)

// Engine orchestrates portable derived-memory processing through Store and
// model ports. It owns no goroutines and keeps no mutable processing state.
type Engine struct {
	store               Store
	atomExtractor       AtomExtractor
	scenarioSynthesizer ScenarioSynthesizer
	profileSynthesizer  ProfileSynthesizer
	config              Config
}

// NewEngine constructs a stateless derived-memory engine.
func NewEngine(
	store Store,
	atomExtractor AtomExtractor,
	scenarioSynthesizer ScenarioSynthesizer,
	profileSynthesizer ProfileSynthesizer,
	config Config,
) (*Engine, error) {
	if store == nil {
		return nil, invalidDerived("derived memory Store is required")
	}
	if atomExtractor == nil {
		return nil, invalidDerived("atom extractor is required")
	}
	if scenarioSynthesizer == nil {
		return nil, invalidDerived("scenario synthesizer is required")
	}
	if profileSynthesizer == nil {
		return nil, invalidDerived("profile synthesizer is required")
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	return &Engine{
		store:               store,
		atomExtractor:       atomExtractor,
		scenarioSynthesizer: scenarioSynthesizer,
		profileSynthesizer:  profileSynthesizer,
		config:              normalized,
	}, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return invalidDerived("context is required")
	}
	if err := ctx.Err(); err != nil {
		return contextFailure(err)
	}
	return nil
}

func contextFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return RetryableError(CodeTimeout, "derived memory operation timed out", nil)
	}
	return RetryableError(CodeTimeout, "derived memory operation was canceled", nil)
}

func storePortFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return contextFailure(ctx.Err())
	}
	if code, class, ok := ClassifyError(err); ok {
		return newError(code, class, "derived memory Store operation failed", nil)
	}
	return RetryableError(CodeStoreFailure, "derived memory Store operation failed", nil)
}

func modelPortFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return contextFailure(ctx.Err())
	}
	if code, class, ok := ClassifyError(err); ok {
		return newError(code, class, "derived memory model operation failed", nil)
	}
	return RetryableError(CodeModelFailure, "derived memory model operation failed", nil)
}
