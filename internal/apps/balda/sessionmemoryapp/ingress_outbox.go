package sessionmemoryapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
	"github.com/rs/zerolog"
)

const (
	defaultIngressLease    = time.Minute
	defaultIngressInterval = time.Second
	defaultIngressBatch    = 32
)

// IngressOutboxStore is the producer-local durable handoff. Its concrete
// storage adapter belongs to state; publication policy belongs here.
type IngressOutboxStore interface {
	EnqueueSessionMemoryIngress(ctx context.Context, record sessionmemorycmd.IngressRecord) (sessionmemorycmd.IngressRecord, bool, error)
	ClaimSessionMemoryIngress(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]sessionmemorycmd.IngressRecord, error)
	MarkSessionMemoryIngressPublished(ctx context.Context, exportID, owner string, publishedAt time.Time) error
	ReleaseSessionMemoryIngress(ctx context.Context, exportID, owner, reason string, terminal bool, updatedAt time.Time) error
}

// IngressOutboxConfig controls the bounded background publisher.
type IngressOutboxConfig struct {
	Enabled       bool
	LeaseDuration time.Duration
	PollInterval  time.Duration
	BatchSize     int
	WorkerID      string
}

func (c IngressOutboxConfig) normalized() (IngressOutboxConfig, error) {
	out := c
	if out.LeaseDuration <= 0 {
		out.LeaseDuration = defaultIngressLease
	}
	if out.PollInterval <= 0 {
		out.PollInterval = defaultIngressInterval
	}
	if out.BatchSize <= 0 {
		out.BatchSize = defaultIngressBatch
	}
	if out.BatchSize > 256 {
		return IngressOutboxConfig{}, fmt.Errorf("session-memory ingress batch size must be at most 256")
	}
	if strings.TrimSpace(out.WorkerID) == "" {
		out.WorkerID = "session-memory-ingress-" + uuid.NewString()
	}
	return out, nil
}

// IngressOutboxPublisher persists capture before sending it to the downstream
// durable transport. A successful PubAck is the only condition that settles a
// record; a process crash leaves an expired lease for a later publisher.
type IngressOutboxPublisher struct {
	store      IngressOutboxStore
	downstream ExportPublisher
	config     IngressOutboxConfig
	logger     zerolog.Logger
	now        func() time.Time

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

var _ ExportPublisher = (*IngressOutboxPublisher)(nil)

func NewIngressOutboxPublisher(store IngressOutboxStore, downstream ExportPublisher, config IngressOutboxConfig, logger zerolog.Logger) (*IngressOutboxPublisher, error) {
	if store == nil {
		return nil, fmt.Errorf("session-memory ingress outbox store is required")
	}
	if config.Enabled && downstream == nil {
		return nil, fmt.Errorf("session-memory downstream publisher is required when enabled")
	}
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &IngressOutboxPublisher{
		store:      store,
		downstream: downstream,
		config:     normalized,
		logger:     logger.With().Str("component", "balda.session_memory_ingress_outbox").Logger(),
		now:        time.Now,
	}, nil
}

// Publish only requires local durability. Transport failure is retained for
// the background publisher and must not lose an otherwise completed turn.
func (p *IngressOutboxPublisher) Publish(ctx context.Context, export sessionmemorycmd.Export) error {
	if p == nil || !p.config.Enabled {
		return nil
	}
	record, err := sessionmemorycmd.NewIngressRecord(export, p.currentTime().UTC())
	if err != nil {
		return err
	}
	if _, _, err := p.store.EnqueueSessionMemoryIngress(ctx, record); err != nil {
		return fmt.Errorf("enqueue session-memory ingress: %w", err)
	}
	if err := p.Flush(ctx); err != nil && ctx.Err() == nil {
		p.logger.Warn().Err(err).Msg("session-memory ingress publish deferred after durable enqueue")
	}
	return nil
}

func (p *IngressOutboxPublisher) Start(parent context.Context) error {
	if p == nil {
		return fmt.Errorf("session-memory ingress outbox publisher is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return ErrWorkerStarted
	}
	p.started = true
	if !p.config.Enabled {
		p.done = closedDone()
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.done = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		p.run(ctx)
	}(p.done)
	return nil
}

func (p *IngressOutboxPublisher) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		p.mu.Lock()
		p.started, p.cancel, p.done = false, nil, nil
		p.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *IngressOutboxPublisher) run(ctx context.Context) {
	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := p.Flush(ctx); err != nil && ctx.Err() == nil {
			p.logger.Warn().Err(err).Msg("session-memory ingress flush failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Flush settles every currently claimable record once. The store enforces
// exact-scope FIFO, so parallel scopes may progress without reordering one.
func (p *IngressOutboxPublisher) Flush(ctx context.Context) error {
	if p == nil || !p.config.Enabled {
		return nil
	}
	if p.downstream == nil {
		return fmt.Errorf("session-memory downstream publisher is required")
	}
	now := p.currentTime().UTC()
	records, err := p.store.ClaimSessionMemoryIngress(ctx, p.config.WorkerID, now, now.Add(p.config.LeaseDuration), p.config.BatchSize)
	if err != nil {
		return fmt.Errorf("claim session-memory ingress: %w", err)
	}
	var errs []error
	for _, record := range records {
		err := p.downstream.Publish(ctx, record.Export)
		if err == nil {
			err = p.store.MarkSessionMemoryIngressPublished(ctx, record.ExportID(), p.config.WorkerID, p.currentTime().UTC())
		}
		if err == nil {
			continue
		}
		terminal := false
		if _, class, ok := sessionmemory.ClassifyError(err); ok && class == sessionmemory.ErrorClassPermanent {
			terminal = true
		}
		reason := ingressFailureReason(err)
		if releaseErr := p.store.ReleaseSessionMemoryIngress(ctx, record.ExportID(), p.config.WorkerID, reason, terminal, p.currentTime().UTC()); releaseErr != nil {
			errs = append(errs, fmt.Errorf("settle session-memory ingress %s: %w", record.ExportID(), releaseErr))
			continue
		}
		errs = append(errs, fmt.Errorf("publish session-memory ingress %s: %w", record.ExportID(), err))
	}
	return errors.Join(errs...)
}

func (p *IngressOutboxPublisher) currentTime() time.Time {
	if p == nil || p.now == nil {
		return time.Now()
	}
	return p.now()
}

func ingressFailureReason(err error) string {
	if err == nil {
		return ""
	}
	reason := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(err.Error()), "\r", " "), "\n", " ")
	if len(reason) > 512 {
		return reason[:512]
	}
	return reason
}
