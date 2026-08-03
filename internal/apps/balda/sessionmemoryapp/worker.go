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
	"github.com/rs/zerolog"
)

const (
	defaultRetryAttempts   = 5
	defaultRetryBaseDelay  = 250 * time.Millisecond
	defaultRetryMaxDelay   = 5 * time.Second
	defaultProgressPeriod  = 30 * time.Second
	defaultFetchErrorDelay = 100 * time.Millisecond
	defaultShutdownTimeout = 30 * time.Second
)

// Config controls the serialized provider consumer.
type Config struct {
	Enabled          bool
	MaxAttempts      int
	RetryBaseDelay   time.Duration
	RetryMaxDelay    time.Duration
	ProgressInterval time.Duration
	FetchErrorDelay  time.Duration
	ShutdownTimeout  time.Duration
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
	return out, nil
}

// ShutdownReport records the last bounded stop attempt.
type ShutdownReport struct {
	Stats         BacklogStats
	StatsErr      error
	ProviderError error
	StoppedAt     time.Time
}

// Worker serializes all provider calls behind one durable queue delivery.
type Worker struct {
	transport Transport
	provider  sessionmemory.Provider
	config    Config
	logger    zerolog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	report  ShutdownReport
}

// NewWorker constructs a lifecycle-managed serialized consumer.
func NewWorker(transport Transport, provider sessionmemory.Provider, config Config, logger zerolog.Logger) (*Worker, error) {
	if transport == nil {
		return nil, fmt.Errorf("session-memory transport is required")
	}
	normalized, err := config.Normalized()
	if err != nil {
		return nil, err
	}
	if normalized.Enabled && provider == nil {
		return nil, fmt.Errorf("session-memory provider is required when enabled")
	}
	return &Worker{
		transport: transport,
		provider:  provider,
		config:    normalized,
		logger:    logger.With().Str("component", "balda.session_memory_worker").Logger(),
	}, nil
}

// Start launches the worker away from the user-response goroutine. Disabled
// workers deliberately create no fetch loop and require no provider.
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
// supplied bounded deadline, records backlog state, and closes the provider.
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
		// call can finish the drain and close the provider without racing an
		// in-flight provider operation.
		report.ProviderError = stopErr
		w.mu.Lock()
		w.report = report
		w.mu.Unlock()
		return stopErr
	}
	if w.config.Enabled {
		report.Stats, report.StatsErr = w.transport.Stats(stopCtx)
	}
	if w.config.Enabled && w.provider != nil {
		report.ProviderError = w.provider.Close(stopCtx)
	}

	w.mu.Lock()
	w.started = false
	w.cancel = nil
	w.done = nil
	w.report = report
	w.mu.Unlock()

	return errors.Join(stopErr, report.StatsErr, report.ProviderError)
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
		if err := w.process(ctx, delivery); err != nil && ctx.Err() == nil {
			w.logger.Warn().Str("operation", "process").Msg("session-memory delivery remains unresolved")
			if !waitContext(ctx, w.config.FetchErrorDelay) {
				return
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, delivery Delivery) error {
	export, err := delivery.Export()
	if err != nil {
		return w.deadLetter(ctx, delivery, sessionmemorycmd.Export{}, sessionmemory.CodePermanent, sessionmemory.ErrorClassPermanent, "invalid session-memory export")
	}
	if err := export.Validate(); err != nil {
		return w.deadLetter(ctx, delivery, sessionmemorycmd.Export{}, sessionmemory.CodePermanent, sessionmemory.ErrorClassPermanent, "invalid session-memory export")
	}

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
			return w.deadLetter(ctx, delivery, export, code, class, "provider permanently rejected session-memory export")
		}
		if attempt >= w.config.MaxAttempts {
			return w.deadLetter(ctx, delivery, export, code, class, "session-memory provider retry limit exhausted")
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
	if w.provider == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
	}
	switch export.Kind {
	case sessionmemorycmd.KindTurn:
		return w.provider.SyncTurn(ctx, *export.Turn)
	case sessionmemorycmd.KindBoundary:
		return w.provider.OnSessionBoundary(ctx, *export.Boundary)
	default:
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "unsupported session-memory export", nil)
	}
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
