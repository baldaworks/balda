package session

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
	adksession "google.golang.org/adk/v2/session"
)

const freshLifetimeProviderID = "balda-provider"

type freshLifetimeControlSpy struct {
	calls atomic.Int32
}

func (s *freshLifetimeControlSpy) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	s.calls.Add(1)
	return agentproduct.SessionControlSnapshot{}
}

func (s *freshLifetimeControlSpy) ValidateModel(context.Context, adksession.Session, string) agentproduct.ModelValidation {
	s.calls.Add(1)
	return agentproduct.ModelValidation{}
}

func (s *freshLifetimeControlSpy) ChangeModel(context.Context, adksession.Session, string) agentproduct.ModelChange {
	s.calls.Add(1)
	return agentproduct.ModelChange{}
}

func (s *freshLifetimeControlSpy) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	s.calls.Add(1)
	return agentproduct.ContextChange{}
}

func TestFreshLifetimePublishesEmptyLocalIdentityWithSharedOwner(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	control := &freshLifetimeControlSpy{}
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.SessionControl = control
	f.builder.model = publicationOriginalModel
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	runtimeManager := manager.runtimeManager.(*fakeScopedRuntimeManager)

	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	incumbent, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "old-context")
	event.Author = ensureTestAuthor
	if err := incumbent.SessionService().AppendEvent(t.Context(), incumbent.Session(), event); err != nil {
		t.Fatal(err)
	}
	oldRuntimeID := incumbent.SessionID()
	oldArchiveID := incumbent.ArchiveID()
	oldGeneration := incumbent.Generation()
	oldSnapshot := incumbent.RuntimeSnapshotID()
	oldBinding := incumbent.ProviderBindingJSON()
	oldModel := incumbent.EffectiveModel()
	providerBuilds := len(runtimeManager.sessionRequests)
	initialFreshCreates := f.builder.freshCalls

	request := FreshLifetimeRequest{
		OperationID: "fresh-lifetime-operation",
		SessionID:   topic.GetSessionID(),
		InputHash:   strings.Repeat("a", 64),
	}
	prepared, result, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil || result.Status != "" {
		t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", prepared, result, err)
	}
	targetID := prepared.operation.TargetArchiveID
	if targetID == "" || targetID == oldRuntimeID || targetID != prepared.candidate.value.sessionID {
		t.Fatalf("target runtime identity = %q, old %q, candidate %q", targetID, oldRuntimeID, prepared.candidate.value.sessionID)
	}
	if prepared.candidate.value.archiveID != targetID || prepared.candidate.value.session.Events().Len() != 0 {
		t.Fatalf("candidate archive/events = %q/%d", prepared.candidate.value.archiveID, prepared.candidate.value.session.Events().Len())
	}
	if len(runtimeManager.sessionRequests) != providerBuilds {
		t.Fatalf("provider builds = %d, want unchanged %d", len(runtimeManager.sessionRequests), providerBuilds)
	}
	if f.builder.freshCalls != initialFreshCreates+1 {
		t.Fatalf("local session creates = %d, want %d", f.builder.freshCalls, initialFreshCreates+1)
	}
	if got := f.builder.freshProviders[len(f.builder.freshProviders)-1]; got != freshLifetimeProviderID || got == topic.agentName {
		t.Fatalf("fresh local initializer provider = %q, display label = %q", got, topic.agentName)
	}

	result, err = manager.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeReset || result.Generation != oldGeneration+1 {
		t.Fatalf("PublishFreshLifetime() = %#v, %v", result, err)
	}
	current, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if topic.GetSessionID() != request.SessionID || topic.GetUserID() != archiveInput().UserID || topic.GetAgentSessionID() != targetID {
		t.Fatalf("stable/runtime identity = %q/%q/%q", topic.GetSessionID(), topic.GetUserID(), topic.GetAgentSessionID())
	}
	if current.SessionID() != targetID || current.ArchiveID() != targetID || current.Generation() != oldGeneration+1 ||
		current.RuntimeSnapshotID() != oldSnapshot || current.ProviderBindingJSON() != oldBinding || current.EffectiveModel() != oldModel {
		t.Fatalf("published runtime = session %q archive %q generation %d snapshot %q binding %q model %q",
			current.SessionID(), current.ArchiveID(), current.Generation(), current.RuntimeSnapshotID(), current.ProviderBindingJSON(), current.EffectiveModel())
	}
	if current.SessionService() != incumbent.SessionService() || current.Session().Events().Len() != 0 {
		t.Fatal("fresh lifetime did not preserve the local service with an empty isolated session")
	}
	if got := archiveStateString(current.Session(), chatarchivecmd.EpochStateKey); got != targetID {
		t.Fatalf("target epoch = %q, want %q", got, targetID)
	}
	if got := archiveStateString(current.Session(), baldaSessionIDStateKey); got != request.SessionID {
		t.Fatalf("stable session state = %q, want %q", got, request.SessionID)
	}
	oldArchive, err := f.provider.ChatArchive().Get(t.Context(), oldArchiveID)
	if err != nil || oldArchive.Status != chatarchivecmd.StatusEnded {
		t.Fatalf("old archive = %#v, %v", oldArchive, err)
	}
	newArchive, err := f.provider.ChatArchive().Get(t.Context(), targetID)
	if err != nil || newArchive.Status != chatarchivecmd.StatusActive || newArchive.RuntimeID != targetID || newArchive.PredecessorID != oldArchiveID {
		t.Fatalf("new archive = %#v, %v", newArchive, err)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
	if err != nil || !found || record.ArchiveID != targetID || record.RuntimeGeneration != oldGeneration+1 ||
		record.RuntimeSnapshotID != oldSnapshot || record.ProviderBindingJSON != oldBinding || record.EffectiveModel != oldModel {
		t.Fatalf("durable metadata = %#v, %t, %v", record, found, err)
	}

	replayed, replayResult, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || replayed != nil || replayResult.Status != FreshLifetimeReset || replayResult.Generation != oldGeneration+1 {
		t.Fatalf("terminal replay = %#v, %#v, %v", replayed, replayResult, err)
	}
	collision := request
	collision.InputHash = strings.Repeat("b", 64)
	if plan, collisionResult, collisionErr := manager.PrepareFreshLifetime(t.Context(), collision); collisionErr != nil || plan != nil || collisionResult.Status != FreshLifetimeConflict {
		t.Fatalf("collision = %#v, %#v, %v", plan, collisionResult, collisionErr)
	}
	if len(runtimeManager.sessionRequests) != providerBuilds || f.builder.freshCalls != initialFreshCreates+1 {
		t.Fatal("replay or collision performed runtime construction")
	}
	if control.calls.Load() != 0 {
		t.Fatalf("SessionControl calls = %d, want 0", control.calls.Load())
	}

	incumbent.Release()
	if closeCalls.Load() != 0 {
		t.Fatalf("aggregate closed while successor remained live: %d", closeCalls.Load())
	}
	current.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

func TestFreshLifetimePreparedAbortDeletesOnlyTarget(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "fresh-abort", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("c", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	targetID := prepared.operation.TargetArchiveID
	result, err := manager.AbortFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeUnavailable {
		t.Fatalf("AbortFreshLifetime() = %#v, %v", result, err)
	}
	if _, err := f.runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: f.runtime.AppName, UserID: archiveInput().UserID, SessionID: targetID}); !errors.Is(err, adksession.ErrNotFound) {
		t.Fatalf("target Get() error = %v, want not found", err)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("incumbent admission after prepared abort = %v", err)
	}
	lease.Release()
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal || operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) {
		t.Fatalf("aborted operation = %#v, %t, %v", operation, found, err)
	}
}

