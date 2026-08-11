package sessionmemoryapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorytest"
	"github.com/normahq/balda/sessionmemory"
	"github.com/rs/zerolog"
)

func TestWorkerSerializesTurnAndBoundaryWithRetry(t *testing.T) {
	transport := newTestTransport(2)
	turn := testTurnExport(t, "turn-serial")
	boundary := testBoundaryExport(t, "boundary-serial")
	first := newTestDelivery(turn)
	second := newTestDelivery(boundary)
	transport.push(first)
	transport.push(second)

	var mu sync.Mutex
	var calls []string
	attempts := 0
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(context.Context, sessionmemory.Turn) error {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			calls = append(calls, "turn")
			if attempts == 1 {
				return sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "temporary", nil)
			}
			return nil
		},
		ApplyBoundaryFunc: func(context.Context, sessionmemory.Boundary) error {
			mu.Lock()
			calls = append(calls, "boundary")
			mu.Unlock()
			return nil
		},
	}
	worker := newTestWorker(t, transport, provider, Config{
		Enabled:          true,
		MaxAttempts:      2,
		RetryBaseDelay:   20 * time.Millisecond,
		RetryMaxDelay:    20 * time.Millisecond,
		ProgressInterval: 5 * time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return first.acked() && second.acked() })
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	if len(gotCalls) != 3 || gotCalls[0] != "turn" {
		t.Fatalf("provider call order = %v, want first call to be turn and total calls = 3", gotCalls)
	}
	turnCalls, boundaryCalls := 0, 0
	for _, call := range gotCalls {
		switch call {
		case "turn":
			turnCalls++
		case "boundary":
			boundaryCalls++
		}
	}
	if turnCalls != 2 || boundaryCalls != 1 {
		t.Fatalf("provider calls = %v, want two turn attempts and one boundary", gotCalls)
	}
	if transport.fetches() != 3 {
		t.Fatalf("fetch count = %d, want one fetch per delivery plus shutdown fetch", transport.fetches())
	}
}

func TestWorkerAllowsIndependentScopesWhileOneScopeIsBlocked(t *testing.T) {
	transport := newTestTransport(2)
	blocked := newTestDelivery(testTurnExportForScope(t, "turn-blocked", "telegram:blocked"))
	independent := newTestDelivery(testTurnExportForScope(t, "turn-independent", "telegram:independent"))
	transport.push(blocked)
	transport.push(independent)
	blockedStarted := make(chan struct{})
	releaseBlocked := make(chan struct{})
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(ctx context.Context, turn sessionmemory.Turn) error {
			if turn.Scope.Key != "telegram:blocked" {
				return nil
			}
			close(blockedStarted)
			select {
			case <-releaseBlocked:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	worker := newTestWorker(t, transport, provider, Config{Enabled: true, MaxConcurrentScopes: 2, ProgressInterval: time.Millisecond})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-blockedStarted:
	case <-time.After(time.Second):
		t.Fatal("blocked scope did not start")
	}
	waitFor(t, time.Second, independent.acked)
	if blocked.acked() {
		t.Fatal("blocked scope completed before release")
	}
	close(releaseBlocked)
	waitFor(t, time.Second, blocked.acked)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestWorkerBoundsConcurrencyAcross128IndependentScopes(t *testing.T) {
	const (
		scopes      = 128
		concurrency = 4
	)
	transport := newTestTransport(scopes)
	deliveries := make([]*testDelivery, 0, scopes)
	for index := 0; index < scopes; index++ {
		delivery := newTestDelivery(testTurnExportForScope(t, fmt.Sprintf("turn-%03d", index), fmt.Sprintf("telegram:scope-%03d", index)))
		deliveries = append(deliveries, delivery)
		transport.push(delivery)
	}
	var mu sync.Mutex
	running, maximum := 0, 0
	limitReached := make(chan struct{})
	var closeOnce sync.Once
	release := make(chan struct{})
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(context.Context, sessionmemory.Turn) error {
			mu.Lock()
			running++
			if running > maximum {
				maximum = running
			}
			if running >= concurrency {
				closeOnce.Do(func() { close(limitReached) })
			}
			mu.Unlock()
			<-release
			mu.Lock()
			running--
			mu.Unlock()
			return nil
		},
	}
	worker := newTestWorker(t, transport, provider, Config{Enabled: true, MaxConcurrentScopes: concurrency, MaxQueuedPerScope: 1})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-limitReached:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach configured concurrency")
	}
	close(release)
	waitFor(t, 5*time.Second, func() bool {
		for _, delivery := range deliveries {
			if !delivery.acked() {
				return false
			}
		}
		return true
	})
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	mu.Lock()
	gotMaximum := maximum
	mu.Unlock()
	if gotMaximum != concurrency {
		t.Fatalf("maximum concurrent capability calls = %d, want %d", gotMaximum, concurrency)
	}
}

