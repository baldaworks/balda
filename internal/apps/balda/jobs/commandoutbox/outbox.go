// Package commandoutbox owns durable command admission and publication policy.
package commandoutbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const ingressOutboxPrefix = "command-outbox/pending/"
const ingressReceiptPrefix = "command-outbox/receipts/"
const ingressSequenceKey = "command-outbox/admission-sequence"

// OutboxStore is the local durable handoff, independent of the command bus.
type OutboxStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

type ingressRecord struct {
	Sequence    uint64                          `json:"sequence,omitempty"`
	Envelope    string                          `json:"envelope,omitempty"`
	Receipt     *actortransport.DispatchReceipt `json:"receipt,omitempty"`
	CreatedAt   time.Time                       `json:"created_at"`
	NextAttempt time.Time                       `json:"next_attempt,omitempty"`
	Attempts    int                             `json:"attempts"`
	RetainUntil time.Time                       `json:"retain_until,omitempty"`
}

// Dispatcher preserves accepted input when the downstream bus is full or
// unavailable. The host runs one publisher per state database. Published receipts
// remain as seven-day deduplication tombstones; replay always uses the original envelope.
type Dispatcher struct {
	store       OutboxStore
	downstream  actortransport.Dispatcher
	logger      zerolog.Logger
	mu          sync.Mutex
	flushMu     sync.Mutex
	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	now         func() time.Time
	gcCursor    map[string]string
}

// NewDispatcher constructs the producer-local command outbox.
func NewDispatcher(store OutboxStore, downstream actortransport.Dispatcher, logger zerolog.Logger) (*Dispatcher, error) {
	if store == nil || downstream == nil {
		return nil, fmt.Errorf("durable ingress requires local storage and downstream transport")
	}
	return &Dispatcher{store: store, downstream: downstream, logger: logger, now: time.Now, gcCursor: map[string]string{}}, nil
}

// Dispatch persists the exact prepared command before attempting its publish.
func (d *Dispatcher) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	wire, err := actorlayer.EncodeEnvelope(env)
	if err != nil {
		return nil, err
	}
	if len(wire) > 1<<20 {
		return nil, actorlayer.DecodeError(fmt.Errorf("ingress envelope exceeds 1 MiB"))
	}
	sum := sha256.Sum256([]byte(env.Namespace + "/" + actorlayer.DedupeKeyOrID(env)))
	key := ingressOutboxPrefix + hex.EncodeToString(sum[:])
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, found, err := d.store.Get(ctx, ingressReceiptPrefix+strings.TrimPrefix(key, ingressOutboxPrefix))
	if err != nil {
		return nil, err
	}
	if found {
		var record ingressRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		if record.Receipt != nil {
			receipt := *record.Receipt
			receipt.Duplicate = true
			return &receipt, nil
		}
	}
	if _, failed, err := d.store.Get(ctx, "command-outbox/failed/"+strings.TrimPrefix(key, ingressOutboxPrefix)); err != nil {
		return nil, err
	} else if failed {
		return nil, actorlayer.PolicyError(fmt.Errorf("durable ingress command expired; submit a new request"))
	}
	raw, found, err = d.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	var record ingressRecord
	if found {
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		if record.Receipt != nil {
			receipt := *record.Receipt
			receipt.Duplicate = true
			return &receipt, nil
		}
		original, err := actorlayer.DecodeEnvelope(record.Envelope)
		if err != nil {
			return nil, err
		}
		return admissionReceipt(original, true), nil
	} else {
		keys, err := d.store.List(ctx, ingressOutboxPrefix)
		if err != nil {
			return nil, err
		}
		if len(keys) >= 256 {
			return nil, actorlayer.TransientError(fmt.Errorf("durable ingress queue is full"))
		}
		sequence, err := d.nextSequence(ctx)
		if err != nil {
			return nil, err
		}
		record = ingressRecord{Sequence: sequence, Envelope: wire, CreatedAt: d.now().UTC()}
		if err := d.save(ctx, key, record); err != nil {
			return nil, err
		}
	}
	// Local persistence is acceptance. A bus outage cannot turn an admitted
	// command into a retryable transport rejection while recovery still owns it.
	return admissionReceipt(env, false), nil
}