func TestFreshLifetimeApplyingRecoveryPublishesPinnedTarget(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "fresh-applying", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("d", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
		t.Fatal(err)
	}
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found {
		t.Fatalf("applying operation = %#v, %t, %v", operation, found, err)
	}
	if err := manager.recoverApplyingFreshLifetimeOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	t.Cleanup(func() {
		_ = topic.DrainRuntime(context.Background())
	})
	if lease.SessionID() != prepared.operation.TargetArchiveID || lease.ArchiveID() != prepared.operation.TargetArchiveID ||
		lease.Generation() != prepared.operation.ExpectedGeneration+1 || lease.Session().Events().Len() != 0 {
		t.Fatalf("recovered runtime = session %q archive %q generation %d events %d",
			lease.SessionID(), lease.ArchiveID(), lease.Generation(), lease.Session().Events().Len())
	}
	terminal, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found || terminal.State != baldastate.RuntimeOperationTerminal ||
		terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("terminal operation = %#v, %t, %v", terminal, found, err)
	}
}

func TestFreshLifetimeColdRecoveryUsesPinnedLocalIdentity(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	oldEvent := adksession.NewEvent(t.Context(), "old-context")
	oldEvent.Author = ensureTestAuthor
	if err := oldLease.SessionService().AppendEvent(t.Context(), oldLease.Session(), oldEvent); err != nil {
		t.Fatal(err)
	}
	oldLease.Release()
	request := FreshLifetimeRequest{OperationID: "fresh-cold-recovery", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("e", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
		t.Fatal(err)
	}
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found {
		t.Fatalf("applying operation = %#v, %t, %v", operation, found, err)
	}

	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
	if err := restarted.recoverApplyingFreshLifetimeOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	if len(runtimeManager.sessionRequests) != 0 {
		t.Fatalf("cold recovery built provider runtime %d times", len(runtimeManager.sessionRequests))
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
	if err != nil || !found || record.ArchiveID != prepared.operation.TargetArchiveID ||
		record.RuntimeGeneration != prepared.operation.ExpectedGeneration+1 {
		t.Fatalf("cold recovered metadata = %#v, %t, %v", record, found, err)
	}
	restored, err := restarted.RestoreSession(t.Context(), SessionContext{Locator: archiveInput().Locator})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.SessionID() != prepared.operation.TargetArchiveID {
		t.Fatalf("restored target = %q, want %q", lease.SessionID(), prepared.operation.TargetArchiveID)
	}
	for event := range lease.Session().Events().All() {
		if event.ID == "old-context" {
			t.Fatal("cold recovery restored predecessor context")
		}
	}
}

func TestFreshLifetimeRecoveryFencesContradictoryTarget(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "fresh-contradiction", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("f", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "contradictory-context")
	event.Author = ensureTestAuthor
	if err := f.runtime.SessionSvc.AppendEvent(t.Context(), prepared.candidate.value.session, event); err != nil {
		t.Fatal(err)
	}
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
		t.Fatal(err)
	}
	operation, _, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.recoverApplyingFreshLifetimeOperation(t.Context(), operation); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("recovery error = %v, want recovery required", err)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v, want recovery fence", err)
	}
	unresolved, _, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || unresolved.State != baldastate.RuntimeOperationApplying {
		t.Fatalf("unresolved operation = %#v, %v", unresolved, err)
	}
}

