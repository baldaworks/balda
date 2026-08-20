package natsbus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	baldaeventbus "github.com/baldaworks/balda/internal/apps/balda/eventbus"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/baldaworks/balda/sessionmemory"
	"github.com/rs/zerolog"
	"go.uber.org/fx/fxtest"
)

func TestBus_SessionMemoryTopologyPublishFetchAndAck(t *testing.T) {
	h := StartTestRuntime(t, enabledSessionMemoryExecutionConfig())
	bus := h.Bus

	stream, err := bus.js.Stream(context.Background(), baldaexecution.DefaultSessionMemoryStream)
	if err != nil {
		t.Fatalf("Stream(session memory) error = %v", err)
	}
	streamInfo, err := stream.Info(context.Background())
	if err != nil {
		t.Fatalf("Stream.Info(session memory) error = %v", err)
	}
	if streamInfo.Config.Retention != jetstream.WorkQueuePolicy {
		t.Fatalf("session-memory retention = %v, want %v", streamInfo.Config.Retention, jetstream.WorkQueuePolicy)
	}
	if len(streamInfo.Config.Subjects) != 1 || streamInfo.Config.Subjects[0] != sessionmemorycmd.SubjectAll {
		t.Fatalf("session-memory subjects = %#v, want [%q]", streamInfo.Config.Subjects, sessionmemorycmd.SubjectAll)
	}
	consumerInfo, err := bus.memoryConsumer.Info(context.Background())
	if err != nil {
		t.Fatalf("memory consumer Info() error = %v", err)
	}
	if consumerInfo.Config.MaxAckPending != 1 || consumerInfo.Config.AckPolicy != jetstream.AckExplicitPolicy {
		t.Fatalf("memory consumer config = %+v", consumerInfo.Config)
	}

	export := testSessionMemoryTurn(t, "turn-topology")
	first, err := bus.PublishSessionMemory(context.Background(), export)
	if err != nil {
		t.Fatalf("PublishSessionMemory(first) error = %v", err)
	}
	second, err := bus.PublishSessionMemory(context.Background(), export)
	if err != nil {
		t.Fatalf("PublishSessionMemory(duplicate) error = %v", err)
	}
	if first.ExportID != export.ExportID() || second.ExportID != export.ExportID() || !second.Duplicate {
		t.Fatalf("publish receipts = %+v / %+v", first, second)
	}
	stats, err := bus.SessionMemoryStats(context.Background())
	if err != nil {
		t.Fatalf("SessionMemoryStats(before fetch) error = %v", err)
	}
	if stats.Messages != 1 || stats.Pending != 1 || stats.Acknowledging != 0 || stats.OldestPendingAt.IsZero() {
		t.Fatalf("stats before fetch = %+v", stats)
	}

	delivery, err := bus.FetchSessionMemory(context.Background())
	if err != nil {
		t.Fatalf("FetchSessionMemory() error = %v", err)
	}
	metadata, err := delivery.Metadata()
	if err != nil {
		t.Fatalf("delivery.Metadata() error = %v", err)
	}
	if metadata.StreamSequence != first.Sequence || metadata.DeliveryCount != 1 {
		t.Fatalf("delivery metadata = %+v, want stream sequence %d and first delivery", metadata, first.Sequence)
	}
	decoded, err := delivery.Export()
	if err != nil {
		t.Fatalf("delivery.Export() error = %v", err)
	}
	if decoded.ExportID() != export.ExportID() || delivery.Subject() != sessionmemorycmd.SubjectTurn {
		t.Fatalf("delivery = %q on %q", decoded.ExportID(), delivery.Subject())
	}
	stats, err = bus.SessionMemoryStats(context.Background())
	if err != nil {
		t.Fatalf("SessionMemoryStats(after fetch) error = %v", err)
	}
	if stats.Pending != 0 || stats.Acknowledging != 1 {
		t.Fatalf("stats after fetch = %+v", stats)
	}
	if err := delivery.Ack(context.Background()); err != nil {
		t.Fatalf("delivery.Ack() error = %v", err)
	}
	waitForSessionMemoryEmpty(t, bus)
}