func TestWorkerProgressAcknowledgesLongProviderCall(t *testing.T) {
	transport := newTestTransport(1)
	delivery := newTestDelivery(testTurnExport(t, "turn-progress"))
	transport.push(delivery)
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(ctx context.Context, _ sessionmemory.Turn) error {
			close(providerStarted)
			select {
			case <-releaseProvider:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	worker := newTestWorker(t, transport, provider, Config{
		Enabled:          true,
		ProgressInterval: 5 * time.Millisecond,
		ShutdownTimeout:  time.Second,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	waitFor(t, time.Second, func() bool { return delivery.progressCalls() > 0 })
	close(releaseProvider)
	waitFor(t, time.Second, delivery.acked)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestWorkerPublishesRedactedDLQBeforeTerminatingPermanentFailure(t *testing.T) {
	transport := newTestTransport(1)
	delivery := newTestDelivery(testTurnExport(t, "turn-permanent"))
	delivery.order = transport.order
	transport.push(delivery)
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(context.Context, sessionmemory.Turn) error {
			return sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "provider response contains another scope", errors.New("secret body"))
		},
	}
	worker := newTestWorker(t, transport, provider, Config{Enabled: true})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitFor(t, time.Second, delivery.terminated)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	transport.mu.Lock()
	deadLetters := append([]DeadLetter(nil), transport.deadLetters...)
	transport.mu.Unlock()
	if len(deadLetters) != 1 {
		t.Fatalf("dead-letter count = %d, want 1", len(deadLetters))
	}
	deadLetter := deadLetters[0]
	if deadLetter.ErrorCode != sessionmemory.CodeScopeViolation || deadLetter.ErrorClass != sessionmemory.ErrorClassPermanent {
		t.Fatalf("dead-letter classification = %q/%q", deadLetter.ErrorCode, deadLetter.ErrorClass)
	}
	if deadLetter.Reason == "" || deadLetter.ExportID == "" {
		t.Fatalf("dead-letter diagnostic = %+v, want stable reason and export id", deadLetter)
	}
	if stringsContains(deadLetter.Reason, "secret") {
		t.Fatalf("dead-letter leaked provider body: %q", deadLetter.Reason)
	}
	if !delivery.orderedEvents([]string{"dlq", "term"}) {
		t.Fatalf("delivery events = %v, want DLQ before Term", delivery.eventsSnapshot())
	}
}

func TestWorkerExhaustsRetryBudgetBeforeDLQ(t *testing.T) {
	transport := newTestTransport(1)
	delivery := newTestDelivery(testTurnExport(t, "turn-exhausted"))
	delivery.order = transport.order
	transport.push(delivery)
	attempts := 0
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(context.Context, sessionmemory.Turn) error {
			attempts++
			return sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "temporary", nil)
		},
	}
	worker := newTestWorker(t, transport, provider, Config{
		Enabled:        true,
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		RetryMaxDelay:  time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitFor(t, time.Second, delivery.terminated)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("provider attempts = %d, want 2", attempts)
	}
	transport.mu.Lock()
	deadLetters := append([]DeadLetter(nil), transport.deadLetters...)
	transport.mu.Unlock()
	if len(deadLetters) != 1 || deadLetters[0].ErrorClass != sessionmemory.ErrorClassRetryable {
		t.Fatalf("retry exhaustion dead letters = %+v", deadLetters)
	}
}

func TestWorkerDisabledDoesNotFetchOrInspectCapabilities(t *testing.T) {
	transport := newTestTransport(1)
	worker := newTestWorker(t, transport, nil, Config{})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() disabled error = %v", err)
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() disabled error = %v", err)
	}
	if transport.fetches() != 0 || transport.statsCalls() != 0 {
		t.Fatalf("disabled transport calls = fetch:%d stats:%d, want zero", transport.fetches(), transport.statsCalls())
	}
}

func TestWorkerStopReportsBacklog(t *testing.T) {
	transport := newTestTransport(1)
	transport.stats = BacklogStats{Messages: 3, Pending: 2, Acknowledging: 1, OldestPendingAt: time.Unix(10, 0).UTC()}
	provider := &sessionmemorytest.Provider{}
	worker := newTestWorker(t, transport, provider, Config{Enabled: true})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	report := worker.ShutdownReport()
	if report.Stats != transport.stats {
		t.Fatalf("shutdown stats = %+v, want %+v", report.Stats, transport.stats)
	}
}

func newTestWorker(t *testing.T, transport *testTransport, provider *sessionmemorytest.Provider, config Config) *Worker {
	t.Helper()
	worker, err := NewCapabilityWorker(transport, provider, provider, config, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewCapabilityWorker() error = %v", err)
	}
	t.Cleanup(func() { _ = worker.Stop(context.Background()) })
	return worker
}

type testTransport struct {
	deliveries  chan Delivery
	mu          sync.Mutex
	fetchCount  int
	statsCount  int
	deadLetters []DeadLetter
	stats       BacklogStats
	order       *testOrder
}

func newTestTransport(capacity int) *testTransport {
	return &testTransport{deliveries: make(chan Delivery, capacity), order: &testOrder{}}
}

func (t *testTransport) push(delivery Delivery) {
	t.deliveries <- delivery
}

func (t *testTransport) Fetch(ctx context.Context) (Delivery, error) {
	t.mu.Lock()
	t.fetchCount++
	t.mu.Unlock()
	select {
	case delivery := <-t.deliveries:
		return delivery, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *testTransport) PublishDeadLetter(_ context.Context, diagnostic DeadLetter) error {
	t.mu.Lock()
	t.deadLetters = append(t.deadLetters, diagnostic)
	t.mu.Unlock()
	t.order.append("dlq")
	return nil
}

func (t *testTransport) Stats(context.Context) (BacklogStats, error) {
	t.mu.Lock()
	t.statsCount++
	stats := t.stats
	t.mu.Unlock()
	return stats, nil
}

func (t *testTransport) fetches() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fetchCount
}

func (t *testTransport) statsCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.statsCount
}