func TestFreshLifetimeConcurrentRedeliveryPublishesOnce(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	initialCreates := f.builder.freshCalls
	request := FreshLifetimeRequest{OperationID: "fresh-redelivery", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("1", 64)}

	type preparedResult struct {
		plan   *PreparedFreshLifetime
		result FreshLifetimeResult
		err    error
	}
	start := make(chan struct{})
	preparedResults := make(chan preparedResult, 2)
	for range 2 {
		go func() {
			<-start
			plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
			preparedResults <- preparedResult{plan: plan, result: result, err: err}
		}()
	}
	close(start)
	first := <-preparedResults
	second := <-preparedResults
	for i, got := range []preparedResult{first, second} {
		if got.err != nil || got.plan == nil || got.result.Status != "" {
			t.Fatalf("prepare %d = %#v, %#v, %v", i, got.plan, got.result, got.err)
		}
	}
	if f.builder.freshCalls != initialCreates+1 {
		t.Fatalf("redelivery local session creates = %d, want %d", f.builder.freshCalls, initialCreates+1)
	}

	results := make(chan preparedResult, 2)
	for _, plan := range []*PreparedFreshLifetime{first.plan, second.plan} {
		go func(plan *PreparedFreshLifetime) {
			result, err := manager.PublishFreshLifetime(t.Context(), plan)
			results <- preparedResult{result: result, err: err}
		}(plan)
	}
	for i := range 2 {
		got := <-results
		if got.err != nil || got.result.Status != FreshLifetimeReset || got.result.Generation != 1 {
			t.Fatalf("publish %d = %#v, %v", i, got.result, got.err)
		}
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.Generation() != 1 || lease.SessionID() != deterministicFreshLifetimeID(request) {
		t.Fatalf("published runtime = generation %d session %q", lease.Generation(), lease.SessionID())
	}
	lease.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
		operation.Outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("operation = %#v, %t, %v", operation, found, err)
	}
}

