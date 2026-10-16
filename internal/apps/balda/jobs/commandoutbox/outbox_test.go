package commandoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type memoryStore struct {
	mu            sync.Mutex
	values        map[string]string
	reject        bool
	rejectDelete  bool
	rejectPending bool
}

func (s *memoryStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok, nil
}
func (s *memoryStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject || (s.rejectPending && strings.HasPrefix(key, ingressOutboxPrefix)) {
		return errors.New("local store unavailable")
	}
	s.values[key] = value
	return nil
}
func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejectDelete && strings.HasPrefix(key, ingressOutboxPrefix) {
		return errors.New("interrupted pending deletion")
	}
	delete(s.values, key)
	return nil
}
func (s *memoryStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.values {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

type recordingDispatcher struct {
	err       error
	envelopes []actorlayer.Envelope
	entered   chan struct{}
	release   chan struct{}
}

func (d *recordingDispatcher) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	if d.entered != nil {
		close(d.entered)
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	return &actortransport.DispatchReceipt{Stream: "commands", MsgID: env.ID}, nil
}
func envelope(id string) actorlayer.Envelope {
	return actorlayer.Envelope{ID: id, Namespace: "balda", Kind: "turn", From: actorlayer.SystemAddress("ingress"), To: actorlayer.SystemAddress("session"), DedupeKey: id, Payload: actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: []byte(`{"text":"original task"}`)}}
}
func fixture(t *testing.T) (*Dispatcher, *memoryStore, *recordingDispatcher) {
	t.Helper()
	s := &memoryStore{values: map[string]string{}}
	bus := &recordingDispatcher{}
	d, err := NewDispatcher(s, bus, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	return d, s, bus
}
func flush(t *testing.T, d *Dispatcher) {
	t.Helper()
	if err := d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDurableAdmissionStaysAcceptedAcrossBusOutageAndRestart(t *testing.T) {
	ctx := context.Background()
	d, s, bus := fixture(t)
	bus.err = errors.New("NATS unavailable")
	now := time.Now()
	d.now = func() time.Time { return now }
	env := envelope("original")
	receipt, err := d.Dispatch(ctx, env)
	if err != nil || receipt == nil || receipt.MsgID != env.ID {
		t.Fatalf("durable admission: %+v %v", receipt, err)
	}
	flush(t, d)
	duplicate := env
	duplicate.ID = "new-envelope-for-same-post"
	duplicate.Payload.Data = []byte(`{"text":"must not replace"}`)
	receipt, err = d.Dispatch(ctx, duplicate)
	if err != nil || receipt == nil || !receipt.Duplicate || receipt.MsgID != env.ID {
		t.Fatalf("pending duplicate rejected: %+v %v", receipt, err)
	}
	bus.err = nil
	restarted, err := NewDispatcher(s, bus, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now.Add(time.Minute) }
	flush(t, restarted)
	if len(bus.envelopes) != 2 || bus.envelopes[1].ID != env.ID || bus.envelopes[1].Payload.String() != env.Payload.String() {
		t.Fatalf("original command lost: %+v", bus.envelopes)
	}
	receipt, err = restarted.Dispatch(ctx, duplicate)
	if err != nil || receipt == nil || !receipt.Duplicate {
		t.Fatalf("published duplicate: %+v %v", receipt, err)
	}
	flush(t, restarted)
	if len(bus.envelopes) != 2 {
		t.Fatal("acknowledged command dispatched again")
	}
}

func TestRejectedLocalAdmissionNeverExecutesLater(t *testing.T) {
	d, s, bus := fixture(t)
	s.reject = true
	if receipt, err := d.Dispatch(context.Background(), envelope("rejected")); err == nil || receipt != nil {
		t.Fatalf("failed persistence acknowledged: %+v %v", receipt, err)
	}
	s.reject = false
	flush(t, d)
	if len(bus.envelopes) != 0 {
		t.Fatal("rejected command executed")
	}
}

func TestBlockedPublisherDoesNotBlockNewDurableAdmission(t *testing.T) {
	d, _, bus := fixture(t)
	bus.entered = make(chan struct{})
	bus.release = make(chan struct{})
	defer close(bus.release)
	if _, err := d.Dispatch(context.Background(), envelope("first")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Flush(context.Background()) }()
	<-bus.entered
	admitted := make(chan error, 1)
	go func() { _, err := d.Dispatch(context.Background(), envelope("second")); admitted <- err }()
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("NATS publish blocked local admission")
	}
	// The deferred release lets the publisher stop after the assertion.
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("publisher did not stop")
		}
	})
}