// nextSequence runs under the single owner's admission lock. Persisting the
// watermark before the row permits gaps after failed writes, never reused order
// values after restart. The pending row remains the acceptance boundary.
func (d *Dispatcher) nextSequence(ctx context.Context) (uint64, error) {
	raw, found, err := d.store.Get(ctx, ingressSequenceKey)
	if err != nil {
		return 0, err
	}
	var sequence uint64
	if found {
		sequence, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("read ingress admission sequence: %w", err)
		}
	}
	if sequence == ^uint64(0) {
		return 0, fmt.Errorf("ingress admission sequence exhausted")
	}
	sequence++
	if err := d.store.Set(ctx, ingressSequenceKey, strconv.FormatUint(sequence, 10)); err != nil {
		return 0, err
	}
	return sequence, nil
}

func admissionReceipt(env actorlayer.Envelope, duplicate bool) *actortransport.DispatchReceipt {
	return &actortransport.DispatchReceipt{Stream: "local-command-outbox", MsgID: env.ID, Duplicate: duplicate}
}

func (d *Dispatcher) save(ctx context.Context, key string, record ingressRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return d.store.Set(ctx, key, string(raw))
}

func (d *Dispatcher) publish(ctx context.Context, key string, record *ingressRecord) (*actortransport.DispatchReceipt, error) {
	env, err := actorlayer.DecodeEnvelope(record.Envelope)
	if err != nil {
		return nil, err
	}
	deadline := record.CreatedAt.Add(24 * time.Hour)
	if !env.ExpiresAt.IsZero() && env.ExpiresAt.Before(deadline) {
		deadline = env.ExpiresAt
	}
	if !d.now().Before(deadline) {
		record.Envelope = ""
		record.RetainUntil = d.now().UTC().Add(7 * 24 * time.Hour)
		if err := d.finish(ctx, key, "command-outbox/failed/", *record); err != nil {
			return nil, err
		}
		d.logger.Error().Str("envelope_id", env.ID).Msg("durable ingress command expired before publication")
		return nil, actorlayer.DecodeError(fmt.Errorf("durable ingress command expired before publication"))
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	receipt, err := d.downstream.Dispatch(callCtx, env)
	if err == nil && receipt == nil {
		err = fmt.Errorf("command transport omitted durable receipt")
	}
	if err != nil {
		record.Attempts++
		delay := time.Second * time.Duration(1<<min(record.Attempts, 6))
		record.NextAttempt = d.now().Add(delay)
		d.mu.Lock()
		saveErr := d.save(ctx, key, *record)
		d.mu.Unlock()
		if saveErr != nil {
			return nil, saveErr
		}
		d.logger.Warn().Err(err).Str("envelope_id", env.ID).Msg("ingress command retained for automatic retry")
		return nil, actorlayer.TransientError(err)
	}
	record.Receipt = receipt
	record.RetainUntil = d.now().UTC().Add(7 * 24 * time.Hour)
	record.Envelope = ""
	record.NextAttempt = time.Time{}
	if err := d.finish(ctx, key, ingressReceiptPrefix, *record); err != nil {
		return nil, err
	}
	return receipt, nil
}

// Flush retries a bounded batch, including commands saved by a previous process.
func (d *Dispatcher) Flush(ctx context.Context) error {
	d.flushMu.Lock()
	defer d.flushMu.Unlock()
	d.mu.Lock()
	if err := d.collect(ctx); err != nil {
		d.mu.Unlock()
		return err
	}
	pending, err := d.pending(ctx)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	attempts := 0
	blocked := map[actorlayer.ActorAddress]bool{}
	for _, item := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		record := item.record
		scope := publicationScope(item.env)
		if blocked[scope] {
			continue
		}
		if record.NextAttempt.After(d.now()) {
			blocked[scope] = true
			continue
		}
		if _, err := d.publish(ctx, item.key, &record); err != nil {
			blocked[scope] = true
		}
		attempts++
		if attempts >= 32 {
			break
		}
	}
	return nil
}

