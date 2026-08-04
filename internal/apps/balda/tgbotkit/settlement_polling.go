package tgbotkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/eventemitter"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/updatepoller"
)

const maxPollingRetryAttempts = 5

type pollingSettlementOutcome uint8

const (
	pollingSettlementPending pollingSettlementOutcome = iota
	pollingSettlementAccepted
	pollingSettlementRetry
	pollingSettlementTerminal
)

type pollingSettlementRecord struct {
	done          chan struct{}
	outcome       pollingSettlementOutcome
	retryRecorded bool
}

// pollingSettlementGate correlates one Telegram update with the synchronous
// tgbotkit event dispatch for that update. The poller only sees OffsetStore;
// this gate is the provider-local bridge between handler settlement and offset
// persistence.
type pollingSettlementGate struct {
	mu      sync.Mutex
	records map[int]*pollingSettlementRecord
	retries map[int]int
	current int
	logger  zerolog.Logger
}

func newPollingSettlementGate(logger zerolog.Logger) *pollingSettlementGate {
	return &pollingSettlementGate{
		records: make(map[int]*pollingSettlementRecord),
		retries: make(map[int]int),
		logger:  logger,
	}
}

func (g *pollingSettlementGate) begin(updateID int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.records[updateID] = &pollingSettlementRecord{done: make(chan struct{})}
	g.current = updateID
}

func (g *pollingSettlementGate) recordError(err error) {
	if err == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	record, ok := g.records[g.current]
	if !ok || record.outcome != pollingSettlementPending {
		return
	}
	if !actorlayer.IsRetryableError(err) {
		record.outcome = pollingSettlementTerminal
		return
	}
	if record.retryRecorded {
		return
	}
	record.retryRecorded = true
	g.retries[g.current]++
	if g.retries[g.current] >= maxPollingRetryAttempts {
		record.outcome = pollingSettlementTerminal
		g.logger.Warn().
			Int("update_id", g.current).
			Int("retry_attempts", g.retries[g.current]).
			Msg("telegram polling update reached retry limit; advancing offset")
		return
	}
	record.outcome = pollingSettlementRetry
}

func (g *pollingSettlementGate) finish(updateID int) {
	g.mu.Lock()
	record, ok := g.records[updateID]
	if !ok {
		g.mu.Unlock()
		return
	}
	if record.outcome == pollingSettlementPending {
		record.outcome = pollingSettlementAccepted
	}
	close(record.done)
	if g.current == updateID {
		g.current = 0
	}
	g.mu.Unlock()
}

type pollingSettlementBatch struct {
	ids     []int
	outcome pollingSettlementOutcome
}

func (g *pollingSettlementGate) awaitThrough(ctx context.Context, finalUpdateID int) (pollingSettlementBatch, error) {
	if finalUpdateID < 0 {
		return pollingSettlementBatch{}, fmt.Errorf("invalid final telegram update id %d", finalUpdateID)
	}

	for {
		g.mu.Lock()
		final, ok := g.records[finalUpdateID]
		if ok {
			records := make(map[int]*pollingSettlementRecord, len(g.records))
			ids := make([]int, 0, len(g.records))
			for id, record := range g.records {
				if id <= finalUpdateID {
					records[id] = record
					ids = append(ids, id)
				}
			}
			g.mu.Unlock()

			if err := waitSettlement(ctx, final.done); err != nil {
				return pollingSettlementBatch{}, err
			}
			outcome := pollingSettlementAccepted
			for _, id := range ids {
				if err := waitSettlement(ctx, records[id].done); err != nil {
					return pollingSettlementBatch{}, err
				}
				g.mu.Lock()
				recordOutcome := records[id].outcome
				g.mu.Unlock()
				if recordOutcome == pollingSettlementRetry {
					outcome = pollingSettlementRetry
				}
			}
			return pollingSettlementBatch{ids: ids, outcome: outcome}, nil
		}
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			return pollingSettlementBatch{}, ctx.Err()
		default:
		}
		// The final update may still be waiting in the runtime update channel.
		// Poll briefly until the channel consumer registers it, while honoring
		// shutdown cancellation.
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return pollingSettlementBatch{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func waitSettlement(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *pollingSettlementGate) release(batch pollingSettlementBatch) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range batch.ids {
		delete(g.records, id)
		if batch.outcome != pollingSettlementRetry {
			delete(g.retries, id)
		}
	}
}

type settlementOffsetStore struct {
	store updatepoller.OffsetStore
	gate  *pollingSettlementGate
}

func (s settlementOffsetStore) Load(ctx context.Context) (int, error) {
	return s.store.Load(ctx)
}

func (s settlementOffsetStore) Save(ctx context.Context, offset int) error {
	batch, err := s.gate.awaitThrough(ctx, offset-1)
	if err != nil {
		return err
	}
	if batch.outcome == pollingSettlementRetry {
		// Returning success without persisting leaves the durable offset at the
		// previous value. The poller will fetch the same update again, preserving
		// Telegram's stable update ID and Balda's normalized dedupe identity.
		s.gate.release(batch)
		return nil
	}
	if err := s.store.Save(ctx, offset); err != nil {
		return err
	}
	s.gate.release(batch)
	return nil
}

type settlementPoller struct {
	poller  *updatepoller.Poller
	emitter eventemitter.EventEmitter
}

func (s *settlementPoller) UpdateChan() <-chan client.Update { return s.poller.UpdateChan() }
func (s *settlementPoller) Start(ctx context.Context) error  { return s.poller.Start(ctx) }
func (s *settlementPoller) Stop(ctx context.Context) error   { return s.poller.Stop(ctx) }

func newSettlementPoller(poller *updatepoller.Poller, gate *pollingSettlementGate) (*settlementPoller, error) {
	base, err := eventemitter.NewSync(eventemitter.NewOptions(
		eventemitter.WithStopOnError(false),
		eventemitter.WithErrorHandler(func(_ string, err error) { gate.recordError(err) }),
	))
	if err != nil {
		return nil, fmt.Errorf("create telegram settlement event emitter: %w", err)
	}
	return &settlementPoller{
		poller:  poller,
		emitter: settlementEventEmitter{EventEmitter: base, gate: gate},
	}, nil
}

type settlementEventEmitter struct {
	eventemitter.EventEmitter
	gate *pollingSettlementGate
}

func (e settlementEventEmitter) Emit(ctx context.Context, event string, payload any) {
	if event != events.OnUpdate {
		e.EventEmitter.Emit(ctx, event, payload)
		return
	}
	updateEvent, ok := payload.(*events.UpdateEvent)
	if !ok || updateEvent == nil || updateEvent.Update == nil {
		e.EventEmitter.Emit(ctx, event, payload)
		return
	}
	updateID := updateEvent.Update.UpdateId
	e.gate.begin(updateID)
	e.EventEmitter.Emit(ctx, event, payload)
	e.gate.finish(updateID)
}

var _ updatepoller.OffsetStore = settlementOffsetStore{}
var _ interface {
	UpdateChan() <-chan client.Update
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
} = (*settlementPoller)(nil)
