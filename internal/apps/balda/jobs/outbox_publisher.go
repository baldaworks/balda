package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	defaultOutboxPollInterval = 500 * time.Millisecond
	defaultOutboxBatchSize    = 100
)

// OutboxPublisher retries durable job events until the event stream accepts them.
type OutboxPublisher struct {
	store eventOutboxStore
	bus   actortransport.EventPublisher
	log   zerolog.Logger

	pollInterval time.Duration
	batchSize    int

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type outboxPublisherParams struct {
	fx.In

	Store  OutboxStore
	Bus    actortransport.EventPublisher
	Logger zerolog.Logger
}

type eventOutboxStore interface {
	baldastate.JobEventOutboxStore
}

// OutboxStore persists events until the publisher marks them delivered.
type OutboxStore interface {
	baldastate.JobEventOutboxStore
}

func NewOutboxPublisher(params outboxPublisherParams) (*OutboxPublisher, error) {
	if params.Store == nil {
		return nil, fmt.Errorf("job event outbox store is required")
	}
	if params.Bus == nil {
		return nil, fmt.Errorf("job event publisher requires an event bus")
	}
	return &OutboxPublisher{
		store:        params.Store,
		bus:          params.Bus,
		log:          params.Logger.With().Str("component", "balda.jobs.outbox").Logger(),
		pollInterval: defaultOutboxPollInterval,
		batchSize:    defaultOutboxBatchSize,
	}, nil
}

// Start flushes pending events once and starts the retry loop.
func (p *OutboxPublisher) Start(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.wg.Add(1)
	p.mu.Unlock()

	if err := p.Flush(ctx); err != nil {
		p.log.Warn().Err(err).Msg("initial job event outbox flush incomplete")
	}
	go p.run(runCtx)
	return nil
}

// Stop waits for the retry loop to terminate.
func (p *OutboxPublisher) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.wg.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush attempts to publish one bounded batch of pending events.
func (p *OutboxPublisher) Flush(ctx context.Context) error {
	if p == nil || p.store == nil {
		return nil
	}
	records, err := p.store.ListPendingJobEvents(ctx, p.batchSize)
	if err != nil {
		return err
	}
	var errs []error
	for _, record := range records {
		if err := publishOutboxRecord(ctx, p.store, p.bus, record); err != nil {
			errs = append(errs, fmt.Errorf("publish job event %q: %w", record.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (p *OutboxPublisher) run(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Flush(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.log.Warn().Err(err).Msg("job event outbox flush incomplete")
			}
		}
	}
}
