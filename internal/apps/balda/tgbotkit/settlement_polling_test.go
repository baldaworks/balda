package tgbotkit

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/eventemitter"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/updatepoller/offsetstore"
)

func TestSettlementOffsetStorePersistsAcceptedAndTerminalUpdates(t *testing.T) {
	t.Parallel()

	backing := offsetstore.NewInMemoryOffsetStore(0)
	gate := newPollingSettlementGate(zerolog.Nop())
	store := settlementOffsetStore{store: backing, gate: gate}

	gate.begin(10)
	gate.finish(10)
	if err := store.Save(context.Background(), 11); err != nil {
		t.Fatalf("Save(accepted) error = %v", err)
	}

	gate.begin(11)
	gate.recordError(actorlayer.PolicyError(errTestTerminal))
	gate.finish(11)
	if err := store.Save(context.Background(), 12); err != nil {
		t.Fatalf("Save(terminal) error = %v", err)
	}

	got, err := backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 12 {
		t.Fatalf("offset = %d, want 12", got)
	}
}

func TestSettlementOffsetStoreReplaysRetryWithoutAdvancingOffset(t *testing.T) {
	t.Parallel()

	backing := offsetstore.NewInMemoryOffsetStore(0)
	gate := newPollingSettlementGate(zerolog.Nop())
	store := settlementOffsetStore{store: backing, gate: gate}

	gate.begin(20)
	gate.recordError(actorlayer.TransientError(errTestRetry))
	gate.finish(20)
	if err := store.Save(context.Background(), 21); err != nil {
		t.Fatalf("Save(retry) error = %v", err)
	}
	got, err := backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("offset after retry = %d, want 0", got)
	}

	// Telegram re-delivers the same update ID after the unchanged offset.
	gate.begin(20)
	gate.finish(20)
	if err := store.Save(context.Background(), 21); err != nil {
		t.Fatalf("Save(replayed accepted) error = %v", err)
	}
	got, err = backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(after replay) error = %v", err)
	}
	if got != 21 {
		t.Fatalf("offset after replay = %d, want 21", got)
	}
}

func TestSettlementOffsetStoreKeepsWholeBatchReplayable(t *testing.T) {
	t.Parallel()

	backing := offsetstore.NewInMemoryOffsetStore(0)
	gate := newPollingSettlementGate(zerolog.Nop())
	store := settlementOffsetStore{store: backing, gate: gate}

	gate.begin(70)
	gate.finish(70)
	gate.begin(71)
	gate.recordError(actorlayer.TransientError(errTestRetry))
	gate.finish(71)
	if err := store.Save(context.Background(), 72); err != nil {
		t.Fatalf("Save(batch retry) error = %v", err)
	}
	got, err := backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("offset after batch retry = %d, want 0", got)
	}

	gate.begin(70)
	gate.finish(70)
	gate.begin(71)
	gate.finish(71)
	if err := store.Save(context.Background(), 72); err != nil {
		t.Fatalf("Save(batch replay) error = %v", err)
	}
	got, err = backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(after batch replay) error = %v", err)
	}
	if got != 72 {
		t.Fatalf("offset after batch replay = %d, want 72", got)
	}
}

func TestSettlementOffsetStoreLeavesUnsettledUpdateReplayableOnShutdown(t *testing.T) {
	t.Parallel()

	backing := offsetstore.NewInMemoryOffsetStore(0)
	gate := newPollingSettlementGate(zerolog.Nop())
	store := settlementOffsetStore{store: backing, gate: gate}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if err := store.Save(ctx, 31); err == nil {
		t.Fatal("Save(unsettled) error = nil, want context cancellation")
	}
	got, err := backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("offset after shutdown = %d, want 0", got)
	}
}

func TestSettlementGateAdvancesAfterBoundedRetryLimit(t *testing.T) {
	t.Parallel()

	backing := offsetstore.NewInMemoryOffsetStore(0)
	gate := newPollingSettlementGate(zerolog.Nop())
	store := settlementOffsetStore{store: backing, gate: gate}
	for attempt := 0; attempt < maxPollingRetryAttempts; attempt++ {
		gate.begin(40)
		gate.recordError(actorlayer.TransientError(errTestRetry))
		gate.finish(40)
		if err := store.Save(context.Background(), 41); err != nil {
			t.Fatalf("Save(attempt %d) error = %v", attempt+1, err)
		}
	}
	got, err := backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 41 {
		t.Fatalf("offset after retry limit = %d, want 41", got)
	}
}

func TestSettlementEventEmitterCapturesRetryableHandlerError(t *testing.T) {
	t.Parallel()

	backing := offsetstore.NewInMemoryOffsetStore(0)
	gate := newPollingSettlementGate(zerolog.Nop())
	store := settlementOffsetStore{store: backing, gate: gate}
	base, err := eventemitter.NewSync(eventemitter.NewOptions(
		eventemitter.WithStopOnError(false),
		eventemitter.WithErrorHandler(func(_ string, err error) { gate.recordError(err) }),
	))
	if err != nil {
		t.Fatalf("NewSync() error = %v", err)
	}
	emitter := settlementEventEmitter{EventEmitter: base, gate: gate}
	emitter.AddListener(events.OnUpdate, eventemitter.ListenerFunc(func(context.Context, any) error {
		return actorlayer.TransientError(errTestRetry)
	}))
	emitter.Emit(context.Background(), events.OnUpdate, &events.UpdateEvent{
		Update: &client.Update{UpdateId: 60},
	})
	if err := store.Save(context.Background(), 61); err != nil {
		t.Fatalf("Save(retry handler) error = %v", err)
	}
	got, err := backing.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("offset after handler retry = %d, want 0", got)
	}
}

var (
	errTestRetry    = testError("retry")
	errTestTerminal = testError("terminal")
)

type testError string

func (e testError) Error() string { return string(e) }