func TestBus_SessionMemoryRestartPreservesPendingExport(t *testing.T) {
	stateDir := t.TempDir()
	params := Params{
		LC:        fxtest.NewLifecycle(t),
		Config:    baldaeventbus.Config{Embedded: true},
		Execution: enabledSessionMemoryExecutionConfig(),
		StateDir:  stateDir,
		Logger:    zerolog.Nop(),
	}
	firstBus, err := newStartedBus(t, params)
	if err != nil {
		t.Fatalf("first NewBus() error = %v", err)
	}
	export := testSessionMemoryTurn(t, "turn-restart")
	if _, err := firstBus.PublishSessionMemory(context.Background(), export); err != nil {
		t.Fatalf("PublishSessionMemory() error = %v", err)
	}
	if err := firstBus.Drain(context.Background()); err != nil {
		t.Fatalf("first Drain() error = %v", err)
	}

	secondBus, err := newStartedBus(t, params)
	if err != nil {
		t.Fatalf("second NewBus() error = %v", err)
	}
	defer func() { _ = secondBus.Drain(context.Background()) }()
	stats, err := secondBus.SessionMemoryStats(context.Background())
	if err != nil {
		t.Fatalf("SessionMemoryStats(after restart) error = %v", err)
	}
	if stats.Messages != 1 || stats.Pending != 1 {
		t.Fatalf("stats after restart = %+v", stats)
	}
	delivery, err := secondBus.FetchSessionMemory(context.Background())
	if err != nil {
		t.Fatalf("FetchSessionMemory(after restart) error = %v", err)
	}
	decoded, err := delivery.Export()
	if err != nil {
		t.Fatalf("delivery.Export(after restart) error = %v", err)
	}
	if decoded.ExportID() != export.ExportID() {
		t.Fatalf("recovered export id = %q, want %q", decoded.ExportID(), export.ExportID())
	}
	if err := delivery.Ack(context.Background()); err != nil {
		t.Fatalf("delivery.Ack(after restart) error = %v", err)
	}
}

