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

const (
	runtimeSlotTestArchiveID = "archive"
	runtimeSlotMismatchValue = "other"
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

type observationController struct {
	snapshot agentproduct.SessionControlSnapshot
}

func (c *observationController) InspectSessionControl(context.Context, RuntimeSessionIdentity) agentproduct.SessionControlSnapshot {
	return c.snapshot
}
func (*observationController) ValidateModel(context.Context, RuntimeSessionIdentity, string) agentproduct.ModelValidation {
	return agentproduct.ModelValidation{}
}
func (*observationController) ChangeModel(context.Context, RuntimeSessionIdentity, string) RuntimeModelChange {
	return RuntimeModelChange{}
}

func TestRuntimeSlotModelObservationIsGenerationScoped(t *testing.T) {
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{AppName: "app", UserID: "user", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	controller := &observationController{snapshot: agentproduct.SessionControlSnapshot{
		Status: agentproduct.ControlReady, CurrentModel: "model-active", ControlRevision: "opaque-revision",
	}}
	slot := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{SessionSvc: service, AppName: "app", ModelController: controller},
		session: created.Session, archiveID: "archive", generation: 4,
	})
	lease, err := slot.acquire()
	if err != nil {
		t.Fatal(err)
	}
	lease.RefreshModelObservation(t.Context())
	observation, ok := lease.ModelObservation()
	if !ok || observation.Model != "model-active" || observation.ControlRevision != "opaque-revision" ||
		observation.Generation != 4 || observation.Kind != ModelObservationProviderObserved {
		t.Fatalf("observation = %+v, %t", observation, ok)
	}
	lease.Release()

	successor, err := slot.sharedModelSuccessor("model-next", publicationOutcome)
	if err != nil {
		t.Fatal(err)
	}
	successorLease, err := successor.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer successorLease.Release()
	if got, ok := successorLease.ModelObservation(); ok {
		t.Fatalf("successor inherited stale observation: %+v", got)
	}
	controller.snapshot.ControlRevision = ""
	controller.snapshot.CurrentModel = "hosted-local"
	successorLease.RefreshModelObservation(t.Context())
	if got, ok := successorLease.ModelObservation(); !ok || got.Kind != ModelObservationLocallyExecuted || got.Model != "hosted-local" {
		t.Fatalf("hosted observation = %+v, %t", got, ok)
	}
}

func TestRuntimeSlotLeaseCarriesOneImmutableRuntime(t *testing.T) {
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: "app", UserID: ensureTestAuthor, SessionID: "runtime-session",
		State: map[string]any{"lease-key": "lease-value"},
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
	if value, found, err := lease.RuntimeStateValue(t.Context(), "lease-key"); err != nil || !found || value != "lease-value" {
		t.Fatalf("RuntimeStateValue() = %v, %t, %v", value, found, err)
	}
}

func TestIndependentModelSuccessorRequiresExactIdentity(t *testing.T) {
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: "app", UserID: "user", SessionID: "session",
	})
	if err != nil {
		t.Fatal(err)
	}
	incumbent := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{SessionSvc: service, AppName: "app", RuntimeSnapshotID: "snapshot", ProviderBindingJSON: "binding"},
		session: created.Session, archiveID: runtimeSlotTestArchiveID, generation: 4, effectiveModel: "old",
	})
	replacement := &BuiltRuntime{
		SessionSvc: service, AppName: "app", RuntimeSnapshotID: "snapshot",
		ProviderBindingJSON: "binding", ConfirmedModel: "new",
	}
	candidate, err := incumbent.independentModelSuccessor(replacement, created.Session, "new", publicationOutcome)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.value.generation != 5 || candidate.value.archiveID != runtimeSlotTestArchiveID || candidate.value.session != created.Session {
		t.Fatalf("candidate = %+v", candidate.value)
	}

	wrongService := *replacement
	wrongService.SessionSvc = adksession.InMemoryService()
	if _, err := incumbent.independentModelSuccessor(&wrongService, created.Session, "new", publicationOutcome); !errors.Is(err, ErrRuntimePublicationInvalid) {
		t.Fatalf("wrong service error = %v", err)
	}
	wrongSnapshot := *replacement
	wrongSnapshot.RuntimeSnapshotID = runtimeSlotMismatchValue
	if _, err := incumbent.independentModelSuccessor(&wrongSnapshot, created.Session, "new", publicationOutcome); !errors.Is(err, ErrRuntimePublicationInvalid) {
		t.Fatalf("wrong snapshot error = %v", err)
	}
	wrongBinding := *replacement
	wrongBinding.ProviderBindingJSON = runtimeSlotMismatchValue
	if _, err := incumbent.independentModelSuccessor(&wrongBinding, created.Session, "new", publicationOutcome); !errors.Is(err, ErrRuntimePublicationInvalid) {
		t.Fatalf("wrong binding error = %v", err)
	}
	wrongModel := *replacement
	wrongModel.ConfirmedModel = modelChangeOtherModel
	if _, err := incumbent.independentModelSuccessor(&wrongModel, created.Session, "new", publicationOutcome); !errors.Is(err, ErrRuntimePublicationInvalid) {
		t.Fatalf("wrong confirmed model error = %v", err)
	}
	wrongApp := *replacement
	wrongApp.AppName = runtimeSlotMismatchValue
	if _, err := incumbent.independentModelSuccessor(&wrongApp, created.Session, "new", publicationOutcome); !errors.Is(err, ErrRuntimePublicationInvalid) {
		t.Fatalf("wrong app error = %v", err)
	}
	otherSession, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: "app", UserID: "user", SessionID: "other-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := incumbent.independentModelSuccessor(replacement, otherSession.Session, "new", publicationOutcome); !errors.Is(err, ErrRuntimePublicationInvalid) {
		t.Fatalf("wrong session error = %v", err)
	}
}

