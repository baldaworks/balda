package badger

import (
	"context"
	"sync"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

const (
	defaultCanonicalGCInterval = 6 * time.Hour
	defaultCanonicalDiscard    = 0.5
)

// CanonicalMaintenanceConfig controls the optional value-log maintenance
// worker. It is separate from the store so the composition root can start and
// stop maintenance in the same lifecycle as the canonical owner.
type CanonicalMaintenanceConfig struct {
	ValueLogGCInterval time.Duration
	DiscardRatio       float64
}

// CanonicalMaintenance owns one bounded maintenance goroutine for a Badger
// canonical store. It never repairs records and it never owns store closure.
type CanonicalMaintenance struct {
	store    *BadgerSessionMemoryStore
	interval time.Duration
	ratio    float64

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	lastErr error
}

// NewCanonicalMaintenance validates operational defaults without opening a
// second database handle or changing the store's ownership boundary.
func NewCanonicalMaintenance(store *BadgerSessionMemoryStore, config CanonicalMaintenanceConfig) (*CanonicalMaintenance, error) {
	if store == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical maintenance store is required", nil)
	}
	interval := config.ValueLogGCInterval
	if interval == 0 {
		interval = defaultCanonicalGCInterval
	}
	if interval <= 0 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance interval must be positive", nil)
	}
	ratio := config.DiscardRatio
	if ratio == 0 {
		ratio = defaultCanonicalDiscard
	}
	if ratio <= 0 || ratio >= 1 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance discard ratio must be between zero and one", nil)
	}
	return &CanonicalMaintenance{store: store, interval: interval, ratio: ratio}, nil
}

// Start launches the single maintenance worker. A second start is rejected so
// one canonical owner cannot accidentally schedule duplicate GC loops.
func (m *CanonicalMaintenance) Start(ctx context.Context) error {
	if m == nil || m.store == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical maintenance is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance context is required", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical maintenance is already running", nil)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	m.running = true
	m.cancel = cancel
	m.done = make(chan struct{})
	m.lastErr = nil
	done := m.done
	go m.run(workerCtx, done)
	return nil
}

func (m *CanonicalMaintenance) run(ctx context.Context, done chan struct{}) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	defer close(done)
	defer func() {
		m.mu.Lock()
		m.running = false
		m.cancel = nil
		m.done = nil
		m.mu.Unlock()
	}()
	for {
		select {
		case <-ticker.C:
			if err := m.store.RunValueLogGC(m.ratio); err != nil {
				m.mu.Lock()
				m.lastErr = err
				m.mu.Unlock()
			}
		case <-ctx.Done():
			return
		}
	}
}

// Stop cancels maintenance and waits for the worker to release its ticker.
// The store remains open for other lifecycle work until Close is called.
func (m *CanonicalMaintenance) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical maintenance context is required", nil)
	}
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "canonical maintenance did not stop before context ended", ctx.Err())
	}
}

// Close stops maintenance and then closes the sole Badger owner. The caller
// can use this as the composition-root OnStop hook.
func (m *CanonicalMaintenance) Close(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.store.Close()
}

// LastError reports the latest non-fatal GC diagnostic. Value-log GC failures
// do not make canonical records writable or trigger destructive repair; the
// composition root can surface this diagnostic and choose to stop the owner.
func (m *CanonicalMaintenance) LastError() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}
