package sessionmemoryapp

import (
	"context"

	"github.com/normahq/balda/sessionmemory"
)

// NativeProvider runs the portable Engine against Balda's durable Store and a
// dedicated structured deriver. It keeps the existing Provider boundary used
// by the JetStream worker while exposing derived retrieval to current callers.
type NativeProvider struct {
	engine  *sessionmemory.Engine
	invoker StructuredInvoker
}

// NewNativeProvider constructs the production in-process provider.
func NewNativeProvider(store sessionmemory.Store, deriver *Deriver, invoker StructuredInvoker) (*NativeProvider, error) {
	if store == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "native session-memory Store is required", nil)
	}
	if deriver == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeModelFailure, "native session-memory deriver is required", nil)
	}
	engine, err := sessionmemory.NewEngine(store, deriver, deriver, deriver, sessionmemory.Config{})
	if err != nil {
		return nil, err
	}
	return &NativeProvider{engine: engine, invoker: invoker}, nil
}

var _ sessionmemory.Provider = (*NativeProvider)(nil)

func (p *NativeProvider) SyncTurn(ctx context.Context, turn sessionmemory.Turn) error {
	if p == nil || p.engine == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	_, err := p.engine.ProcessTurn(ctx, turn)
	return err
}

func (p *NativeProvider) OnSessionBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	if p == nil || p.engine == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	_, err := p.engine.ProcessBoundary(ctx, boundary)
	return err
}

// Search adapts derived references to the legacy Provider response shape. The
// MCP surface will consume the richer Engine response in the scope migration.
func (p *NativeProvider) Search(ctx context.Context, request sessionmemory.SearchRequest) (sessionmemory.SearchResponse, error) {
	if p == nil || p.engine == nil {
		return sessionmemory.SearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	normalized, err := sessionmemory.NormalizeSearchRequest(request)
	if err != nil {
		return sessionmemory.SearchResponse{}, err
	}
	response, err := p.engine.Search(ctx, sessionmemory.DerivedSearchRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         normalized.Scope,
		Query:         normalized.Query,
		Limit:         normalized.Limit,
	})
	if err != nil {
		return sessionmemory.SearchResponse{}, err
	}
	results := make([]sessionmemory.SearchResult, 0, len(response.Results))
	for _, reference := range response.Results {
		sessionID := "native"
		if len(reference.Provenance.RawSources) > 0 {
			sessionID = reference.Provenance.RawSources[0].SessionID
		}
		results = append(results, sessionmemory.SearchResult{
			ID:        reference.RevisionID,
			ScopeKey:  reference.Scope.Key,
			SessionID: sessionID,
			Text:      reference.Text,
			CreatedAt: reference.CreatedAt,
			Score:     reference.Score,
		})
	}
	return sessionmemory.SearchResponse{SchemaVersion: sessionmemory.SchemaVersionV1, Scope: response.Scope, Results: results}, nil
}

func (p *NativeProvider) Close(ctx context.Context) error {
	if p == nil || p.invoker == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return p.invoker.Close(ctx)
}

// DisabledProvider is the stable no-op provider used when session memory is
// disabled. Keeping a concrete implementation in the graph lets Fx compose a
// disabled application without constructing an HTTP client or a consumer.
type DisabledProvider struct{}

func (DisabledProvider) SyncTurn(context.Context, sessionmemory.Turn) error {
	return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) OnSessionBoundary(context.Context, sessionmemory.Boundary) error {
	return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) Search(context.Context, sessionmemory.SearchRequest) (sessionmemory.SearchResponse, error) {
	return sessionmemory.SearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) Close(context.Context) error { return nil }

var _ sessionmemory.Provider = DisabledProvider{}
