package sessionmemoryapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
	portableapp "github.com/normahq/balda/sessionmemory/app"
	"github.com/rs/zerolog"
)

const (
	defaultRetryAttempts   = 5
	defaultRetryBaseDelay  = 250 * time.Millisecond
	defaultRetryMaxDelay   = 5 * time.Second
	defaultProgressPeriod  = 30 * time.Second
	defaultFetchErrorDelay = 100 * time.Millisecond
	defaultShutdownTimeout = 30 * time.Second
	defaultMaxScopes       = 4
	defaultQueuedPerScope  = 32
)

// Config controls the serialized memory capability consumer.
type Config struct {
	Enabled             bool
	MaxAttempts         int
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	ProgressInterval    time.Duration
	FetchErrorDelay     time.Duration
	ShutdownTimeout     time.Duration
	MaxConcurrentScopes int
	MaxQueuedPerScope   int
}

// Normalized supplies safe defaults and rejects invalid retry/lifecycle
// settings before a worker can start.
func (c Config) Normalized() (Config, error) {
	out := c
	if out.MaxAttempts <= 0 {
		out.MaxAttempts = defaultRetryAttempts
	}
	if out.RetryBaseDelay <= 0 {
		out.RetryBaseDelay = defaultRetryBaseDelay
	}
	if out.RetryMaxDelay <= 0 {
		out.RetryMaxDelay = defaultRetryMaxDelay
	}
	if out.RetryMaxDelay < out.RetryBaseDelay {
		return Config{}, fmt.Errorf("retry max delay must be at least retry base delay")
	}
	if out.ProgressInterval <= 0 {
		out.ProgressInterval = defaultProgressPeriod
	}
	if out.FetchErrorDelay <= 0 {
		out.FetchErrorDelay = defaultFetchErrorDelay
	}
	if out.ShutdownTimeout <= 0 {
		out.ShutdownTimeout = defaultShutdownTimeout
	}
	if out.MaxConcurrentScopes <= 0 {
		out.MaxConcurrentScopes = defaultMaxScopes
	}
	if out.MaxQueuedPerScope <= 0 {
		out.MaxQueuedPerScope = defaultQueuedPerScope
	}
	if out.MaxConcurrentScopes > 128 || out.MaxQueuedPerScope > 1024 {
		return Config{}, fmt.Errorf("session-memory lane limits exceed safe bounds")
	}
	return out, nil
}

// ShutdownReport records the last bounded stop attempt.
type ShutdownReport struct {
	Stats     BacklogStats
	StatsErr  error
	StopError error
	StoppedAt time.Time
}

// Worker serializes all memory capability calls behind one durable queue delivery.
type Worker struct {
	transport Transport
	turn      portableapp.TurnIngestor
	boundary  portableapp.BoundaryIngestor
	config    Config
	logger    zerolog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	report  ShutdownReport

	lanes       map[string]*memoryLane
	laneChanged chan struct{}
	laneWG      sync.WaitGroup
	semaphore   chan struct{}
}

type laneWork struct {
	delivery Delivery
	export   sessionmemorycmd.Export
}

type memoryLane struct{ pending []laneWork }

// NewCapabilityWorker constructs the production worker over narrow portable
// ingest capabilities. Runtime lifecycle is owned by the Balda composition
// root and is deliberately not closed by this delivery worker.
func NewCapabilityWorker(transport Transport, turn portableapp.TurnIngestor, boundary portableapp.BoundaryIngestor, config Config, logger zerolog.Logger) (*Worker, error) {
	if transport == nil {
		return nil, fmt.Errorf("session-memory transport is required")
	}
	normalized, err := config.Normalized()
	if err != nil {
		return nil, err
	}
	if normalized.Enabled && (turn == nil || boundary == nil) {
		return nil, fmt.Errorf("session-memory ingest capabilities are required when enabled")
	}
	return &Worker{
		transport:   transport,
		turn:        turn,
		boundary:    boundary,
		config:      normalized,
		logger:      logger.With().Str("component", "balda.session_memory_worker").Logger(),
		lanes:       make(map[string]*memoryLane),
		laneChanged: make(chan struct{}),
		semaphore:   make(chan struct{}, normalized.MaxConcurrentScopes),
	}, nil
}

// Start launches the worker away from the user-response goroutine. Disabled
// workers deliberately create no fetch loop and require no capability.
func (w *Worker) Start(parent context.Context) error {
	if w == nil {
		return fmt.Errorf("session-memory worker is required")
	}
	if parent == nil {
		parent = context.Background()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return ErrWorkerStarted
	}
	w.started = true
	if !w.config.Enabled {
		w.done = closedDone()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.done = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		w.run(ctx)
	}(w.done)
	return nil
}

