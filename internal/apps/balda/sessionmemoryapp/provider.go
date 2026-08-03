package sessionmemoryapp

import (
	"context"

	"github.com/normahq/balda/sessionmemory"
)

// NativeProvider runs the portable Engine against Balda's durable Store and a
// dedicated structured deriver. JetStream carries completed exports and
// boundaries into this native provider; retrieval and forgetting stay on the
// native application ports below.
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

// SearchDerived returns the validated, structured native search response. The
// caller is responsible for deriving the exact scope from authenticated
// Balda context; this method never accepts a locator fallback.
func (p *NativeProvider) SearchDerived(ctx context.Context, request sessionmemory.DerivedSearchRequest) (sessionmemory.DerivedSearchResponse, error) {
	if p == nil || p.engine == nil {
		return sessionmemory.DerivedSearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	return p.engine.Search(ctx, request)
}

// Trace returns the validated native provenance graph for one exact scope.
func (p *NativeProvider) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	if p == nil || p.engine == nil {
		return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	return p.engine.Trace(ctx, request)
}

// ForgetSource atomically tombstones one native raw source and invalidates its
// complete same-scope derived dependency closure. It is an application-level
// operation; no user-facing MCP tool invokes it implicitly.
func (p *NativeProvider) ForgetSource(ctx context.Context, command sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error) {
	if p == nil || p.engine == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	return p.engine.ForgetSource(ctx, command)
}

// ForgetScope atomically tombstones all readable native memory in one exact
// scope. Global fact memory is owned by a separate Store and is untouched.
func (p *NativeProvider) ForgetScope(ctx context.Context, command sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error) {
	if p == nil || p.engine == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "native session-memory provider is unavailable", nil)
	}
	return p.engine.ForgetScope(ctx, command)
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

func (DisabledProvider) SearchDerived(context.Context, sessionmemory.DerivedSearchRequest) (sessionmemory.DerivedSearchResponse, error) {
	return sessionmemory.DerivedSearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) Trace(context.Context, sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) ForgetSource(context.Context, sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error) {
	return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) ForgetScope(context.Context, sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error) {
	return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) Close(context.Context) error { return nil }

var _ sessionmemory.Provider = DisabledProvider{}
