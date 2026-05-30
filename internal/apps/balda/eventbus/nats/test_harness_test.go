package natsbus

import (
	"context"
	"testing"

	baldaeventbus "github.com/normahq/balda/internal/apps/balda/eventbus"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/rs/zerolog"
	"go.uber.org/fx/fxtest"
)

// TestJetStreamHarness provides a reusable embedded JetStream command bus for tests.
type TestJetStreamHarness struct {
	Bus *Bus
}

// StartTestJetStream creates an embedded JetStream bus backed by a temp store dir.
// It ensures required streams/consumers are available through NewActorRuntimeTransport startup.
func StartTestJetStream(t *testing.T, swarmCfg swarm.Config) *TestJetStreamHarness {
	t.Helper()
	busRaw, err := NewActorRuntimeTransport(Params{
		LC:         fxtest.NewLifecycle(t),
		Config:     baldaeventbus.Config{Embedded: true, JetStream: true},
		Swarm:      swarmCfg,
		WorkingDir: t.TempDir(),
		Logger:     zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("StartTestJetStream() NewActorRuntimeTransport error = %v", err)
	}
	bus, ok := busRaw.(*Bus)
	if !ok {
		t.Fatalf("StartTestJetStream() bus type = %T, want *Bus", busRaw)
	}
	t.Cleanup(func() { _ = bus.Drain(context.Background()) })
	return &TestJetStreamHarness{Bus: bus}
}

// Dispatch is a test command publisher helper for fixtures/scenarios.
func (h *TestJetStreamHarness) Dispatch(t *testing.T, env swarm.Envelope) *swarm.DispatchReceipt {
	t.Helper()
	ack, err := h.Bus.Dispatch(context.Background(), env)
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	return ack
}