func TestFreshLifetimePreparationFailureRollsBackAuthoritatively(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*archiveFixture, *Manager)
	}{
		{
			name: "archive begin rejected",
			configure: func(f *archiveFixture, manager *Manager) {
				manager.archive = &archiveFault{ArchiveLifecycle: f.provider.ChatArchive(), beginErr: chatarchivecmd.ErrUnavailable}
			},
		},
		{
			name: "local create rejected",
			configure: func(f *archiveFixture, _ *Manager) {
				f.builder.fail = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			manager := f.manager()
			manager.runtimeOperations = f.provider.RuntimeOperations()
			topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
			if err != nil {
				t.Fatal(err)
			}
			test.configure(f, manager)
			request := FreshLifetimeRequest{
				OperationID: "rollback-" + strings.ReplaceAll(test.name, " ", "-"),
				SessionID:   topic.GetSessionID(),
				InputHash:   strings.Repeat("2", 64),
			}
			plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
			if err != nil || plan != nil || result.Status != FreshLifetimeUnavailable {
				t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", plan, result, err)
			}
			operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
			if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
				operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) {
				t.Fatalf("rolled back operation = %#v, %t, %v", operation, found, err)
			}
			lease, err := topic.AcquireRuntime()
			if err != nil {
				t.Fatalf("incumbent fenced after definitive preparation failure: %v", err)
			}
			lease.Release()
			if _, err := f.provider.ChatArchive().Pending(t.Context(), topic.locator.ChannelType, topic.locator.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
				t.Fatalf("pending archive after rollback = %v", err)
			}
		})
	}
}

type uncertainBeginRuntimeStore struct {
	baldastate.RuntimeOperationStore
}

func (s uncertainBeginRuntimeStore) Begin(ctx context.Context, operation baldastate.RuntimeOperationRecord) (baldastate.RuntimeOperationRecord, error) {
	committed, err := s.RuntimeOperationStore.Begin(context.WithoutCancel(ctx), operation)
	if err != nil {
		return committed, err
	}
	return baldastate.RuntimeOperationRecord{}, context.Canceled
}

type freshLifetimeGetFaultService struct {
	adksession.Service
	fail atomic.Bool
}

func (s *freshLifetimeGetFaultService) Get(ctx context.Context, request *adksession.GetRequest) (*adksession.GetResponse, error) {
	if s.fail.Load() {
		return nil, errors.New("private-get-secret")
	}
	return s.Service.Get(ctx, request)
}

