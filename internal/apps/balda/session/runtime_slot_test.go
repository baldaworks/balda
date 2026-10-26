package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/runtime/v2/agentproduct"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

type testSessionControl struct{}

func (*testSessionControl) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	return agentproduct.SessionControlSnapshot{}
}
func (*testSessionControl) ValidateModel(context.Context, adksession.Session, string) agentproduct.ModelValidation {
	return agentproduct.ModelValidation{}
}
func (*testSessionControl) ChangeModel(context.Context, adksession.Session, string) agentproduct.ModelChange {
	return agentproduct.ModelChange{}
}
func (*testSessionControl) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	return agentproduct.ContextChange{}
}

func TestRuntimeSlotLeaseCarriesOneImmutableRuntime(t *testing.T) {
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: "app", UserID: ensureTestAuthor, SessionID: "runtime-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRunner := &runner.Runner{}
	wantControl := &testSessionControl{}
	wantAgent, err := adkagent.New(adkagent.Config{Name: "slot-agent"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &BuiltRuntime{
		Agent:               wantAgent,
		Runner:              wantRunner,
		SessionControl:      wantControl,
		SessionSvc:          service,
		AppName:             "app",
		RuntimeSnapshotID:   "snapshot-1",
		ProviderBindingJSON: `{"root_provider_id":"provider-1"}`,
	}
	slot := newRuntimeSlot(runtimeSlotConfig{
		runtime: runtime, session: created.Session, userID: "ignored", sessionID: "ignored",
		archiveID: "archive-1", generation: 7, effectiveModel: "model-1", outcome: "context_preserved",
	})
	runtime.Agent = nil
	runtime.Runner = &runner.Runner{}
	runtime.SessionControl = nil
	runtime.RuntimeSnapshotID = "mutated"
	runtime.ProviderBindingJSON = "{}"

	lease, err := slot.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Agent() != wantAgent || lease.Runner() != wantRunner || lease.SessionControl() != wantControl || lease.SessionService() != service || lease.Session() != created.Session {
		t.Fatal("lease mixed or lost runtime objects")
	}
	if lease.AppName() != "app" || lease.UserID() != ensureTestAuthor || lease.SessionID() != "runtime-session" {
		t.Fatalf("identity = %q/%q/%q", lease.AppName(), lease.UserID(), lease.SessionID())
	}
	if lease.ArchiveID() != "archive-1" || lease.Generation() != 7 || lease.EffectiveModel() != "model-1" || lease.Outcome() != "context_preserved" {
		t.Fatalf("metadata = %q/%d/%q/%q", lease.ArchiveID(), lease.Generation(), lease.EffectiveModel(), lease.Outcome())
	}
	if lease.RuntimeSnapshotID() != "snapshot-1" || lease.ProviderBindingJSON() != `{"root_provider_id":"provider-1"}` {
		t.Fatalf("binding = %q/%q", lease.RuntimeSnapshotID(), lease.ProviderBindingJSON())
	}
}

func TestRuntimeSlotDrainWaitsForLeaseWithoutForcedClose(t *testing.T) {
	var closes atomic.Int32
	slot := newRuntimeSlot(runtimeSlotConfig{runtime: &BuiltRuntime{Close: func() error {
		closes.Add(1)
		return nil
	}}})
	topic := &TopicSession{runtime: slot}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := topic.DrainRuntime(ctx); !errors.Is(err, ErrRuntimeCleanupIncomplete) {
		t.Fatalf("DrainRuntime() = %v, want cleanup incomplete", err)
	}
	if closes.Load() != 0 {
		t.Fatal("runtime closed under a live lease")
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("AcquireRuntime() after drain = %v", err)
	}

	var releases sync.WaitGroup
	for range 16 {
		releases.Add(1)
		go func() {
			defer releases.Done()
			lease.Release()
		}()
	}
	releases.Wait()
	if err := topic.DrainRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("close count = %d, want 1", closes.Load())
	}
	if err := topic.DrainRuntime(t.Context()); err != nil {
		t.Fatalf("repeated drain = %v", err)
	}
}

func TestRuntimeSlotSharedOwnerClosesAfterFinalSlot(t *testing.T) {
	var closes atomic.Int32
	parent := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{Runner: &runner.Runner{}, Close: func() error {
			closes.Add(1)
			return nil
		}},
		archiveID: "archive", generation: 1,
	})
	successor, err := parent.sharedSuccessor(2, "model-2", "context_preserved")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 0 {
		t.Fatal("predecessor drain closed shared runtime")
	}
	lease, err := successor.acquire()
	if err != nil {
		t.Fatal(err)
	}
	if lease.ArchiveID() != "archive" || lease.Generation() != 2 || lease.Runner() == nil {
		t.Fatalf("successor lease = generation %d, runner %p", lease.Generation(), lease.Runner())
	}
	lease.Release()
	if err := successor.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("close count = %d, want 1", closes.Load())
	}
	if _, err := parent.sharedSuccessor(3, "model-3", "context_preserved"); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("shared successor after drain = %v", err)
	}
}

func TestRuntimeSlotCandidateAbortIsIdempotentAndBounded(t *testing.T) {
	var closes atomic.Int32
	slot := newRuntimeSlot(runtimeSlotConfig{runtime: &BuiltRuntime{Close: func() error {
		closes.Add(1)
		return errors.New("provider secret: should not escape")
	}}})
	for range 8 {
		if err := slot.drain(t.Context()); !errors.Is(err, ErrRuntimeCloseFailed) || err.Error() != ErrRuntimeCloseFailed.Error() {
			t.Fatalf("candidate abort = %v", err)
		}
	}
	if closes.Load() != 1 {
		t.Fatalf("close count = %d, want 1", closes.Load())
	}
}

func TestRuntimeSlotCloseTimeoutDoesNotInventCompletion(t *testing.T) {
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	slot := newRuntimeSlot(runtimeSlotConfig{runtime: &BuiltRuntime{Close: func() error {
		close(closeStarted)
		<-releaseClose
		return nil
	}}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := slot.drain(ctx); !errors.Is(err, ErrRuntimeCleanupIncomplete) {
		t.Fatalf("drain = %v, want cleanup incomplete", err)
	}
	select {
	case <-closeStarted:
	default:
		t.Fatal("aggregate close did not start")
	}
	close(releaseClose)
	if err := slot.drain(t.Context()); err != nil {
		t.Fatalf("drain after close settled = %v", err)
	}
}

func TestRuntimeSlotConcurrentDrainIsStable(t *testing.T) {
	var closes atomic.Int32
	slot := newRuntimeSlot(runtimeSlotConfig{runtime: &BuiltRuntime{Close: func() error {
		closes.Add(1)
		return nil
	}}})
	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- slot.drain(t.Context())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("drain = %v", err)
		}
	}
	if closes.Load() != 1 {
		t.Fatalf("close count = %d, want 1", closes.Load())
	}
}