// Stop cancels new fetches, waits for the current message to settle or the
// supplied bounded deadline, and records backlog state. Runtime capability
// lifecycle remains owned by the composition root.
func (w *Worker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stopCtx := ctx
	if _, ok := ctx.Deadline(); !ok && w.config.ShutdownTimeout > 0 {
		var cancel context.CancelFunc
		stopCtx, cancel = context.WithTimeout(ctx, w.config.ShutdownTimeout)
		defer cancel()
	}

	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	var stopErr error
	select {
	case <-done:
	case <-stopCtx.Done():
		stopErr = stopCtx.Err()
	}

	report := ShutdownReport{StoppedAt: time.Now().UTC()}
	if stopErr != nil {
		// Keep the lifecycle state until the worker actually exits. A later Stop
		// call can finish the drain without racing an in-flight capability
		// operation.
		report.StopError = stopErr
		w.mu.Lock()
		w.report = report
		w.mu.Unlock()
		return stopErr
	}
	if w.config.Enabled {
		report.Stats, report.StatsErr = w.transport.Stats(stopCtx)
	}

	w.mu.Lock()
	w.started = false
	w.cancel = nil
	w.done = nil
	w.lanes = make(map[string]*memoryLane)
	w.laneChanged = make(chan struct{})
	w.semaphore = make(chan struct{}, w.config.MaxConcurrentScopes)
	w.report = report
	w.mu.Unlock()

	return errors.Join(stopErr, report.StatsErr, report.StopError)
}

// ShutdownReport returns a snapshot of the last stop outcome.
func (w *Worker) ShutdownReport() ShutdownReport {
	if w == nil {
		return ShutdownReport{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.report
}

func closedDone() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (w *Worker) run(ctx context.Context) {
	defer w.laneWG.Wait()
	for {
		delivery, err := w.transport.Fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrNoMessages) {
				continue
			}
			w.logger.Warn().Str("operation", "fetch").Msg("session-memory fetch failed; retrying")
			if !waitContext(ctx, w.config.FetchErrorDelay) {
				return
			}
			continue
		}
		if delivery == nil {
			w.logger.Warn().Str("operation", "fetch").Msg("session-memory transport returned an empty delivery")
			continue
		}
		export, err := delivery.Export()
		if err == nil {
			err = export.Validate()
		}
		if err != nil {
			if err := w.deadLetter(ctx, delivery, sessionmemorycmd.Export{}, sessionmemory.CodePermanent, sessionmemory.ErrorClassPermanent, "invalid session-memory export"); err != nil && ctx.Err() == nil {
				w.logger.Warn().Err(err).Str("operation", "dead_letter").Msg("invalid session-memory delivery remains unresolved")
			}
			continue
		}
		scopeKey := exportScopeKey(export)
		if scopeKey == "" {
			if err := w.deadLetter(ctx, delivery, export, sessionmemory.CodePermanent, sessionmemory.ErrorClassPermanent, "session-memory export has no exact scope"); err != nil && ctx.Err() == nil {
				w.logger.Warn().Err(err).Str("operation", "dead_letter").Msg("scope-less session-memory delivery remains unresolved")
			}
			continue
		}
		if err := w.enqueue(ctx, scopeKey, laneWork{delivery: delivery, export: export}); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Warn().Err(err).Str("operation", "enqueue").Msg("session-memory lane enqueue failed")
		}
	}
}

func exportScopeKey(export sessionmemorycmd.Export) string {
	switch export.Kind {
	case sessionmemorycmd.KindTurn:
		if export.Turn != nil {
			return export.Turn.Scope.Key
		}
	case sessionmemorycmd.KindBoundary:
		if export.Boundary != nil {
			return export.Boundary.Scope.Key
		}
	}
	return ""
}