type freshLifetimeCreateCommitErrorService struct {
	adksession.Service
	fail atomic.Bool
}

func (s *freshLifetimeCreateCommitErrorService) Create(ctx context.Context, request *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	response, err := s.Service.Create(ctx, request)
	if err != nil {
		return response, err
	}
	if !s.fail.Load() {
		return response, nil
	}
	return nil, errors.New("private-create-secret")
}

func TestFreshLifetimePreparationUncertaintyUsesStoreTruth(t *testing.T) {
	t.Run("begin committed then cancellation rolls back", func(t *testing.T) {
		f := newArchiveFixture(t)
		f.runtime.ProviderBindingJSON = publicationBinding
		manager := f.manager()
		manager.runtimeOperations = uncertainBeginRuntimeStore{RuntimeOperationStore: f.provider.RuntimeOperations()}
		topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		request := FreshLifetimeRequest{OperationID: "uncertain-begin", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("3", 64)}
		plan, result, err := manager.PrepareFreshLifetime(ctx, request)
		if err != nil || plan != nil || result.Status != FreshLifetimeUnavailable || result.Reason != baldastate.RuntimeOperationReasonCanceled {
			t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", plan, result, err)
		}
		operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
		if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
			operation.ReasonCode != string(baldastate.RuntimeOperationReasonCanceled) {
			t.Fatalf("resolved begin = %#v, %t, %v", operation, found, err)
		}
	})

	t.Run("create committed then error is adopted by Get", func(t *testing.T) {
		f := newArchiveFixture(t)
		f.runtime.ProviderBindingJSON = publicationBinding
		service := &freshLifetimeCreateCommitErrorService{Service: f.runtime.SessionSvc}
		f.runtime.SessionSvc = service
		f.builder.service = service
		manager := f.manager()
		manager.runtimeOperations = f.provider.RuntimeOperations()
		topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
		if err != nil {
			t.Fatal(err)
		}
		service.fail.Store(true)
		request := FreshLifetimeRequest{OperationID: "uncertain-create", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("4", 64)}
		plan, _, err := manager.PrepareFreshLifetime(t.Context(), request)
		if err != nil || plan == nil {
			t.Fatalf("PrepareFreshLifetime() = %#v, %v", plan, err)
		}
		if _, err := manager.AbortFreshLifetime(t.Context(), plan); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unreadable target remains prepared and fenced", func(t *testing.T) {
		f := newArchiveFixture(t)
		f.runtime.ProviderBindingJSON = publicationBinding
		service := &freshLifetimeGetFaultService{Service: f.runtime.SessionSvc}
		f.runtime.SessionSvc = service
		f.builder.service = service
		manager := f.manager()
		manager.runtimeOperations = f.provider.RuntimeOperations()
		topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
		if err != nil {
			t.Fatal(err)
		}
		service.fail.Store(true)
		request := FreshLifetimeRequest{OperationID: "uncertain-get", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("5", 64)}
		plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
		if plan != nil || result.Status != FreshLifetimeRecoveryRequired || !errors.Is(err, ErrRuntimeRecoveryRequired) {
			t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", plan, result, err)
		}
		operation, found, getErr := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
		if getErr != nil || !found || operation.State != baldastate.RuntimeOperationPrepared {
			t.Fatalf("uncertain operation = %#v, %t, %v", operation, found, getErr)
		}
		if _, acquireErr := topic.AcquireRuntime(); !errors.Is(acquireErr, ErrRuntimeRecoveryRequired) {
			t.Fatalf("AcquireRuntime() = %v, want recovery fence", acquireErr)
		}
	})
}

