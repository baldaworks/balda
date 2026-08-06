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

// CanonicalProvider is the composition-root adapter for the v2 turn,
// boundary, recall, trace, forget, and lifecycle ports. It has no legacy
// delegate: the legacy Store is migration input only.
type CanonicalProvider struct {
	processor *sessionmemory.CanonicalTurnProcessor
	boundary  interface {
		ProcessBoundary(ctx context.Context, boundary sessionmemory.Boundary) (sessionmemory.BoundaryOutcome, error)
	}
	derived    sessionmemory.CanonicalDerivedReader
	recall     RecallSearcher
	forget     CanonicalForgetOperations
	before     func(context.Context, sessionmemory.Scope) error
	project    func(context.Context, sessionmemory.Scope) error
	start      func(context.Context) error
	close      func(context.Context) error
	derivation sessionmemory.DerivationRef
}

// RecallSearcher is the additive canonical recall port used by MCP.
type RecallSearcher interface {
	Search(ctx context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error)
}

// CanonicalForgetOperations owns canonical logical-denial outcomes.
type CanonicalForgetOperations interface {
	ForgetSource(ctx context.Context, command sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error)
	ForgetScope(ctx context.Context, command sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error)
}

// CanonicalProviderConfig is the complete enabled canonical runtime graph.
type CanonicalProviderConfig struct {
	Processor *sessionmemory.CanonicalTurnProcessor
	Boundary  interface {
		ProcessBoundary(ctx context.Context, boundary sessionmemory.Boundary) (sessionmemory.BoundaryOutcome, error)
	}
	Derived    sessionmemory.CanonicalDerivedReader
	Recall     RecallSearcher
	Forget     CanonicalForgetOperations
	Before     func(context.Context, sessionmemory.Scope) error
	Project    func(context.Context, sessionmemory.Scope) error
	Start      func(context.Context) error
	Close      func(context.Context) error
	Derivation sessionmemory.DerivationRef
}

func NewCanonicalProviderWithRuntime(config CanonicalProviderConfig) (*CanonicalProvider, error) {
	if config.Processor == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical turn processor is required", nil)
	}
	if config.Boundary == nil || config.Derived == nil || config.Recall == nil || config.Forget == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical provider runtime ports are required", nil)
	}
	if err := config.Derivation.Validate(); err != nil {
		return nil, err
	}
	return &CanonicalProvider{processor: config.Processor, boundary: config.Boundary, derived: config.Derived, recall: config.Recall, forget: config.Forget, before: config.Before, project: config.Project, start: config.Start, close: config.Close, derivation: config.Derivation}, nil
}

var _ sessionmemory.Provider = (*CanonicalProvider)(nil)

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

func (p *CanonicalProvider) SyncTurn(ctx context.Context, turn sessionmemory.Turn) error {
	if p == nil || p.processor == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical session-memory provider is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, turn.Scope); err != nil {
			return err
		}
	}
	if _, err := p.processor.ProcessTurn(ctx, turn, p.derivation); err != nil {
		return err
	}
	if p.project != nil {
		return p.project(ctx, turn.Scope)
	}
	return nil
}

func (p *CanonicalProvider) OnSessionBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	if p == nil || p.boundary == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical session-memory provider is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, boundary.Scope); err != nil {
			return err
		}
	}
	if _, err := p.boundary.ProcessBoundary(ctx, boundary); err != nil {
		return err
	}
	if p.project != nil {
		return p.project(ctx, boundary.Scope)
	}
	return nil
}

func (p *CanonicalProvider) SearchDerived(ctx context.Context, request sessionmemory.DerivedSearchRequest) (sessionmemory.DerivedSearchResponse, error) {
	if p == nil || p.derived == nil {
		return sessionmemory.DerivedSearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical session-memory provider is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, request.Scope); err != nil {
			return sessionmemory.DerivedSearchResponse{}, err
		}
	}
	return p.derived.SearchDerived(ctx, request)
}

func (p *CanonicalProvider) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	if p == nil || p.derived == nil {
		return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical session-memory provider is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, request.Scope); err != nil {
			return sessionmemory.TraceResponse{}, err
		}
	}
	return p.derived.Trace(ctx, request)
}

func (p *CanonicalProvider) ForgetSource(ctx context.Context, command sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error) {
	if p == nil || p.forget == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical session-memory provider is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, command.Source.Scope); err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
	}
	outcome, err := p.forget.ForgetSource(ctx, command)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if p.project != nil {
		if projectErr := p.project(ctx, command.Source.Scope); projectErr != nil {
			return outcome, projectErr
		}
	}
	return outcome, nil
}

func (p *CanonicalProvider) ForgetScope(ctx context.Context, command sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error) {
	if p == nil || p.forget == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical session-memory provider is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, command.Scope); err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
	}
	outcome, err := p.forget.ForgetScope(ctx, command)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if p.project != nil {
		if projectErr := p.project(ctx, command.Scope); projectErr != nil {
			return outcome, projectErr
		}
	}
	return outcome, nil
}

// Search implements the additive canonical recall port used by MCP.
func (p *CanonicalProvider) Search(ctx context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
	if p == nil || p.recall == nil {
		return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical recall is unavailable", nil)
	}
	if p.before != nil {
		if err := p.before(ctx, request.Scope); err != nil {
			return sessionmemory.RecallResponse{}, err
		}
	}
	return p.recall.Search(ctx, request)
}

func (p *CanonicalProvider) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if p.close != nil {
		return p.close(ctx)
	}
	return nil
}

// Start starts runtime-owned maintenance after bundled MCP services are
// available and before Balda provider/channel ingress begins.
func (p *CanonicalProvider) Start(ctx context.Context) error {
	if p == nil || p.start == nil {
		return nil
	}
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical session-memory start context is required", nil)
	}
	return p.start(ctx)
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

// DisabledProvider is the stable no-op processor used when session memory is
// disabled. Keeping a concrete implementation in the graph lets Fx compose a
// disabled application without constructing the native runtime or consumer.
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