func TestSessionOrderSurvivesPublicationBackoff(t *testing.T) {
	d, _, bus := fixture(t)
	now := time.Now()
	d.now = func() time.Time { return now }
	bus.err = errors.New("full")
	for _, id := range []string{"first", "second"} {
		if _, err := d.Dispatch(context.Background(), envelope(id)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Millisecond)
	}
	flush(t, d)
	if len(bus.envelopes) != 1 || bus.envelopes[0].ID != "first" {
		t.Fatalf("order while blocked: %+v", bus.envelopes)
	}
	bus.err = nil
	now = now.Add(time.Minute)
	flush(t, d)
	if len(bus.envelopes) != 3 || bus.envelopes[1].ID != "first" || bus.envelopes[2].ID != "second" {
		t.Fatalf("recovery order: %+v", bus.envelopes)
	}
}

func TestRetentionDeletesTerminalRecordsAndPreservesFreshPending(t *testing.T) {
	ctx := context.Background()
	d, s, bus := fixture(t)
	now := time.Now()
	d.now = func() time.Time { return now }
	if _, err := d.Dispatch(ctx, envelope("published")); err != nil {
		t.Fatal(err)
	}
	flush(t, d)
	expired := envelope("expired")
	expired.ExpiresAt = now.Add(time.Minute)
	if _, err := d.Dispatch(ctx, expired); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	flush(t, d)
	if len(bus.envelopes) != 1 {
		t.Fatal("expired command was executed")
	}
	for _, prefix := range []string{ingressReceiptPrefix, "command-outbox/failed/"} {
		keys, _ := s.List(ctx, prefix)
		if len(keys) != 1 {
			t.Fatalf("terminal record missing: %s %v", prefix, keys)
		}
		raw, _, _ := s.Get(ctx, keys[0])
		if strings.Contains(raw, "original task") {
			t.Fatal("terminal retention kept command payload")
		}
	}
	now = now.Add(7*24*time.Hour + time.Second)
	if _, err := d.Dispatch(ctx, envelope("fresh-pending")); err != nil {
		t.Fatal(err)
	}
	if err := d.collect(ctx); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{ingressReceiptPrefix, "command-outbox/failed/"} {
		keys, _ := s.List(ctx, prefix)
		if len(keys) != 0 {
			t.Fatalf("expired terminal records retained: %v", keys)
		}
	}
	keys, _ := s.List(ctx, ingressOutboxPrefix)
	if len(keys) != 1 {
		t.Fatalf("pending command removed by GC: %v", keys)
	}
	flush(t, d)
	if len(bus.envelopes) != 2 || bus.envelopes[1].ID != "fresh-pending" {
		t.Fatal("fresh pending command lost")
	}
}

func TestRetentionScanContinuesAcrossBatches(t *testing.T) {
	d, s, _ := fixture(t)
	ctx := context.Background()
	for i := 0; i < 150; i++ {
		record := ingressRecord{RetainUntil: time.Now().Add(-time.Hour)}
		raw, _ := json.Marshal(record)
		key := ingressReceiptPrefix + strings.Repeat("a", i+1)
		if err := s.Set(ctx, key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := d.collect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := s.List(ctx, ingressReceiptPrefix)
	if len(keys) != 0 {
		t.Fatalf("GC cursor stalled: %d records", len(keys))
	}
}

func TestRestartAfterTerminalSaveNeverRepublishes(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "expired"}[expiry], func(t *testing.T) {
			d, s, bus := fixture(t)
			env := envelope("crash-window")
			if expiry {
				env.ExpiresAt = time.Now().Add(-time.Second)
			}
			if _, err := d.Dispatch(context.Background(), env); err != nil {
				t.Fatal(err)
			}
			s.rejectDelete = true
			flush(t, d)
			before := len(bus.envelopes)
			s.rejectDelete = false
			restarted, err := NewDispatcher(s, bus, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			flush(t, restarted)
			if len(bus.envelopes) != before {
				t.Fatal("terminal command republished after restart")
			}
			keys, _ := s.List(context.Background(), ingressOutboxPrefix)
			if len(keys) != 0 {
				t.Fatal("interrupted pending deletion not reconciled")
			}
		})
	}
}