func TestFreshLifetimePublishAbortRaceSettlesOnce(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "publish-abort-race", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("6", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	type settled struct {
		result FreshLifetimeResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan settled, 2)
	go func() {
		<-start
		result, err := manager.PublishFreshLifetime(t.Context(), prepared)
		results <- settled{result: result, err: err}
	}()
	go func() {
		<-start
		result, err := manager.AbortFreshLifetime(t.Context(), prepared)
		results <- settled{result: result, err: err}
	}()
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.result != second.result {
		t.Fatalf("settlements = %#v/%v and %#v/%v", first.result, first.err, second.result, second.err)
	}
	if first.result.Status != FreshLifetimeReset && first.result.Status != FreshLifetimeUnavailable {
		t.Fatalf("settlement status = %q", first.result.Status)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("terminal settlement fenced incumbent: %v", err)
	}
	lease.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

func TestFreshLifetimeRedeliveredAbortReplaysPublishedTerminal(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "publish-then-abort", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("8", 64)}
	first, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	redelivered, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	published, err := manager.PublishFreshLifetime(t.Context(), first)
	if err != nil || published.Status != FreshLifetimeReset {
		t.Fatalf("PublishFreshLifetime() = %#v, %v", published, err)
	}
	replayed, err := manager.AbortFreshLifetime(t.Context(), redelivered)
	if err != nil || replayed != published {
		t.Fatalf("AbortFreshLifetime() replay = %#v, %v; want %#v", replayed, err, published)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("terminal replay fenced runtime: %v", err)
	}
	lease.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFreshLifetimePreparationLeaseBlocksDrainUntilRollback(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	f.builder.beforeFresh = func() {
		close(entered)
		<-release
	}
	request := FreshLifetimeRequest{OperationID: "prepare-drain", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("7", 64)}
	type preparation struct {
		plan   *PreparedFreshLifetime
		result FreshLifetimeResult
		err    error
	}
	prepared := make(chan preparation, 1)
	go func() {
		plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
		prepared <- preparation{plan: plan, result: result, err: err}
	}()
	<-entered
	drained := make(chan error, 1)
	go func() {
		drained <- topic.DrainRuntime(context.Background())
	}()
	select {
	case err := <-drained:
		t.Fatalf("drain completed while preparation held a lease: %v", err)
	default:
	}
	if closeCalls.Load() != 0 {
		t.Fatalf("aggregate closed during preparation: %d", closeCalls.Load())
	}
	close(release)
	got := <-prepared
	if got.err != nil || got.plan != nil || got.result.Status != FreshLifetimeUnavailable {
		t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", got.plan, got.result, got.err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

func TestFreshLifetimeAfterHostedReplacementPreservesProviderIdentity(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	incumbent := topic.runtime
	replacementRuntime := incumbent.value.runtime
	replacement, err := incumbent.independentModelSuccessor(
		&replacementRuntime,
		incumbent.value.session,
		"hosted-model",
		string(baldastate.RuntimeOperationOutcomeContextPreserved),
	)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.value.providerID != freshLifetimeProviderID {
		t.Fatalf("hosted successor provider = %q, want %q", replacement.value.providerID, freshLifetimeProviderID)
	}
	if !replacement.claimPublication() ||
		!topic.swapRuntime(incumbent, replacement, incumbent.value.archiveID, incumbent.value.generation) {
		t.Fatal("failed to publish hosted replacement fixture")
	}
	replacement.releasePublicationClaim(true)
	incumbent.beginDrain()

	request := FreshLifetimeRequest{OperationID: "hosted-then-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("9", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.builder.freshProviders[len(f.builder.freshProviders)-1]; got != freshLifetimeProviderID || got == topic.agentName {
		t.Fatalf("reset initializer provider = %q, display label = %q", got, topic.agentName)
	}
	if _, err := manager.AbortFreshLifetime(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type freshLifetimeOperationGetErrorStore struct {
	baldastate.RuntimeOperationStore
}

func (s freshLifetimeOperationGetErrorStore) Get(context.Context, string) (baldastate.RuntimeOperationRecord, bool, error) {
	return baldastate.RuntimeOperationRecord{}, false, errors.New("private-operation-secret")
}

func TestFreshLifetimeAbortGetAmbiguityReleasesCandidateOwner(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "abort-get-ambiguity", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("a", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	manager.runtimeOperations = freshLifetimeOperationGetErrorStore{RuntimeOperationStore: f.provider.RuntimeOperations()}
	result, err := manager.AbortFreshLifetime(t.Context(), prepared)
	if result.Status != FreshLifetimeRecoveryRequired || !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AbortFreshLifetime() = %#v, %v", result, err)
	}
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

type freshLifetimeCloseAwareService struct {
	adksession.Service
	closed       *atomic.Bool
	violation    *atomic.Bool
	deleteEnter  chan struct{}
	deleteResume chan struct{}
}

func (s *freshLifetimeCloseAwareService) Delete(ctx context.Context, request *adksession.DeleteRequest) error {
	if s.closed.Load() {
		s.violation.Store(true)
	}
	close(s.deleteEnter)
	<-s.deleteResume
	if s.closed.Load() {
		s.violation.Store(true)
	}
	return s.Service.Delete(ctx, request)
}

type freshLifetimeFinalizeBarrierStore struct {
	baldastate.RuntimeOperationStore
	closed         *atomic.Bool
	violation      *atomic.Bool
	finalizeEnter  chan struct{}
	finalizeResume chan struct{}
}

func (s *freshLifetimeFinalizeBarrierStore) Finalize(ctx context.Context, final baldastate.RuntimeOperationFinalization) (baldastate.RuntimeOperationRecord, error) {
	if final.Outcome == baldastate.RuntimeOperationOutcomeAborted {
		if s.closed.Load() {
			s.violation.Store(true)
		}
		close(s.finalizeEnter)
		<-s.finalizeResume
		if s.closed.Load() {
			s.violation.Store(true)
		}
	}
	return s.RuntimeOperationStore.Finalize(ctx, final)
}

func TestFreshLifetimeAbortRetainsOwnerThroughDeleteAndFinalize(t *testing.T) {
	f := newArchiveFixture(t)
	var closed, violation atomic.Bool
	var closeCalls atomic.Int32
	service := &freshLifetimeCloseAwareService{
		Service: f.runtime.SessionSvc, closed: &closed, violation: &violation,
		deleteEnter: make(chan struct{}), deleteResume: make(chan struct{}),
	}
	f.runtime.SessionSvc = service
	f.builder.service = service
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closed.Store(true)
		closeCalls.Add(1)
		return nil
	}
	operations := &freshLifetimeFinalizeBarrierStore{
		RuntimeOperationStore: f.provider.RuntimeOperations(),
		closed:                &closed,
		violation:             &violation,
		finalizeEnter:         make(chan struct{}),
		finalizeResume:        make(chan struct{}),
	}
	manager := f.manager()
	manager.runtimeOperations = operations
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "abort-owner-barrier", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("b", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	type abortResult struct {
		result FreshLifetimeResult
		err    error
	}
	aborted := make(chan abortResult, 1)
	go func() {
		result, err := manager.AbortFreshLifetime(t.Context(), prepared)
		aborted <- abortResult{result: result, err: err}
	}()
	<-service.deleteEnter
	drained := make(chan error, 1)
	go func() {
		drained <- topic.DrainRuntime(context.Background())
	}()
	if closed.Load() {
		t.Fatal("aggregate closed before target Delete")
	}
	close(service.deleteResume)
	<-operations.finalizeEnter
	if closed.Load() {
		t.Fatal("aggregate closed before operation Finalize")
	}
	close(operations.finalizeResume)
	got := <-aborted
	if got.err != nil || got.result.Status != FreshLifetimeUnavailable {
		t.Fatalf("AbortFreshLifetime() = %#v, %v", got.result, got.err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if violation.Load() || closeCalls.Load() != 1 {
		t.Fatalf("close ordering violation=%t close calls=%d", violation.Load(), closeCalls.Load())
	}
}