func (w *Worker) enqueue(ctx context.Context, scopeKey string, work laneWork) error {
	for {
		w.mu.Lock()
		lane := w.lanes[scopeKey]
		if lane == nil {
			lane = &memoryLane{}
			w.lanes[scopeKey] = lane
		}
		if len(lane.pending) < w.config.MaxQueuedPerScope {
			lane.pending = append(lane.pending, work)
			if len(lane.pending) == 1 {
				w.laneWG.Add(1)
				go w.runLane(ctx, scopeKey, lane)
			}
			w.signalLaneChangeLocked()
			w.mu.Unlock()
			return nil
		}
		changed := w.laneChanged
		w.mu.Unlock()
		if err := work.delivery.InProgress(ctx); err != nil && ctx.Err() == nil {
			w.logger.Warn().Err(err).Str("operation", "queue_progress_ack").Msg("session-memory queue progress acknowledgement failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (w *Worker) runLane(ctx context.Context, scopeKey string, lane *memoryLane) {
	defer w.laneWG.Done()
	for {
		w.mu.Lock()
		if len(lane.pending) == 0 {
			if w.lanes[scopeKey] == lane {
				delete(w.lanes, scopeKey)
			}
			w.signalLaneChangeLocked()
			w.mu.Unlock()
			return
		}
		work := lane.pending[0]
		lane.pending[0] = laneWork{}
		lane.pending = lane.pending[1:]
		w.signalLaneChangeLocked()
		w.mu.Unlock()

		select {
		case w.semaphore <- struct{}{}:
			err := w.processExport(ctx, work.delivery, work.export)
			<-w.semaphore
			if err != nil && ctx.Err() == nil {
				w.logger.Warn().Err(err).Str("operation", "process").Str("scope", scopeKey).Msg("session-memory delivery remains unresolved")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) signalLaneChangeLocked() {
	close(w.laneChanged)
	w.laneChanged = make(chan struct{})
}

func (w *Worker) processExport(ctx context.Context, delivery Delivery, export sessionmemorycmd.Export) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = w.syncWithProgress(ctx, delivery, export)
		if err == nil {
			if ackErr := delivery.Ack(ctx); ackErr != nil {
				return fmt.Errorf("ack session-memory delivery: %w", ackErr)
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code, class := classifyProviderFailure(err)
		if class == sessionmemory.ErrorClassPermanent {
			return w.deadLetter(ctx, delivery, export, code, class, "memory capability permanently rejected session-memory export")
		}
		if attempt >= w.config.MaxAttempts {
			return w.deadLetter(ctx, delivery, export, code, class, "session-memory capability retry limit exhausted")
		}
		if !w.waitRetry(ctx, delivery, attempt) {
			return ctx.Err()
		}
	}
}

func (w *Worker) syncWithProgress(ctx context.Context, delivery Delivery, export sessionmemorycmd.Export) error {
	result := make(chan error, 1)
	go func() {
		result <- w.sync(ctx, export)
	}()

	ticker := time.NewTicker(w.config.ProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := delivery.InProgress(ctx); err != nil && ctx.Err() == nil {
				w.logger.Warn().Str("operation", "progress_ack").Msg("session-memory progress acknowledgement failed")
			}
		}
	}
}

func (w *Worker) sync(ctx context.Context, export sessionmemorycmd.Export) error {
	switch export.Kind {
	case sessionmemorycmd.KindTurn:
		if w.turn != nil {
			return w.turn.IngestTurn(ctx, *export.Turn)
		}
	case sessionmemorycmd.KindBoundary:
		if w.boundary != nil {
			return w.boundary.ApplyBoundary(ctx, *export.Boundary)
		}
	default:
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "unsupported session-memory export", nil)
	}
	return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory ingest is unavailable", nil)
}

func (w *Worker) waitRetry(ctx context.Context, delivery Delivery, attempt int) bool {
	delay := w.retryDelay(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	ticker := time.NewTicker(w.config.ProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if err := delivery.InProgress(ctx); err != nil && ctx.Err() == nil {
				w.logger.Warn().Str("operation", "progress_ack").Msg("session-memory progress acknowledgement failed")
			}
		}
	}
}

func (w *Worker) retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return w.config.RetryBaseDelay
	}
	shift := attempt - 1
	if shift >= 62 {
		return w.config.RetryMaxDelay
	}
	delay := float64(w.config.RetryBaseDelay) * math.Pow(2, float64(shift))
	if delay >= float64(w.config.RetryMaxDelay) {
		return w.config.RetryMaxDelay
	}
	return time.Duration(delay)
}

func (w *Worker) deadLetter(ctx context.Context, delivery Delivery, export sessionmemorycmd.Export, code sessionmemory.ErrorCode, class sessionmemory.ErrorClass, reason string) error {
	metadata, metadataErr := delivery.Metadata()
	if metadataErr != nil {
		metadata = DeliveryMetadata{}
	}
	diagnostic := DeadLetter{
		ExportID:         export.ExportID(),
		Kind:             string(export.Kind),
		Subject:          delivery.Subject(),
		StreamSequence:   metadata.StreamSequence,
		ConsumerSequence: metadata.ConsumerSequence,
		DeliveryCount:    metadata.DeliveryCount,
		ErrorCode:        code,
		ErrorClass:       class,
		Reason:           strings.TrimSpace(reason),
	}
	if err := w.transport.PublishDeadLetter(ctx, diagnostic); err != nil {
		return fmt.Errorf("publish session-memory dead letter: %w", err)
	}
	if err := delivery.Term(diagnostic.Reason); err != nil {
		return fmt.Errorf("term session-memory delivery: %w", err)
	}
	w.logger.Warn().
		Str("operation", "dead_letter").
		Str("export_id", diagnostic.ExportID).
		Str("kind", diagnostic.Kind).
		Str("error_code", string(diagnostic.ErrorCode)).
		Str("error_class", string(diagnostic.ErrorClass)).
		Msg(diagnostic.Reason)
	return nil
}

func classifyProviderFailure(err error) (sessionmemory.ErrorCode, sessionmemory.ErrorClass) {
	if code, class, ok := sessionmemory.ClassifyError(err); ok {
		return code, class
	}
	return sessionmemory.CodeUnavailable, sessionmemory.ErrorClassRetryable
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
