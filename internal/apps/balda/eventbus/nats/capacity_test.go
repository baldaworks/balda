package natsbus

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/eventbus"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/rs/zerolog"
	"go.uber.org/fx/fxtest"
)

func TestStreamBudgetsReserveCommandCapacity(t *testing.T) {
	cfg, err := resolveConfig(eventbus.Config{BoundedStreams: true, MaxStore: "2gb"}, baldaexecution.Config{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	used := int64(0)
	for _, spec := range []streamSpec{cfg.Commands, cfg.Events, cfg.DLQ, cfg.Memory} {
		if spec.MaxBytes <= 0 {
			t.Fatalf("unbounded stream: %+v", spec)
		}
		used += spec.MaxBytes
	}
	if used >= cfg.MaxStore || cfg.Events.MaxBytes >= cfg.Commands.MaxBytes {
		t.Fatalf("event history can exhaust command reserve: %+v", cfg)
	}
}

func TestEventHistoryCannotConsumeCommandStorage(t *testing.T) {
	bus, err := newStartedBus(t, Params{LC: fxtest.NewLifecycle(t), Config: eventbus.Config{Embedded: true, BoundedStreams: true, MaxStore: "8mb"}, Execution: baldaexecution.Config{}, StateDir: t.TempDir(), Logger: zerolog.Nop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bus.Drain(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if _, err := bus.js.Publish(ctx, baldaexecution.SubjectEventJobUpdated, []byte(strings.Repeat("x", 64<<10))); err != nil {
			t.Fatalf("history write %d: %v", i, err)
		}
	}
	stream, err := bus.js.Stream(ctx, baldaexecution.DefaultEventStream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Bytes > uint64(bus.cfg.Events.MaxBytes) || info.State.FirstSeq <= 1 {
		t.Fatalf("history was not bounded: %+v", info.State)
	}
	for i := 0; i < 3; i++ {
		if _, err := bus.Dispatch(ctx, commandTestEnvelope(fmt.Sprintf("reserved-command-%d", i))); err != nil {
			t.Fatalf("history blocked new command: %v", err)
		}
	}
}

func TestExistingHistorySurvivesDefaultRestartAndOptInRefusal(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	params := Params{LC: fxtest.NewLifecycle(t), Config: eventbus.Config{Embedded: true, MaxStore: "8mb"}, Execution: baldaexecution.Config{}, StateDir: directory, Logger: zerolog.Nop()}
	first, err := newStartedBus(t, params)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if _, err := first.js.Publish(ctx, baldaexecution.SubjectEventJobUpdated, []byte(strings.Repeat("x", 64<<10))); err != nil {
			t.Fatal(err)
		}
	}
	stream, err := first.js.Stream(ctx, baldaexecution.DefaultEventStream)
	if err != nil {
		t.Fatal(err)
	}
	before, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.State.Bytes <= 2<<20 {
		t.Fatal("fixture does not exceed the opt-in event cap")
	}
	if err := first.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := newStartedBus(t, params)
	if err != nil {
		t.Fatalf("default upgrade rejected existing history: %v", err)
	}
	stream, err = second.js.Stream(ctx, baldaexecution.DefaultEventStream)
	if err != nil {
		t.Fatal(err)
	}
	after, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Msgs != before.State.Msgs || after.Config.MaxBytes != -1 {
		t.Fatalf("default restart changed history: %+v", after)
	}
	if err := second.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	params.Config.BoundedStreams = true
	if bus, err := newStartedBus(t, params); err == nil {
		_ = bus.Drain(ctx)
		t.Fatal("explicit activation silently reduced occupied stream")
	}
	params.Config.BoundedStreams = false
	restored, err := newStartedBus(t, params)
	if err != nil {
		t.Fatalf("default startup after rejected opt-in: %v", err)
	}
	t.Cleanup(func() { _ = restored.Drain(ctx) })
	stream, err = restored.js.Stream(ctx, baldaexecution.DefaultEventStream)
	if err != nil {
		t.Fatal(err)
	}
	final, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.State.Msgs != before.State.Msgs {
		t.Fatalf("opt-in refusal deleted messages: %d -> %d", before.State.Msgs, final.State.Msgs)
	}
}