type testDelivery struct {
	mu             sync.Mutex
	export         sessionmemorycmd.Export
	progress       int
	ackedFlag      bool
	terminatedFlag bool
	events         []string
	order          *testOrder
}

func newTestDelivery(export sessionmemorycmd.Export) *testDelivery {
	return &testDelivery{export: export}
}

func (d *testDelivery) Export() (sessionmemorycmd.Export, error) { return d.export, nil }
func (d *testDelivery) Subject() string                          { return d.export.Subject() }
func (d *testDelivery) Metadata() (DeliveryMetadata, error) {
	return DeliveryMetadata{StreamSequence: 1, ConsumerSequence: 1, DeliveryCount: 1}, nil
}
func (d *testDelivery) InProgress(context.Context) error {
	d.mu.Lock()
	d.progress++
	d.mu.Unlock()
	return nil
}
func (d *testDelivery) Ack(context.Context) error {
	d.mu.Lock()
	d.ackedFlag = true
	d.events = append(d.events, "ack")
	d.mu.Unlock()
	if d.order != nil {
		d.order.append("ack")
	}
	return nil
}
func (d *testDelivery) Term(string) error {
	d.mu.Lock()
	d.terminatedFlag = true
	d.events = append(d.events, "term")
	d.mu.Unlock()
	if d.order != nil {
		d.order.append("term")
	}
	return nil
}
func (d *testDelivery) acked() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ackedFlag
}
func (d *testDelivery) terminated() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.terminatedFlag
}
func (d *testDelivery) progressCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.progress
}
func (d *testDelivery) orderedEvents(want []string) bool {
	if d.order == nil {
		return false
	}
	got := d.order.snapshot()
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
func (d *testDelivery) eventsSnapshot() []string {
	if d.order != nil {
		return d.order.snapshot()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.events...)
}

type testOrder struct {
	mu     sync.Mutex
	events []string
}

func (o *testOrder) append(event string) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}

func (o *testOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func testTurnExport(t *testing.T, sourceID string) sessionmemorycmd.Export {
	return testTurnExportForScope(t, sourceID, "telegram:1:0")
}

func testTurnExportForScope(t *testing.T, sourceID, scopeKey string) sessionmemorycmd.Export {
	t.Helper()
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: scopeKey, Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"},
		sourceID,
		time.Now().UTC(),
		"hello",
		"hi",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn export error = %v", err)
	}
	return export
}

func testBoundaryExport(t *testing.T, transitionID string) sessionmemorycmd.Export {
	t.Helper()
	boundary, err := sessionmemory.NewBoundary(
		sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"},
		transitionID,
		sessionmemory.BoundaryReasonRotation,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	export, err := sessionmemorycmd.NewBoundary(boundary)
	if err != nil {
		t.Fatalf("NewBoundary export error = %v", err)
	}
	return export
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func stringsContains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
