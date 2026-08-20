package natsbus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	baldaeventbus "github.com/baldaworks/balda/internal/apps/balda/eventbus"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorytest"
	"github.com/baldaworks/balda/sessionmemory"
	"github.com/rs/zerolog"
)

func TestBus_SessionMemoryWorkerProcessesOrderedExportsAndRetries(t *testing.T) {
	h := StartTestRuntime(t, enabledSessionMemoryExecutionConfig())
	turn := testSessionMemoryTurn(t, "worker-turn")
	boundaryValue, err := sessionmemory.NewBoundary(
		sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "tg-1-0", AgentSessionID: "tg-1-0"},
		"worker-boundary",
		sessionmemory.BoundaryReasonRotation,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	boundary, err := sessionmemorycmd.NewBoundary(boundaryValue)
	if err != nil {
		t.Fatalf("NewBoundary export error = %v", err)
	}
	if _, err := h.Bus.PublishSessionMemory(context.Background(), turn); err != nil {
		t.Fatalf("PublishSessionMemory(turn) error = %v", err)
	}
	if _, err := h.Bus.PublishSessionMemory(context.Background(), boundary); err != nil {
		t.Fatalf("PublishSessionMemory(boundary) error = %v", err)
	}

	var mu sync.Mutex
	var calls []string
	turnAttempts := 0
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(context.Context, sessionmemory.Turn) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, "turn")
			turnAttempts++
			if turnAttempts == 1 {
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
	worker, err := sessionmemoryapp.NewCapabilityWorker(h.Bus.SessionMemoryTransport(), provider, provider, sessionmemoryapp.Config{
		Enabled:          true,
		MaxAttempts:      2,
		RetryBaseDelay:   10 * time.Millisecond,
		RetryMaxDelay:    10 * time.Millisecond,
		ProgressInterval: 5 * time.Millisecond,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewCapabilityWorker() error = %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("worker.Start() error = %v", err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()

	waitForSessionMemoryWorker(t, 3*time.Second, func() bool {
		stats, statsErr := h.Bus.SessionMemoryStats(context.Background())
		return statsErr == nil && stats.Messages == 0 && stats.Pending == 0 && stats.Acknowledging == 0
	})
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	if len(gotCalls) != 3 || gotCalls[0] != "turn" || gotCalls[1] != "turn" || gotCalls[2] != "boundary" {
		t.Fatalf("provider call order = %v, want [turn retry boundary]", gotCalls)
	}
}

func TestBus_SessionMemoryWorkerPublishesDLQBeforeTerm(t *testing.T) {
	h := StartTestRuntime(t, baldaexecution.Config{
		Commands: baldaexecution.CommandConfig{FetchWait: "50ms"},
		Memory:   baldaexecution.SessionMemoryConfig{Enabled: true},
	})
	export := testSessionMemoryTurn(t, "worker-dlq")
	if _, err := h.Bus.PublishSessionMemory(context.Background(), export); err != nil {
		t.Fatalf("PublishSessionMemory() error = %v", err)
	}
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(context.Context, sessionmemory.Turn) error {
			return sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "scope rejected", errors.New("secret provider body"))
		},
	}
	worker, err := sessionmemoryapp.NewCapabilityWorker(h.Bus.SessionMemoryTransport(), provider, provider, sessionmemoryapp.Config{
		Enabled: true,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewCapabilityWorker() error = %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("worker.Start() error = %v", err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()

	msg := fetchSessionMemoryDLQ(t, h.Bus)
	if got := msg.Headers().Get("Balda-DLQ-Source-Stream"); got != baldaexecution.DefaultSessionMemoryStream {
		t.Fatalf("DLQ source stream = %q, want %q", got, baldaexecution.DefaultSessionMemoryStream)
	}
	if got := msg.Headers().Get("Balda-DLQ-Error-Code"); got != string(sessionmemory.CodeScopeViolation) {
		t.Fatalf("DLQ error code = %q, want %q", got, sessionmemory.CodeScopeViolation)
	}
	if strings.Contains(string(msg.Data()), "secret provider body") {
		t.Fatalf("DLQ payload leaked provider error body: %q", string(msg.Data()))
	}
	waitForSessionMemoryWorker(t, 3*time.Second, func() bool {
		stats, statsErr := h.Bus.SessionMemoryStats(context.Background())
		return statsErr == nil && stats.Messages == 0 && stats.Pending == 0 && stats.Acknowledging == 0
	})
}

func TestBus_SessionMemoryWorkerCancellationLeavesExportForRedelivery(t *testing.T) {
	stateDir := t.TempDir()
	params := Params{
		Config:    baldaeventbus.Config{Embedded: true},
		Execution: baldaexecution.Config{Memory: baldaexecution.SessionMemoryConfig{Enabled: true, AckWait: "50ms", FetchWait: "50ms"}},
		StateDir:  stateDir,
		Logger:    zerolog.Nop(),
	}
	firstBus, err := newStartedBus(t, params)
	if err != nil {
		t.Fatalf("first NewBus() error = %v", err)
	}
	export := testSessionMemoryTurn(t, "worker-redelivery")
	if _, err := firstBus.PublishSessionMemory(context.Background(), export); err != nil {
		t.Fatalf("PublishSessionMemory() error = %v", err)
	}
	started := make(chan struct{})
	provider := &sessionmemorytest.Provider{
		IngestTurnFunc: func(ctx context.Context, _ sessionmemory.Turn) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker, err := sessionmemoryapp.NewCapabilityWorker(firstBus.SessionMemoryTransport(), provider, provider, sessionmemoryapp.Config{
		Enabled:         true,
		ShutdownTimeout: time.Second,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("first NewCapabilityWorker() error = %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("first worker.Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first provider did not start")
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("first worker.Stop() error = %v", err)
	}
	if err := firstBus.Drain(context.Background()); err != nil {
		t.Fatalf("first bus Drain() error = %v", err)
	}

	secondBus, err := newStartedBus(t, params)
	if err != nil {
		t.Fatalf("second NewBus() error = %v", err)
	}
	defer func() { _ = secondBus.Drain(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	recovered, err := secondBus.FetchSessionMemory(context.Background())
	if err != nil {
		t.Fatalf("FetchSessionMemory() after cancellation error = %v", err)
	}
	decoded, err := recovered.Export()
	if err != nil {
		t.Fatalf("recovered Export() error = %v", err)
	}
	if decoded.ExportID() != export.ExportID() {
		t.Fatalf("recovered export id = %q, want %q", decoded.ExportID(), export.ExportID())
	}
	if err := recovered.Ack(context.Background()); err != nil {
		t.Fatalf("recovered Ack() error = %v", err)
	}
}

func fetchSessionMemoryDLQ(t *testing.T, bus *Bus) jetstream.Msg {
	t.Helper()
	consumer, err := bus.js.CreateOrUpdateConsumer(context.Background(), bus.cfg.Execution.DLQ.Stream, jetstream.ConsumerConfig{
		Durable:       "SESSION_MEMORY_DLQ_TEST_" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")),
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: baldaexecution.SubjectDLQCommand,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer(DLQ) error = %v", err)
	}
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	if err != nil {
		t.Fatalf("Fetch(DLQ) error = %v", err)
	}
	select {
	case msg, ok := <-batch.Messages():
		if !ok {
			t.Fatalf("Fetch(DLQ) closed without message: %v", batch.Error())
		}
		if err := msg.Ack(); err != nil {
			t.Fatalf("Ack(DLQ) error = %v", err)
		}
		return msg
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for session-memory DLQ")
		return nil
	}
}

func waitForSessionMemoryWorker(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session-memory worker condition was not satisfied before timeout")
}
