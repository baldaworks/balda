package app

import (
	"context"
	"sync"

	"github.com/baldaworks/balda/sessionmemory"
)

// Runtime is the concrete portable session-memory application.  Consumers
// should depend on one or more of its capability interfaces, not on a type
// assertion for optional methods.
type Runtime struct {
	turn     *TurnProcessor
	boundary *BoundaryService
	recall   RecallReader
	trace    TraceReader
	forget   Forgetter

	mu        sync.Mutex
	lifecycle []Lifecycle
	started   int
	starting  bool
	closed    bool
}

// NewRuntime composes the supported application capabilities.  Missing
// optional capabilities leave their methods disabled; this is useful for a
// host that grants only ingestion or only read access.  Turn ingestion is
// constructed when CanonicalStore and Extractor (or StructuredInvoker) are
// present.  Recall and projection adapters are constructed from their narrow
// ports without importing a storage implementation.
func NewRuntime(config RuntimeConfig) (*Runtime, error) {
	derivation := config.Derivation
	if derivation == (sessionmemory.DerivationRef{}) {
		derivation = sessionmemory.LegacyDerivationRef()
	}
	if err := derivation.Validate(); err != nil {
		return nil, err
	}

	project := config.Project
	var projectionRuntime *ProjectionRuntime
	if project == nil && config.CanonicalStore != nil && config.ProjectionCheckpoints != nil && config.ProjectionApplier != nil && config.ProjectionID != "" {
		var err error
		projectionRuntime, err = NewProjectionRuntime(config.CanonicalStore, config.ProjectionCheckpoints, config.ProjectionApplier, config.ProjectionID, config.ProjectionBatchSize)
		if err != nil {
			return nil, err
		}
		project = projectionRuntime
	}

	extractor := config.Extractor
	var invokerLifecycle *structuredInvokerLifecycle
	if config.StructuredInvoker != nil {
		invokerLifecycle = &structuredInvokerLifecycle{invoker: config.StructuredInvoker}
	}
	if extractor == nil && config.StructuredInvoker != nil {
		deriver, err := NewDeriver(config.StructuredInvoker)
		if err != nil {
			return nil, err
		}
		extractor = deriver
	}

	var turn *TurnProcessor
	if config.CanonicalStore != nil && extractor != nil {
		var err error
		turn, err = NewTurnProcessor(config.CanonicalStore, extractor, config.Policy, derivation, project)
		if err != nil {
			return nil, err
		}
	}

	recall := config.Recall
	if recall == nil && config.RecallCanonical != nil {
		service, err := NewRecallService(config.RecallCanonical, config.RecallProjection)
		if err != nil {
			return nil, err
		}
		recall = service
	}

	trace := config.Trace
	if trace == nil && config.CanonicalDerived != nil {
		service, err := NewTraceService(config.CanonicalDerived)
		if err != nil {
			return nil, err
		}
		trace = service
	}

	var boundary *BoundaryService
	if config.Boundary != nil {
		var err error
		boundary, err = NewBoundaryService(config.Boundary, project)
		if err != nil {
			return nil, err
		}
	}

	forget := config.Forget
	if forget != nil {
		service, err := NewForgetServiceWithProjection(forget, project)
		if err != nil {
			return nil, err
		}
		forget = service
	}

	lifecycle := make([]Lifecycle, 0, len(config.Lifecycle)+2)
	// Projection is the first application-side resource: canonical commits
	// must be available before any generation can be advertised.
	if projectionRuntime != nil {
		lifecycle = append(lifecycle, projectionRuntime)
	}
	if invokerLifecycle != nil {
		lifecycle = append(lifecycle, invokerLifecycle)
	}
	if projectionRuntime == nil {
		if resource, ok := project.(Lifecycle); ok {
			lifecycle = append(lifecycle, resource)
		}
	}
	for _, resource := range config.Lifecycle {
		if resource == nil {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "runtime lifecycle dependency is nil", nil)
		}
		lifecycle = append(lifecycle, resource)
	}
	return &Runtime{turn: turn, boundary: boundary, recall: recall, trace: trace, forget: forget, lifecycle: lifecycle}, nil
}

// IngestTurn implements TurnIngestor.
func (r *Runtime) IngestTurn(ctx context.Context, turn sessionmemory.Turn) error {
	if r == nil || r.turn == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory turn ingestion is unavailable", nil)
	}
	return r.turn.IngestTurn(ctx, turn)
}

// ApplyBoundary implements BoundaryIngestor.
func (r *Runtime) ApplyBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	if r == nil || r.boundary == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory boundary ingestion is unavailable", nil)
	}
	return r.boundary.ApplyBoundary(ctx, boundary)
}

// Search implements RecallReader.
func (r *Runtime) Search(ctx context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
	if r == nil || r.recall == nil {
		return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory recall is unavailable", nil)
	}
	return r.recall.Search(ctx, request)
}

// Trace implements TraceReader.
func (r *Runtime) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	if r == nil || r.trace == nil {
		return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory trace is unavailable", nil)
	}
	return r.trace.Trace(ctx, request)
}

// ForgetSource implements Forgetter.
func (r *Runtime) ForgetSource(ctx context.Context, command sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error) {
	if r == nil || r.forget == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory forget is unavailable", nil)
	}
	return r.forget.ForgetSource(ctx, command)
}

// ForgetScope implements Forgetter.
func (r *Runtime) ForgetScope(ctx context.Context, command sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error) {
	if r == nil || r.forget == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory forget is unavailable", nil)
	}
	return r.forget.ForgetScope(ctx, command)
}

// Start starts resources in canonical-to-application order.  A failure closes
// every resource that was started in the current attempt.
func (r *Runtime) Start(ctx context.Context) error {
	if r == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory runtime is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "session-memory runtime start context is required", nil)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return sessionmemory.PermanentError(sessionmemory.CodeShuttingDown, "session-memory runtime is closed", nil)
	}
	if r.started == len(r.lifecycle) {
		r.mu.Unlock()
		return nil
	}
	if r.starting {
		r.mu.Unlock()
		return sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "session-memory runtime is starting", nil)
	}
	r.starting = true
	startAt := r.started
	resources := append([]Lifecycle(nil), r.lifecycle...)
	r.mu.Unlock()

	for index := startAt; index < len(resources); index++ {
		if err := resources[index].Start(ctx); err != nil {
			for closeIndex := index; closeIndex >= startAt; closeIndex-- {
				_ = resources[closeIndex].Close(context.Background())
			}
			r.mu.Lock()
			r.starting = false
			r.started = startAt
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		r.started = index + 1
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.starting = false
	r.mu.Unlock()
	return nil
}

// Close stops resources in reverse order and is idempotent.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	resources := append([]Lifecycle(nil), r.lifecycle...)
	started := r.started
	r.mu.Unlock()
	var firstErr error
	if started > len(resources) {
		started = len(resources)
	}
	for index := started - 1; index >= 0; index-- {
		if err := resources[index].Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type structuredInvokerLifecycle struct {
	invoker StructuredInvoker
}

func (*structuredInvokerLifecycle) Start(context.Context) error { return nil }

func (l *structuredInvokerLifecycle) Close(ctx context.Context) error {
	if l == nil || l.invoker == nil {
		return nil
	}
	return l.invoker.Close(ctx)
}

var _ TurnIngestor = (*Runtime)(nil)
var _ BoundaryIngestor = (*Runtime)(nil)
var _ RecallReader = (*Runtime)(nil)
var _ TraceReader = (*Runtime)(nil)
var _ Forgetter = (*Runtime)(nil)
var _ Lifecycle = (*Runtime)(nil)