func publicationScope(env actorlayer.Envelope) actorlayer.ActorAddress {
	if sessionID := actorcmd.EnvelopeSessionID(env); sessionID != "" {
		return actorlayer.ActorAddress{Target: actorcmd.ActorTypeSession, Key: sessionID}
	}
	return env.To
}

type pendingIngress struct {
	key    string
	record ingressRecord
	env    actorlayer.Envelope
}

func (d *Dispatcher) pending(ctx context.Context) ([]pendingIngress, error) {
	keys, err := d.store.List(ctx, ingressOutboxPrefix)
	if err != nil {
		return nil, err
	}
	var pending []pendingIngress
	for _, key := range keys {
		_, published, err := d.store.Get(ctx, ingressReceiptPrefix+strings.TrimPrefix(key, ingressOutboxPrefix))
		if err != nil {
			return nil, err
		}
		_, expired, err := d.store.Get(ctx, "command-outbox/failed/"+strings.TrimPrefix(key, ingressOutboxPrefix))
		if err != nil {
			return nil, err
		}
		if published || expired {
			if err := d.store.Delete(ctx, key); err != nil {
				return nil, err
			}
			continue
		}
		raw, found, err := d.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		var record ingressRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		env, err := actorlayer.DecodeEnvelope(record.Envelope)
		if err != nil {
			return nil, err
		}
		pending = append(pending, pendingIngress{key: key, record: record, env: env})
	}
	sort.Slice(pending, func(i, j int) bool {
		left, right := pending[i].record.Sequence, pending[j].record.Sequence
		if left != right {
			// Older pending rows have no sequence. They were admitted before
			// sequenced rows and drain first in their existing timestamp order.
			return left < right
		}
		if pending[i].record.CreatedAt.Equal(pending[j].record.CreatedAt) {
			return pending[i].key < pending[j].key
		}
		return pending[i].record.CreatedAt.Before(pending[j].record.CreatedAt)
	})
	return pending, nil
}

// Start starts recovery publication independently of websocket delivery retries.
func (d *Dispatcher) Start() {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := d.Flush(ctx); err != nil && ctx.Err() == nil {
				d.logger.Error().Err(err).Msg("durable ingress recovery failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Stop stops recovery publication before its storage closes.
func (d *Dispatcher) Stop(ctx context.Context) error {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.cancel == nil {
		return nil
	}
	d.cancel()
	select {
	case <-d.done:
		d.cancel = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// collect scans at most 64 terminal values per prefix per tick, continuing in
// key order. Pending commands are never part of retention garbage collection.
func (d *Dispatcher) collect(ctx context.Context) error {
	for _, prefix := range []string{ingressReceiptPrefix, "command-outbox/failed/"} {
		keys, err := d.store.List(ctx, prefix)
		if err != nil {
			return err
		}
		sort.Strings(keys)
		start := sort.SearchStrings(keys, d.gcCursor[prefix])
		if start < len(keys) && keys[start] == d.gcCursor[prefix] {
			start++
		}
		end := min(start+64, len(keys))
		for _, key := range keys[start:end] {
			raw, found, err := d.store.Get(ctx, key)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			var record ingressRecord
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				return err
			}
			if !record.RetainUntil.IsZero() && !d.now().Before(record.RetainUntil) {
				// Finish cleanup from a crash between terminal save and pending
				// deletion before forgetting the deduplication tombstone.
				pendingKey := ingressOutboxPrefix + strings.TrimPrefix(key, prefix)
				if err := d.store.Delete(ctx, pendingKey); err != nil {
					return err
				}
				if err := d.store.Delete(ctx, key); err != nil {
					return err
				}
			}
		}
		if end == len(keys) {
			d.gcCursor[prefix] = ""
		} else {
			d.gcCursor[prefix] = keys[end-1]
		}
	}
	return nil
}

func (d *Dispatcher) finish(ctx context.Context, key, prefix string, record ingressRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.save(ctx, prefix+strings.TrimPrefix(key, ingressOutboxPrefix), record); err != nil {
		return err
	}
	return d.store.Delete(ctx, key)
}