func TestSharedModelSuccessorAdvancesExactlyOneGeneration(t *testing.T) {
	incumbent := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{}, archiveID: runtimeSlotTestArchiveID, generation: 8, effectiveModel: "old",
	})
	candidate, err := incumbent.sharedModelSuccessor("new", publicationOutcome)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.value.generation != 9 || candidate.value.archiveID != runtimeSlotTestArchiveID || candidate.owner != incumbent.owner {
		t.Fatalf("candidate = %+v", candidate.value)
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
	if lease.ArchiveID() != runtimeSlotTestArchiveID || lease.Generation() != 2 || lease.Runner() == nil {
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

func TestRuntimeSlotConcurrentAbortReturnsOneBoundedCloseResult(t *testing.T) {
	privateDetail := errors.New("provider secret: abort detail")
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	var closes atomic.Int32
	slot := newRuntimeSlot(runtimeSlotConfig{runtime: &BuiltRuntime{Close: func() error {
		closes.Add(1)
		close(closeStarted)
		<-releaseClose
		return privateDetail
	}}})

	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- slot.abortUnpublished(t.Context())
		}()
	}
	close(start)
	<-closeStarted
	if closes.Load() != 1 {
		t.Fatalf("close calls while blocked = %d, want 1", closes.Load())
	}
	close(releaseClose)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrRuntimeCloseFailed) || err.Error() != ErrRuntimeCloseFailed.Error() {
			t.Errorf("abortUnpublished() error = %v, want bounded close failure", err)
		}
	}
	if closes.Load() != 1 {
		t.Fatalf("close calls = %d, want 1", closes.Load())
	}
}

func TestRuntimeSlotSharedOwnerWaitsForEverySlotAndLease(t *testing.T) {
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	var closes atomic.Int32
	root := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{Close: func() error {
			closes.Add(1)
			close(closeStarted)
			<-releaseClose
			return nil
		}},
		archiveID:  runtimeSlotTestArchiveID,
		generation: 1,
	})
	first, err := root.sharedSuccessor(2, "model-2", publicationOutcome)
	if err != nil {
		t.Fatal(err)
	}
	second, err := first.sharedSuccessor(3, "model-3", publicationOutcome)
	if err != nil {
		t.Fatal(err)
	}
	slots := []*RuntimeSlot{root, first, second}
	leases := make([]*RuntimeLease, 0, len(slots))
	for _, slot := range slots {
		lease, err := slot.acquire()
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
		slot.beginDrain()
	}
	if closes.Load() != 0 {
		t.Fatal("shared owner closed while leases remained")
	}

	var releases sync.WaitGroup
	for _, lease := range leases {
		releases.Add(1)
		go func() {
			defer releases.Done()
			lease.Release()
		}()
	}
	releases.Wait()
	<-closeStarted
	if closes.Load() != 1 {
		t.Fatalf("close calls while blocked = %d, want 1", closes.Load())
	}
	close(releaseClose)
	for _, slot := range slots {
		if err := slot.waitDrain(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if closes.Load() != 1 {
		t.Fatalf("close calls = %d, want 1", closes.Load())
	}
}