func TestBus_SessionMemoryPublishReportsDiscardNewPressure(t *testing.T) {
	bus, err := NewBus(Params{
		LC:        fxtest.NewLifecycle(t),
		Config:    baldaeventbus.Config{Embedded: true},
		Execution: enabledSessionMemoryExecutionConfig(),
		StateDir:  t.TempDir(),
		Logger:    zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	bus.cfg.Memory.MaxBytes = 1
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = bus.Drain(context.Background()) }()
	_, err = bus.PublishSessionMemory(context.Background(), testSessionMemoryTurn(t, "turn-pressure"))
	if !errors.Is(err, ErrSessionMemoryQueueFull) {
		t.Fatalf("PublishSessionMemory() error = %v, want ErrSessionMemoryQueueFull", err)
	}
}

func TestBus_FetchSessionMemoryHonorsCanceledContext(t *testing.T) {
	h := StartTestRuntime(t, enabledSessionMemoryExecutionConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Bus.FetchSessionMemory(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchSessionMemory(canceled) error = %v, want context.Canceled", err)
	}
}

func TestBus_SessionMemoryStatsRequiresConsumer(t *testing.T) {
	h := StartTestRuntime(t, enabledSessionMemoryExecutionConfig())
	h.Bus.memoryConsumer = nil
	_, err := h.Bus.SessionMemoryStats(context.Background())
	if !errors.Is(err, ErrSessionMemoryConsumerUnavailable) {
		t.Fatalf("SessionMemoryStats() error = %v, want ErrSessionMemoryConsumerUnavailable", err)
	}
}

func TestBus_DisabledSessionMemoryHasNoStreamOrConsumer(t *testing.T) {
	h := StartTestRuntime(t, baldaexecution.Config{})
	if h.Bus.memoryConsumer != nil {
		t.Fatal("disabled session-memory created a consumer")
	}
	if _, err := h.Bus.js.Stream(context.Background(), baldaexecution.DefaultSessionMemoryStream); err == nil {
		t.Fatal("disabled session-memory created a stream")
	}
	export := testSessionMemoryTurn(t, "disabled")
	if _, err := h.Bus.PublishSessionMemory(context.Background(), export); !errors.Is(err, ErrSessionMemoryDisabled) {
		t.Fatalf("PublishSessionMemory(disabled) error = %v, want ErrSessionMemoryDisabled", err)
	}
	if _, err := h.Bus.FetchSessionMemory(context.Background()); !errors.Is(err, ErrSessionMemoryDisabled) {
		t.Fatalf("FetchSessionMemory(disabled) error = %v, want ErrSessionMemoryDisabled", err)
	}
	if _, err := h.Bus.SessionMemoryStats(context.Background()); !errors.Is(err, ErrSessionMemoryDisabled) {
		t.Fatalf("SessionMemoryStats(disabled) error = %v, want ErrSessionMemoryDisabled", err)
	}
}

func TestResolveConfigRejectsSessionMemoryNameCollisions(t *testing.T) {
	t.Parallel()

	_, err := resolveConfig(baldaeventbus.Config{Embedded: true}, baldaexecution.Config{
		Memory: baldaexecution.SessionMemoryConfig{Enabled: true, Stream: baldaexecution.DefaultCommandStream},
	}, t.TempDir())
	if err == nil {
		t.Fatal("resolveConfig() error = nil, want stream collision error")
	}
}

func TestResolveConfigIgnoresDisabledSessionMemoryNameCollisions(t *testing.T) {
	t.Parallel()

	_, err := resolveConfig(baldaeventbus.Config{Embedded: true}, baldaexecution.Config{
		Memory: baldaexecution.SessionMemoryConfig{Stream: baldaexecution.DefaultCommandStream},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("resolveConfig(disabled collision) error = %v, want nil", err)
	}
}

func TestResolveConfigIgnoresDisabledSessionMemoryMalformedOptionalValues(t *testing.T) {
	t.Parallel()
	const malformedDuration = "not-a-duration"
	cfg := enabledSessionMemoryExecutionConfig()
	cfg.Memory.Enabled = false
	cfg.Memory.AckWait = malformedDuration
	cfg.Memory.FetchWait = malformedDuration
	cfg.Memory.PublishTimeout = malformedDuration
	cfg.Memory.MaxAge = malformedDuration
	cfg.Memory.MaxBytes = "not-bytes"
	cfg.Memory.MaxMsgSize = "not-bytes"
	resolved, err := resolveConfig(baldaeventbus.Config{Embedded: true}, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("resolveConfig() error = %v, want nil while disabled", err)
	}
	if resolved.Execution.Memory.Enabled {
		t.Fatal("resolved disabled memory unexpectedly enabled")
	}
}

func TestResolveConfigUsesSessionMemoryRetentionLimits(t *testing.T) {
	t.Parallel()
	cfg := enabledSessionMemoryExecutionConfig()
	cfg.Memory.MaxAge = "2d"
	cfg.Memory.MaxBytes = "32MiB"
	cfg.Memory.MaxMsgSize = "2MiB"
	resolved, err := resolveConfig(baldaeventbus.Config{Embedded: true}, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	if resolved.Memory.MaxAge != 2*24*time.Hour || resolved.Memory.MaxBytes != 32*1024*1024 || resolved.Memory.MaxMsgSize != 2*1024*1024 {
		t.Fatalf("memory stream spec = %+v, want configured retention", resolved.Memory)
	}
}

func TestResolveConfigRejectsNonPositiveEnabledSessionMemoryLimits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*baldaexecution.Config)
	}{
		{name: "max age", mutate: func(cfg *baldaexecution.Config) { cfg.Memory.MaxAge = "0s" }},
		{name: "max bytes", mutate: func(cfg *baldaexecution.Config) { cfg.Memory.MaxBytes = "0" }},
		{name: "max message size", mutate: func(cfg *baldaexecution.Config) { cfg.Memory.MaxMsgSize = "0" }},
		{name: "ack wait", mutate: func(cfg *baldaexecution.Config) { cfg.Memory.AckWait = "0s" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := enabledSessionMemoryExecutionConfig()
			test.mutate(&cfg)
			if _, err := resolveConfig(baldaeventbus.Config{Embedded: true}, cfg, t.TempDir()); err == nil {
				t.Fatal("resolveConfig() error = nil, want non-positive setting rejection")
			}
		})
	}
}

func testSessionMemoryTurn(t *testing.T, sourceID string) sessionmemorycmd.Export {
	t.Helper()
	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "tg-1-0", AgentSessionID: "tg-1-0"}
	turn, err := sessionmemory.NewTurn(scope, session, sourceID, time.Now().UTC(), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("sessionmemorycmd.NewTurn() error = %v", err)
	}
	return export
}

func waitForSessionMemoryEmpty(t *testing.T, bus *Bus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		stats, err := bus.SessionMemoryStats(ctx)
		if err != nil {
			t.Fatalf("SessionMemoryStats(wait) error = %v", err)
		}
		if stats.Messages == 0 && stats.Pending == 0 && stats.Acknowledging == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("session-memory stats did not empty: %+v", stats)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func enabledSessionMemoryExecutionConfig() baldaexecution.Config {
	return baldaexecution.Config{
		Memory: baldaexecution.SessionMemoryConfig{Enabled: true},
	}
}