func TestAdmissionOrderSurvivesWallClockRollbackAndRestart(t *testing.T) {
	d, store, bus := fixture(t)
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	for _, id := range []string{"first", "second"} {
		if _, err := d.Dispatch(t.Context(), envelope(id)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(-10 * time.Second)
	}
	restarted, err := NewDispatcher(store, bus, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	if _, err := restarted.Dispatch(t.Context(), envelope("third")); err != nil {
		t.Fatal(err)
	}
	flush(t, restarted)
	if len(bus.envelopes) != 3 {
		t.Fatalf("published %d envelopes", len(bus.envelopes))
	}
	for i, want := range []string{"first", "second", "third"} {
		if bus.envelopes[i].ID != want {
			t.Errorf("publication %d=%s, want %s", i, bus.envelopes[i].ID, want)
		}
	}
}

func TestFailedPendingWriteLeavesSafeSequenceGap(t *testing.T) {
	d, store, bus := fixture(t)
	store.rejectPending = true
	if receipt, err := d.Dispatch(t.Context(), envelope("rejected")); err == nil || receipt != nil {
		t.Fatalf("failed row accepted: %v %v", receipt, err)
	}
	if raw, _, _ := store.Get(t.Context(), ingressSequenceKey); raw != "1" {
		t.Fatalf("watermark=%q, want 1", raw)
	}
	store.rejectPending = false
	restarted, err := NewDispatcher(store, bus, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Dispatch(t.Context(), envelope("accepted")); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Dispatch(t.Context(), envelope("accepted")); err != nil {
		t.Fatal(err)
	}
	if raw, _, _ := store.Get(t.Context(), ingressSequenceKey); raw != "2" {
		t.Fatalf("retry changed watermark=%q, want 2", raw)
	}
	flush(t, restarted)
	if len(bus.envelopes) != 1 || bus.envelopes[0].ID != "accepted" {
		t.Fatalf("rejected admission executed: %+v", bus.envelopes)
	}
}

func TestConcurrentAdmissionsRetainDistinctSequence(t *testing.T) {
	d, store, bus := fixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for n := range 16 {
		wg.Go(func() { _, err := d.Dispatch(t.Context(), envelope(fmt.Sprintf("concurrent-%d", n))); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	keys, err := store.List(t.Context(), ingressOutboxPrefix)
	if err != nil {
		t.Fatal(err)
	}
	sequences := map[string]uint64{}
	for _, key := range keys {
		raw, _, err := store.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		var record ingressRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			t.Fatal(err)
		}
		env, err := actorlayer.DecodeEnvelope(record.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		sequences[env.ID] = record.Sequence
	}
	flush(t, d)
	if len(bus.envelopes) != 16 {
		t.Fatalf("published=%d", len(bus.envelopes))
	}
	for n, env := range bus.envelopes {
		if got := sequences[env.ID]; got != uint64(n+1) {
			t.Errorf("publication sequence=%d, want %d", got, n+1)
		}
	}
}

func TestUnsequencedPendingRowsDrainBeforeNewAdmissions(t *testing.T) {
	d, store, bus := fixture(t)
	now := time.Now().UTC()
	d.now = func() time.Time { return now }
	if _, err := d.Dispatch(t.Context(), envelope("existing")); err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(t.Context(), ingressOutboxPrefix)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := store.Get(t.Context(), keys[0])
	if err != nil {
		t.Fatal(err)
	}
	var record ingressRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	record.Sequence = 0
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(t.Context(), keys[0], string(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), ingressSequenceKey); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewDispatcher(store, bus, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now.Add(-time.Hour) }
	if _, err := restarted.Dispatch(t.Context(), envelope("new")); err != nil {
		t.Fatal(err)
	}
	flush(t, restarted)
	if len(bus.envelopes) != 2 || bus.envelopes[0].ID != "existing" || bus.envelopes[1].ID != "new" {
		t.Fatalf("upgrade publication order=%+v", bus.envelopes)
	}
}

func TestInvalidAdmissionWatermarkRejectsNewInput(t *testing.T) {
	for _, raw := range []string{"not-a-number", "18446744073709551615"} {
		t.Run(raw, func(t *testing.T) {
			d, store, bus := fixture(t)
			if err := store.Set(t.Context(), ingressSequenceKey, raw); err != nil {
				t.Fatal(err)
			}
			if receipt, err := d.Dispatch(t.Context(), envelope("denied")); err == nil || receipt != nil {
				t.Fatalf("unusable watermark accepted input: %v %v", receipt, err)
			}
			flush(t, d)
			if len(bus.envelopes) != 0 {
				t.Fatal("rejected admission executed")
			}
		})
	}
}
