package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

const modelColdSnapshotID = "snapshot"

type modelStartHooks struct {
	events []hookcmd.Event
	err    error
}

func (h *modelStartHooks) Run(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	h.events = append(h.events, event)
	return hookcmd.Result{}, h.err
}

const (
	modelChangeTargetModel   = "model-2"
	modelChangeOtherModel    = "other-model"
	modelControlCallOrder    = "validate,change"
	modelValidationCallOrder = "validate"
)

func TestModelChangeResultVocabulary(t *testing.T) {
	statuses := []ModelChangeResultStatus{
		ModelChangeChangedLive, ModelChangeChangedReplacement,
		ModelChangeUnsupported, ModelChangeIncompatible, ModelChangeRejected, ModelChangeBusy,
		ModelChangeConflict, ModelChangeUnavailable, ModelChangeRecoveryRequired,
	}
	want := []string{
		"changed_live", "changed_replacement", "unsupported", "incompatible", "rejected", "busy",
		"conflict", "unavailable", "recovery_required",
	}
	for index, status := range statuses {
		if string(status) != want[index] {
			t.Fatalf("status[%d] = %q, want %q", index, status, want[index])
		}
	}
}

type modelTestController struct {
	mu         sync.Mutex
	validation agentproduct.ModelValidation
	change     RuntimeModelChange
	calls      []string
	changeHook func()
}

func (c *modelTestController) InspectSessionControl(context.Context, RuntimeSessionIdentity) agentproduct.SessionControlSnapshot {
	return agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: publicationOriginalModel}
}

func (c *modelTestController) ValidateModel(_ context.Context, _ RuntimeSessionIdentity, _ string) agentproduct.ModelValidation {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "validate")
	return c.validation
}

func (c *modelTestController) ChangeModel(_ context.Context, _ RuntimeSessionIdentity, _ string) RuntimeModelChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "change")
	if c.changeHook != nil {
		c.changeHook()
	}
	return c.change
}

func (c *modelTestController) callOrder() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func TestChangeModelLivePublishesSharedSuccessorOnce(t *testing.T) {
	var closes atomic.Int32
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported, Model: modelChangeTargetModel},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topic, manager, store, _, _ := modelChangeFixture(t, control, func() error { closes.Add(1); return nil })
	incumbentOwner := topic.currentRuntime().owner
	request := modelChangeRequest()

	result, err := manager.ChangeModel(t.Context(), request)
	if err != nil || result.Status != ModelChangeChangedLive || result.EffectiveModel != modelChangeTargetModel ||
		result.Reason != baldastate.RuntimeOperationReasonChangedLive || result.Generation != 2 {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if got := strings.Join(control.callOrder(), ","); got != modelControlCallOrder {
		t.Fatalf("control order = %q", got)
	}
	if got := strings.Join(store.callOrder(), ","); got != "begin,applying,finalize:context_preserved" {
		t.Fatalf("store order = %q", got)
	}
	current := topic.currentRuntime()
	if current == nil || current.owner == nil || current.owner != incumbentOwner {
		t.Fatal("published runtime lost its shared owner")
	}
	if closes.Load() != 0 {
		t.Fatal("shared owner closed during publication")
	}

	replayed, err := manager.ChangeModel(t.Context(), request)
	if err != nil || replayed != result {
		t.Fatalf("redelivery = %+v, %v", replayed, err)
	}
	if got := strings.Join(control.callOrder(), ","); got != modelControlCallOrder {
		t.Fatalf("redelivery called control: %q", got)
	}
	if err := topic.DrainRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("close count = %d, want 1", closes.Load())
	}
}

type modelChangeArchive struct {
	ArchiveLifecycle
	session chatarchivecmd.Session
}

func (a *modelChangeArchive) Get(context.Context, string) (chatarchivecmd.Session, error) {
	return a.session, nil
}

type coldModelChangeUnitFixture struct {
	manager        *Manager
	newManager     func() *Manager
	control        *modelTestController
	hooks          *modelStartHooks
	runtimeManager *fakeScopedRuntimeManager
	operations     *publicationStore
	store          *fakeSessionStore
	record         baldastate.SessionRecord
}

func newColdModelChangeUnitFixture(
	t *testing.T,
	validation agentproduct.ModelValidation,
	change RuntimeModelChange,
) coldModelChangeUnitFixture {
	t.Helper()
	locator := testTelegramLocator(10, 42)
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: "user", SessionID: publicationArchiveID,
	})
	if err != nil {
		t.Fatal(err)
	}
	control := &modelTestController{validation: validation, change: change}
	runtime := &BuiltRuntime{
		ModelController: control, SessionSvc: service, AppName: baldaRuntimeAppName,
		RuntimeSnapshotID: modelColdSnapshotID, ProviderBindingJSON: publicationBinding,
		RecipeModel: publicationOriginalModel,
	}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "provider"},
		sessionRuntime:          runtime,
	}
	builder := &fakeAgentBuilder{
		agentMetadata:  AgentMetadata{Model: publicationOriginalModel},
		runtimeSession: created.Session,
	}
	requestedModel := publicationOriginalModel
	record := baldastate.SessionRecord{
		ArchiveID: publicationArchiveID, SessionID: locator.SessionID, UserID: "user",
		ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON,
		AgentName: "auto", RuntimeSnapshotID: modelColdSnapshotID, ProviderBindingJSON: publicationBinding,
		RuntimeGeneration: 1, RequestedModel: &requestedModel,
		RuntimeOutcome: string(baldastate.RuntimeOperationOutcomeContextReset), Status: baldastate.SessionStatusActive,
	}
	store := &fakeSessionStore{recordsByID: map[string]baldastate.SessionRecord{record.SessionID: record}}
	operations := &publicationStore{}
	archive := &modelChangeArchive{session: chatarchivecmd.Session{
		ID: record.ArchiveID, StableID: record.SessionID, RuntimeID: record.ArchiveID,
		Status:   chatarchivecmd.StatusActive,
		Location: chatarchivecmd.Location{Channel: record.ChannelType, AddressKey: record.AddressKey},
	}}
	hooks := &modelStartHooks{}
	newManager := func() *Manager {
		return &Manager{
			archive:      archive,
			agentBuilder: builder, runtimeManager: runtimeManager, workingDir: t.TempDir(),
			sessionStore: store, runtimeOperations: operations, logger: zerolog.Nop(),
			hooks:    hooks,
			sessions: make(map[string]*TopicSession), operations: make(map[string]*sessionOperation),
		}
	}
	operations.beforeFinalize = func(outcome baldastate.RuntimeOperationOutcome) {
		if outcome != baldastate.RuntimeOperationOutcomeContextPreserved {
			return
		}
		committed := store.recordsByID[record.SessionID]
		requested := modelChangeTargetModel
		committed.RequestedModel = &requested
		committed.EffectiveModel = modelChangeTargetModel
		committed.RuntimeGeneration++
		committed.RuntimeOutcome = string(outcome)
		store.recordsByID[record.SessionID] = committed
	}
	manager := newManager()
	return coldModelChangeUnitFixture{
		manager: manager, newManager: newManager, control: control,
		hooks: hooks, runtimeManager: runtimeManager, operations: operations, store: store, record: record,
	}
}

func assertColdModelRecordUnchanged(t *testing.T, fixture coldModelChangeUnitFixture) {
	t.Helper()
	got, found := fixture.store.recordsByID[fixture.record.SessionID]
	if !found || !reflect.DeepEqual(got, fixture.record) {
		t.Fatalf("persisted session = %+v, found %t; want unchanged %+v", got, found, fixture.record)
	}
}

func assertColdModelRecordCommitted(t *testing.T, fixture coldModelChangeUnitFixture) {
	t.Helper()
	got, found := fixture.store.recordsByID[fixture.record.SessionID]
	if !found || got.RequestedModel == nil || *got.RequestedModel != modelChangeTargetModel ||
		got.EffectiveModel != modelChangeTargetModel || got.RuntimeGeneration != fixture.record.RuntimeGeneration+1 ||
		got.RuntimeOutcome != string(baldastate.RuntimeOperationOutcomeContextPreserved) {
		t.Fatalf("persisted session = %+v, found %t; want committed model %q at generation %d",
			got, found, modelChangeTargetModel, fixture.record.RuntimeGeneration+1)
	}
}

func assertColdRuntimeLease(
	t *testing.T,
	topic *TopicSession,
	record baldastate.SessionRecord,
	wantModel string,
	wantGeneration uint64,
) {
	t.Helper()
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("AcquireRuntime() error = %v", err)
	}
	defer lease.Release()
	if lease.EffectiveModel() != wantModel || lease.Generation() != wantGeneration ||
		lease.ArchiveID() != record.ArchiveID || lease.SessionID() != record.ArchiveID ||
		lease.RuntimeSnapshotID() != record.RuntimeSnapshotID || lease.ProviderBindingJSON() != record.ProviderBindingJSON {
		t.Fatalf("runtime lease = model %q generation %d archive %q session %q snapshot %q binding %q",
			lease.EffectiveModel(), lease.Generation(), lease.ArchiveID(), lease.SessionID(),
			lease.RuntimeSnapshotID(), lease.ProviderBindingJSON())
	}
	got, err := lease.SessionService().Get(t.Context(), &adksession.GetRequest{
		AppName: lease.AppName(), UserID: lease.UserID(), SessionID: lease.SessionID(),
	})
	if err != nil {
		t.Fatalf("next runtime use: Get() error = %v", err)
	}
	if got == nil || got.Session == nil || got.Session.ID() != lease.Session().ID() {
		t.Fatalf("next runtime use = session %v; want %q", got, lease.Session().ID())
	}
}

func TestChangeModelAfterColdResetUsesExactRuntimeController(t *testing.T) {
	for _, test := range []struct {
		name           string
		validation     agentproduct.ModelValidation
		change         RuntimeModelChange
		wantStatus     ModelChangeResultStatus
		wantCalls      string
		wantOperation  bool
		wantModel      string
		wantGeneration uint64
		wantOutcome    baldastate.RuntimeOperationOutcome
		wantReason     baldastate.RuntimeOperationReason
		wantJournal    string
		wantUnchanged  bool
	}{
		{
			name: "supported alternate",
			validation: agentproduct.ModelValidation{
				Status: agentproduct.ModelSupported, Model: modelChangeTargetModel,
			},
			change:         RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
			wantStatus:     ModelChangeChangedLive,
			wantCalls:      modelControlCallOrder,
			wantOperation:  true,
			wantModel:      modelChangeTargetModel,
			wantGeneration: 2,
			wantOutcome:    baldastate.RuntimeOperationOutcomeContextPreserved,
			wantReason:     baldastate.RuntimeOperationReasonChangedLive,
			wantJournal:    "begin,applying,finalize:context_preserved",
		},
		{
			name:           "unsupported alternate",
			validation:     agentproduct.ModelValidation{Status: agentproduct.ModelUnsupported},
			wantStatus:     ModelChangeUnsupported,
			wantCalls:      modelValidationCallOrder,
			wantGeneration: 1,
			wantReason:     baldastate.RuntimeOperationReasonUnsupported,
			wantUnchanged:  true,
		},
		{
			name: "provider rejects validated alternate",
			validation: agentproduct.ModelValidation{
				Status: agentproduct.ModelSupported, Model: modelChangeTargetModel,
			},
			change:         RuntimeModelChange{Status: agentproduct.ModelRejected},
			wantStatus:     ModelChangeRejected,
			wantCalls:      modelControlCallOrder,
			wantOperation:  true,
			wantGeneration: 1,
			wantOutcome:    baldastate.RuntimeOperationOutcomeAborted,
			wantReason:     baldastate.RuntimeOperationReasonRejected,
			wantJournal:    "begin,applying,finalize:aborted",
			wantUnchanged:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newColdModelChangeUnitFixture(t, test.validation, test.change)
			request := ModelChangeRequest{
				OperationID: "cold-model-change", SessionID: fixture.record.SessionID,
				InputHash: strings.Repeat("d", 64), TargetModel: modelChangeTargetModel,
			}
			result, err := fixture.manager.ChangeModel(t.Context(), request)
			if err != nil || result.Status != test.wantStatus || result.EffectiveModel != test.wantModel ||
				result.Generation != test.wantGeneration || result.Reason != test.wantReason {
				t.Fatalf("ChangeModel() = %+v, %v", result, err)
			}
			if got := strings.Join(fixture.control.callOrder(), ","); got != test.wantCalls {
				t.Fatalf("control order = %q, want %q", got, test.wantCalls)
			}
			if len(fixture.runtimeManager.unpublishedRequests) != 1 {
				t.Fatalf("exact runtime requests = %d, want 1", len(fixture.runtimeManager.unpublishedRequests))
			}
			runtimeRequest := fixture.runtimeManager.unpublishedRequests[0]
			if runtimeRequest.RuntimeSnapshotID != fixture.record.RuntimeSnapshotID ||
				runtimeRequest.ProviderBindingJSON != fixture.record.ProviderBindingJSON ||
				runtimeRequest.ModelOverride == nil || *runtimeRequest.ModelOverride != publicationOriginalModel {
				t.Fatalf("exact runtime request = %+v", runtimeRequest)
			}
			topic := fixture.manager.activeTopicSession(fixture.record.SessionID)
			if topic == nil {
				t.Fatal("exact runtime was not published")
			}
			assertColdRuntimeLease(t, topic, fixture.record, test.wantModel, test.wantGeneration)
			if found := fixture.operations.operation.OperationID != ""; found != test.wantOperation {
				t.Fatalf("model operation found = %t, want %t", found, test.wantOperation)
			}
			if got := strings.Join(fixture.operations.callOrder(), ","); got != test.wantJournal {
				t.Fatalf("model journal calls = %q, want %q", got, test.wantJournal)
			}
			if test.wantUnchanged {
				assertColdModelRecordUnchanged(t, fixture)
			} else {
				assertColdModelRecordCommitted(t, fixture)
			}
			if !test.wantOperation {
				if len(fixture.hooks.events) != 1 || fixture.hooks.events[0].Source != sessionHookResume {
					t.Fatalf("validation resume hooks = %+v, want one resume", fixture.hooks.events)
				}
				return
			}
			operation := fixture.operations.operation
			if operation.State != baldastate.RuntimeOperationTerminal ||
				operation.Outcome != string(test.wantOutcome) || operation.ReasonCode != string(test.wantReason) ||
				operation.TargetRequestedModel == nil || *operation.TargetRequestedModel != modelChangeTargetModel {
				t.Fatalf("model operation = %+v", operation)
			}
			if len(fixture.hooks.events) != 1 || fixture.hooks.events[0].Source != sessionHookResume {
				t.Fatalf("initial resume hooks = %+v, want one resume", fixture.hooks.events)
			}
			restarted := fixture.newManager()
			replayed, err := restarted.ChangeModel(t.Context(), request)
			if err != nil || replayed != result {
				t.Fatalf("restarted ChangeModel() = %+v, %v; want %+v", replayed, err, result)
			}
			if got := strings.Join(fixture.control.callOrder(), ","); got != test.wantCalls {
				t.Fatalf("replay called controller: %q", got)
			}
			if len(fixture.hooks.events) != 2 || fixture.hooks.events[1].Source != sessionHookResume {
				t.Fatalf("restart resume hooks = %+v, want one hook per exact runtime open", fixture.hooks.events)
			}
			if len(fixture.runtimeManager.unpublishedRequests) != 2 {
				t.Fatalf("restart exact runtime requests = %d, want 2", len(fixture.runtimeManager.unpublishedRequests))
			}
			restartedTopic := restarted.activeTopicSession(fixture.record.SessionID)
			if restartedTopic == nil {
				t.Fatal("replay did not restore the exact runtime")
			}
			assertColdRuntimeLease(t, restartedTopic, fixture.record, test.wantModel, test.wantGeneration)
			replayedAgain, err := restarted.ChangeModel(t.Context(), request)
			if err != nil || replayedAgain != result {
				t.Fatalf("second replay = %+v, %v; want %+v", replayedAgain, err, result)
			}
			if got := strings.Join(fixture.control.callOrder(), ","); got != test.wantCalls {
				t.Fatalf("second replay called controller: %q", got)
			}
			if len(fixture.hooks.events) != 2 {
				t.Fatalf("second replay repeated resume hooks: %+v", fixture.hooks.events)
			}
			if len(fixture.runtimeManager.unpublishedRequests) != 2 {
				t.Fatalf("second replay reopened runtime: %d exact requests", len(fixture.runtimeManager.unpublishedRequests))
			}
			if got := strings.Join(fixture.operations.callOrder(), ","); got != test.wantJournal {
				t.Fatalf("replay repeated journal mutation: %q", got)
			}
		})
	}
}

func TestChangeModelColdEntryFailuresDoNotMutateState(t *testing.T) {
	t.Run("exact runtime build failure", func(t *testing.T) {
		fixture := newColdModelChangeUnitFixture(t, agentproduct.ModelValidation{}, RuntimeModelChange{})
		fixture.runtimeManager.sessionErr = errors.New("secret provider failure")
		result, err := fixture.manager.ChangeModel(t.Context(), ModelChangeRequest{
			OperationID: "cold-model-build-failure", SessionID: fixture.record.SessionID,
			InputHash: strings.Repeat("f", 64), TargetModel: modelChangeTargetModel,
		})
		if err != nil || result.Status != ModelChangeUnavailable || result.Reason != baldastate.RuntimeOperationReasonUnavailable {
			t.Fatalf("ChangeModel() = %+v, %v", result, err)
		}
		if fixture.manager.activeTopicSession(fixture.record.SessionID) != nil {
			t.Fatal("failed exact runtime was published")
		}
		if fixture.operations.operation.OperationID != "" || len(fixture.control.callOrder()) != 0 || len(fixture.hooks.events) != 0 {
			t.Fatalf("failed exact open side effects = operation %+v, control %v, hooks %+v",
				fixture.operations.operation, fixture.control.callOrder(), fixture.hooks.events)
		}
		assertColdModelRecordUnchanged(t, fixture)
	})

	t.Run("exact runtime binding mismatch aborts candidate", func(t *testing.T) {
		fixture := newColdModelChangeUnitFixture(t, agentproduct.ModelValidation{}, RuntimeModelChange{})
		var closes atomic.Int32
		fixture.runtimeManager.sessionRuntime.ProviderBindingJSON = `{"RootProviderID":"other"}`
		fixture.runtimeManager.unpublishedClose = func() error { closes.Add(1); return nil }
		result, err := fixture.manager.ChangeModel(t.Context(), ModelChangeRequest{
			OperationID: "cold-model-binding-mismatch", SessionID: fixture.record.SessionID,
			InputHash: strings.Repeat("b", 64), TargetModel: modelChangeTargetModel,
		})
		if err != nil || result.Status != ModelChangeUnavailable || closes.Load() != 1 {
			t.Fatalf("ChangeModel() = %+v, %v; candidate closes = %d", result, err, closes.Load())
		}
		if fixture.runtimeManager.lastCandidate == nil || !fixture.runtimeManager.lastCandidate.aborted ||
			fixture.manager.activeTopicSession(fixture.record.SessionID) != nil || fixture.operations.operation.OperationID != "" {
			t.Fatalf("mismatched candidate = %+v, active topic %v, operation %+v",
				fixture.runtimeManager.lastCandidate, fixture.manager.activeTopicSession(fixture.record.SessionID), fixture.operations.operation)
		}
		assertColdModelRecordUnchanged(t, fixture)
	})

	t.Run("model controller absent", func(t *testing.T) {
		fixture := newColdModelChangeUnitFixture(t, agentproduct.ModelValidation{}, RuntimeModelChange{})
		fixture.runtimeManager.sessionRuntime.ModelController = nil
		result, err := fixture.manager.ChangeModel(t.Context(), ModelChangeRequest{
			OperationID: "cold-model-no-controller", SessionID: fixture.record.SessionID,
			InputHash: strings.Repeat("c", 64), TargetModel: modelChangeTargetModel,
		})
		if err != nil || result.Status != ModelChangeUnsupported || result.Reason != baldastate.RuntimeOperationReasonUnsupported ||
			result.Generation != fixture.record.RuntimeGeneration {
			t.Fatalf("ChangeModel() = %+v, %v", result, err)
		}
		if fixture.operations.operation.OperationID != "" || len(fixture.control.callOrder()) != 0 {
			t.Fatalf("absent controller side effects = operation %+v, control %v",
				fixture.operations.operation, fixture.control.callOrder())
		}
		if len(fixture.hooks.events) != 1 || fixture.hooks.events[0].Source != sessionHookResume {
			t.Fatalf("absent controller resume hooks = %+v, want one resume", fixture.hooks.events)
		}
		assertColdModelRecordUnchanged(t, fixture)
		topic := fixture.manager.activeTopicSession(fixture.record.SessionID)
		if topic == nil {
			t.Fatal("exact runtime without model control was not retained")
		}
		assertColdRuntimeLease(t, topic, fixture.record, "", fixture.record.RuntimeGeneration)
	})
}

func TestChangeModelColdResumeHookDenialClosesUnpublishedRuntime(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.RuntimeSnapshotID = modelColdSnapshotID
	f.runtime.RecipeModel = publicationOriginalModel
	initial := f.manager()
	initial.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := initial.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
	if err != nil || !found {
		t.Fatalf("persisted record = %+v, %t, %v", record, found, err)
	}

	var closes atomic.Int32
	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
	runtimeManager.unpublishedClose = func() error { closes.Add(1); return nil }
	runtimeManager.runtimeForSession = func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
		runtime := *f.runtime
		runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
		return &runtime, nil
	}
	hooks := &modelStartHooks{err: hookcmd.ErrDenied}
	restarted.hooks = hooks
	request := ModelChangeRequest{
		OperationID: "cold-model-hook-denial", SessionID: record.SessionID,
		InputHash: strings.Repeat("e", 64), TargetModel: modelChangeTargetModel,
	}
	result, err := restarted.ChangeModel(t.Context(), request)
	if err != nil || result.Status != ModelChangeUnavailable {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if closes.Load() != 1 || runtimeManager.lastCandidate == nil || !runtimeManager.lastCandidate.aborted || restarted.activeTopicSession(record.SessionID) != nil {
		t.Fatalf("denied runtime close/visibility = %d/%v", closes.Load(), restarted.activeTopicSession(record.SessionID))
	}
	if len(hooks.events) != 1 || hooks.events[0].Source != sessionHookResume || hooks.events[0].AgentSessionID == "" {
		t.Fatalf("resume hooks = %+v", hooks.events)
	}
	if _, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID); err != nil || found {
		t.Fatalf("operation after denied hook = found %t, err %v", found, err)
	}
}

func TestChangeModelValidationOutcomesAreUnjournaledNoops(t *testing.T) {
	for _, test := range []struct {
		status agentproduct.ModelValidationStatus
		want   ModelChangeResultStatus
	}{
		{agentproduct.ModelUnsupported, ModelChangeUnsupported},
		{agentproduct.ModelIncompatible, ModelChangeIncompatible},
		{agentproduct.ModelBusy, ModelChangeBusy},
		{agentproduct.ModelFailed, ModelChangeUnavailable},
		{agentproduct.ModelClosed, ModelChangeUnavailable},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			control := &modelTestController{validation: agentproduct.ModelValidation{Status: test.status}}
			_, manager, store, _, _ := modelChangeFixture(t, control, nil)
			result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if err != nil || result.Status != test.want || result.Generation != 1 || result.EffectiveModel != publicationOriginalModel {
				t.Fatalf("ChangeModel() = %+v, %v", result, err)
			}
			if got := strings.Join(control.callOrder(), ","); got != modelValidationCallOrder {
				t.Fatalf("control order = %q", got)
			}
			if len(store.callOrder()) != 0 {
				t.Fatalf("journal calls = %v", store.callOrder())
			}
		})
	}
}

func TestChangeModelDefinitiveOutcomesTerminalizeNoop(t *testing.T) {
	for _, test := range []struct {
		status agentproduct.ModelChangeStatus
		want   ModelChangeResultStatus
		reason baldastate.RuntimeOperationReason
	}{
		{agentproduct.ModelChangeUnsupported, ModelChangeUnsupported, baldastate.RuntimeOperationReasonUnsupported},
		{agentproduct.ModelChangeIncompatible, ModelChangeIncompatible, baldastate.RuntimeOperationReasonIncompatible},
		{agentproduct.ModelRejected, ModelChangeRejected, baldastate.RuntimeOperationReasonRejected},
		{agentproduct.ModelChangeBusy, ModelChangeBusy, baldastate.RuntimeOperationReasonBusy},
		{agentproduct.ModelChangeFailed, ModelChangeUnavailable, baldastate.RuntimeOperationReasonPreDispatch},
		{agentproduct.ModelChangeClosed, ModelChangeUnavailable, baldastate.RuntimeOperationReasonClosed},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			control := &modelTestController{
				validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
				change:     RuntimeModelChange{Status: test.status},
			}
			_, manager, store, _, _ := modelChangeFixture(t, control, nil)
			result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if err != nil || result.Status != test.want || result.Reason != test.reason || result.Generation != 1 {
				t.Fatalf("ChangeModel() = %+v, %v", result, err)
			}
			if store.operation.State != baldastate.RuntimeOperationTerminal ||
				store.operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) || store.operation.ReasonCode != string(test.reason) {
				t.Fatalf("terminal operation = %+v", store.operation)
			}
			replayed, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if err != nil || replayed != result {
				t.Fatalf("redelivery = %+v, %v", replayed, err)
			}
			if got := strings.Join(control.callOrder(), ","); got != modelControlCallOrder {
				t.Fatalf("redelivery called control: %q", got)
			}
		})
	}
}

func TestChangeModelAmbiguousRemainsApplyingAndFences(t *testing.T) {
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelAmbiguous},
	}
	topic, manager, store, _, _ := modelChangeFixture(t, control, nil)

	result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || result.Status != ModelChangeRecoveryRequired || result.Reason != baldastate.RuntimeOperationReasonUnavailable {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if store.operation.State != baldastate.RuntimeOperationApplying || store.operation.TerminalAt != nil {
		t.Fatalf("operation = %+v", store.operation)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v", err)
	}
	if _, err := manager.ChangeModel(t.Context(), modelChangeRequest()); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("redelivery = %v", err)
	}
	if got := strings.Join(control.callOrder(), ","); got != modelControlCallOrder {
		t.Fatalf("ambiguous operation retried: %q", got)
	}
}

func TestChangeModelReplacementPreservesExactSession(t *testing.T) {
	control := &modelTestController{validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported}}
	var incumbentCloses atomic.Int32
	topic, manager, _, service, sess := modelChangeFixture(t, control, func() error { incumbentCloses.Add(1); return nil })
	incumbent := topic.currentRuntime()
	var candidateCloses atomic.Int32
	control.change = RuntimeModelChange{
		Status: agentproduct.ModelReplacement, EffectiveModel: modelChangeTargetModel,
		Candidate: &BuiltRuntime{
			SessionSvc: service, AppName: sess.AppName(), RuntimeSnapshotID: "snapshot",
			ProviderBindingJSON: publicationBinding, ConfirmedModel: modelChangeTargetModel,
			Close: func() error { candidateCloses.Add(1); return nil },
		},
		Identity: RuntimeSessionIdentity{SessionService: service, AppName: sess.AppName(), UserID: sess.UserID(), SessionID: sess.ID()},
	}

	result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
	if err != nil || result.Status != ModelChangeChangedReplacement ||
		result.Reason != baldastate.RuntimeOperationReasonChangedReplacement || result.Generation != 2 {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.SessionService() != service || lease.Session() != sess || lease.AppName() != sess.AppName() ||
		lease.UserID() != sess.UserID() || lease.SessionID() != sess.ID() {
		t.Fatal("replacement changed ADK session identity")
	}
	if value, found, err := lease.RuntimeStateValue(t.Context(), "history"); err != nil || !found || value != "kept" {
		t.Fatalf("preserved state = %v, %t, %v", value, found, err)
	}
	if candidateCloses.Load() != 0 {
		t.Fatal("published candidate closed")
	}
	if err := incumbent.waitDrain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if incumbentCloses.Load() != 1 {
		t.Fatalf("incumbent close count = %d, want 1", incumbentCloses.Load())
	}
	replayed, err := manager.ChangeModel(t.Context(), modelChangeRequest())
	if err != nil || replayed != result {
		t.Fatalf("redelivery = %+v, %v", replayed, err)
	}
	if got := strings.Join(control.callOrder(), ","); got != modelControlCallOrder {
		t.Fatalf("redelivery called control: %q", got)
	}
	lease.Release()
	if err := topic.DrainRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if candidateCloses.Load() != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidateCloses.Load())
	}
}

func TestChangeModelInvalidReplacementRemainsApplyingAndCleansCandidate(t *testing.T) {
	var closes atomic.Int32
	control := &modelTestController{validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported}}
	topic, manager, store, service, sess := modelChangeFixture(t, control, nil)
	control.change = RuntimeModelChange{
		Status: agentproduct.ModelReplacement,
		Candidate: &BuiltRuntime{
			SessionSvc: service, AppName: sess.AppName(), RuntimeSnapshotID: "wrong",
			ProviderBindingJSON: publicationBinding, Close: func() error { closes.Add(1); return errors.New("secret provider detail") },
		},
		Identity: RuntimeSessionIdentity{SessionService: service, AppName: sess.AppName(), UserID: sess.UserID(), SessionID: sess.ID()},
	}

	result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || result.Status != ModelChangeRecoveryRequired ||
		strings.Contains(err.Error(), "secret") || result.Reason != baldastate.RuntimeOperationReasonUnavailable {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if closes.Load() != 1 || store.operation.State != baldastate.RuntimeOperationApplying || store.operation.TerminalAt != nil {
		t.Fatalf("cleanup=%d operation=%+v", closes.Load(), store.operation)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v", err)
	}
}

func TestChangeModelOperationIdentityConflictsDoNotFence(t *testing.T) {
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
	if _, err := manager.ChangeModel(t.Context(), modelChangeRequest()); err != nil {
		t.Fatal(err)
	}
	beforeCalls := strings.Join(control.callOrder(), ",")
	beforeJournal := strings.Join(store.callOrder(), ",")

	collision := modelChangeRequest()
	collision.InputHash = strings.Repeat("b", 64)
	collision.TargetModel = modelChangeOtherModel
	result, err := manager.ChangeModel(t.Context(), collision)
	if err != nil || result.Status != ModelChangeConflict || result.Reason != baldastate.RuntimeOperationReasonInvalid {
		t.Fatalf("collision = %+v, %v", result, err)
	}
	if got := strings.Join(control.callOrder(), ","); got != beforeCalls {
		t.Fatalf("collision called provider: %q", got)
	}
	if got := strings.Join(store.callOrder(), ","); got != beforeJournal {
		t.Fatalf("collision mutated journal: %q", got)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("collision fenced runtime: %v", err)
	}
	lease.Release()
}

func TestChangeModelCrossSessionOperationCollisionHasZeroProviderWork(t *testing.T) {
	controlA := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topicA, manager, storeA, serviceA, sessionA := modelChangeFixture(t, controlA, nil)
	_ = topicA
	_ = storeA
	_ = serviceA
	_ = sessionA
	if _, err := manager.ChangeModel(t.Context(), modelChangeRequest()); err != nil {
		t.Fatal(err)
	}
	controlB := &modelTestController{validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported}}
	topicB := addModelChangeTopic(t, manager, "session-b", controlB)

	request := ModelChangeRequest{
		OperationID: "model-operation", SessionID: "session-b", InputHash: strings.Repeat("b", 64), TargetModel: modelChangeTargetModel,
	}
	result, err := manager.ChangeModel(t.Context(), request)
	if err != nil || result.Status != ModelChangeConflict {
		t.Fatalf("cross-session collision = %+v, %v", result, err)
	}
	if len(controlB.callOrder()) != 0 {
		t.Fatalf("cross-session collision called provider: %v", controlB.callOrder())
	}
	lease, err := topicB.AcquireRuntime()
	if err != nil {
		t.Fatalf("cross-session collision fenced runtime: %v", err)
	}
	lease.Release()
}

func TestChangeModelConcurrentOperationCollisionHasOneWinner(t *testing.T) {
	controlA := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topicA, manager, store, serviceA, sessionA := modelChangeFixture(t, controlA, nil)
	_ = topicA
	_ = serviceA
	_ = sessionA
	controlB := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: "model-b"},
	}
	addModelChangeTopic(t, manager, "session-b", controlB)
	requests := []ModelChangeRequest{
		modelChangeRequest(),
		{OperationID: "model-operation", SessionID: "session-b", InputHash: strings.Repeat("b", 64), TargetModel: "model-b"},
	}
	start := make(chan struct{})
	results := make(chan ModelChangeResult, 2)
	var calls sync.WaitGroup
	for _, request := range requests {
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			result, _ := manager.ChangeModel(t.Context(), request)
			results <- result
		}()
	}
	close(start)
	calls.Wait()
	close(results)
	statuses := map[ModelChangeResultStatus]int{}
	for result := range results {
		statuses[result.Status]++
	}
	if statuses[ModelChangeChangedLive] != 1 || statuses[ModelChangeConflict] != 1 {
		t.Fatalf("statuses = %v", statuses)
	}
	providerCalls := len(controlA.callOrder()) + len(controlB.callOrder())
	if providerCalls != 2 { // one validation plus one change for the winner
		t.Fatalf("provider calls = %d, want 2", providerCalls)
	}
	if store.successfulFinalizations() != 1 {
		t.Fatalf("successful finalizations = %d", store.successfulFinalizations())
	}
}

func TestChangeModelConcurrentExactRedeliveryReturnsOneSemanticResult(t *testing.T) {
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topic, manager, store, service, sess := modelChangeFixture(t, control, nil)
	_ = topic
	_ = service
	_ = sess
	start := make(chan struct{})
	results := make(chan ModelChangeResult, 2)
	var calls sync.WaitGroup
	for range 2 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			result, _ := manager.ChangeModel(t.Context(), modelChangeRequest())
			results <- result
		}()
	}
	close(start)
	calls.Wait()
	close(results)
	for result := range results {
		if result.Status != ModelChangeChangedLive || result.Reason != baldastate.RuntimeOperationReasonChangedLive || result.Generation != 2 {
			t.Fatalf("result = %+v", result)
		}
	}
	if got := strings.Join(control.callOrder(), ","); got != modelControlCallOrder {
		t.Fatalf("provider calls = %q", got)
	}
	if store.successfulFinalizations() != 1 {
		t.Fatalf("successful finalizations = %d", store.successfulFinalizations())
	}
}

func TestChangeModelPreBoundaryStoreFailuresDoNotCallProviderOrFence(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*publicationStore)
		wantStatus ModelChangeResultStatus
		wantCalls  string
	}{
		{name: "get unavailable", configure: func(store *publicationStore) { store.getErr = baldastate.ErrRuntimeOperationUnavailable }, wantStatus: ModelChangeUnavailable},
		{name: "begin conflict", configure: func(store *publicationStore) { store.beginErr = baldastate.ErrRuntimeOperationConflict }, wantStatus: ModelChangeConflict, wantCalls: modelValidationCallOrder},
		{name: "begin unavailable", configure: func(store *publicationStore) { store.beginErr = baldastate.ErrRuntimeOperationUnavailable }, wantStatus: ModelChangeUnavailable, wantCalls: modelValidationCallOrder},
		{name: "mark conflict", configure: func(store *publicationStore) { store.markApplyingErr = baldastate.ErrRuntimeOperationConflict }, wantStatus: ModelChangeConflict, wantCalls: modelValidationCallOrder},
		{name: "mark unavailable", configure: func(store *publicationStore) { store.markApplyingErr = baldastate.ErrRuntimeOperationUnavailable }, wantStatus: ModelChangeUnavailable, wantCalls: modelValidationCallOrder},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &modelTestController{
				validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
				change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
			}
			topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
			test.configure(store)
			result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if err != nil || result.Status != test.wantStatus {
				t.Fatalf("ChangeModel() = %+v, %v", result, err)
			}
			if got := strings.Join(control.callOrder(), ","); got != test.wantCalls {
				t.Fatalf("provider calls = %q, want %q", got, test.wantCalls)
			}
			lease, err := topic.AcquireRuntime()
			if err != nil {
				t.Fatalf("pre-boundary failure fenced runtime: %v", err)
			}
			lease.Release()
		})
	}
}

func TestChangeModelBeginFailureUsesAuthoritativeReread(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*publicationStore)
		wantStatus ModelChangeResultStatus
		wantState  baldastate.RuntimeOperationState
		wantFound  bool
		wantFence  bool
	}{
		{
			name: "not committed", wantStatus: ModelChangeUnavailable,
			configure: func(store *publicationStore) { store.beginErr = baldastate.ErrRuntimeOperationUnavailable },
		},
		{
			name: "committed prepared", wantStatus: ModelChangeUnavailable, wantState: baldastate.RuntimeOperationPrepared, wantFound: true,
			configure: func(store *publicationStore) {
				store.beginErr = baldastate.ErrRuntimeOperationUnavailable
				store.beginCommitErr = true
			},
		},
		{
			name: "reread unavailable", wantStatus: ModelChangeRecoveryRequired, wantFence: true,
			configure: func(store *publicationStore) {
				store.beginErr = baldastate.ErrRuntimeOperationUnavailable
				store.getErrAfterBegin = baldastate.ErrRuntimeOperationUnavailable
			},
		},
		{
			name: "contradictory applying", wantStatus: ModelChangeRecoveryRequired, wantState: baldastate.RuntimeOperationApplying, wantFound: true, wantFence: true,
			configure: func(store *publicationStore) {
				store.beginErr = baldastate.ErrRuntimeOperationUnavailable
				store.beginErrorRecord = func(intent baldastate.RuntimeOperationRecord) baldastate.RuntimeOperationRecord {
					intent.State = baldastate.RuntimeOperationApplying
					return intent
				}
			},
		},
		{
			name: "terminal noop", wantStatus: ModelChangeUnsupported, wantState: baldastate.RuntimeOperationTerminal, wantFound: true,
			configure: func(store *publicationStore) {
				store.beginErr = baldastate.ErrRuntimeOperationUnavailable
				store.beginErrorRecord = func(intent baldastate.RuntimeOperationRecord) baldastate.RuntimeOperationRecord {
					return terminalPublicationOperation(intent, baldastate.RuntimeOperationOutcomeAborted, baldastate.RuntimeOperationReasonUnsupported)
				}
			},
		},
		{
			name: "different committed intent", wantStatus: ModelChangeConflict, wantState: baldastate.RuntimeOperationPrepared, wantFound: true,
			configure: func(store *publicationStore) {
				store.beginErr = baldastate.ErrRuntimeOperationConflict
				store.beginErrorRecord = func(intent baldastate.RuntimeOperationRecord) baldastate.RuntimeOperationRecord {
					intent.SessionID = "other-session"
					return intent
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &modelTestController{
				validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
				change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
			}
			topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
			test.configure(store)
			result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if test.wantFence {
				if !errors.Is(err, ErrRuntimeRecoveryRequired) {
					t.Fatalf("ChangeModel() error = %v", err)
				}
			} else if err != nil {
				t.Fatalf("ChangeModel() error = %v", err)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("result = %+v, want %s", result, test.wantStatus)
			}
			if got := strings.Join(control.callOrder(), ","); got != modelValidationCallOrder {
				t.Fatalf("provider calls = %q", got)
			}
			if found := store.operation.OperationID != ""; found != test.wantFound || store.operation.State != test.wantState {
				t.Fatalf("operation = %+v, want found=%t state=%s", store.operation, test.wantFound, test.wantState)
			}
			assertModelFence(t, topic, test.wantFence)
		})
	}
}

func TestChangeModelMarkApplyingFailureUsesAuthoritativeReread(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*publicationStore)
		wantStatus ModelChangeResultStatus
		wantState  baldastate.RuntimeOperationState
		wantFence  bool
	}{
		{
			name: "not committed prepared", wantStatus: ModelChangeUnavailable, wantState: baldastate.RuntimeOperationPrepared,
			configure: func(store *publicationStore) { store.markApplyingErr = baldastate.ErrRuntimeOperationUnavailable },
		},
		{
			name: "committed applying", wantStatus: ModelChangeRecoveryRequired, wantState: baldastate.RuntimeOperationApplying, wantFence: true,
			configure: func(store *publicationStore) {
				store.markApplyingErr = baldastate.ErrRuntimeOperationUnavailable
				store.markCommitErr = true
			},
		},
		{
			name: "reread unavailable", wantStatus: ModelChangeRecoveryRequired, wantState: baldastate.RuntimeOperationPrepared, wantFence: true,
			configure: func(store *publicationStore) {
				store.markApplyingErr = baldastate.ErrRuntimeOperationUnavailable
				store.getErrAfterMark = baldastate.ErrRuntimeOperationUnavailable
			},
		},
		{
			name: "terminal noop", wantStatus: ModelChangeUnsupported, wantState: baldastate.RuntimeOperationTerminal,
			configure: func(store *publicationStore) {
				store.markApplyingErr = baldastate.ErrRuntimeOperationUnavailable
				store.markErrorRecord = func(operation baldastate.RuntimeOperationRecord) baldastate.RuntimeOperationRecord {
					return terminalPublicationOperation(operation, baldastate.RuntimeOperationOutcomeAborted, baldastate.RuntimeOperationReasonUnsupported)
				}
			},
		},
		{
			name: "contradictory intent", wantStatus: ModelChangeRecoveryRequired, wantState: baldastate.RuntimeOperationPrepared, wantFence: true,
			configure: func(store *publicationStore) {
				store.markApplyingErr = baldastate.ErrRuntimeOperationConflict
				store.markErrorRecord = func(operation baldastate.RuntimeOperationRecord) baldastate.RuntimeOperationRecord {
					operation.TargetModel = modelChangeOtherModel
					return operation
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &modelTestController{
				validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
				change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
			}
			topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
			test.configure(store)
			result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if test.wantFence {
				if !errors.Is(err, ErrRuntimeRecoveryRequired) {
					t.Fatalf("ChangeModel() error = %v", err)
				}
			} else if err != nil {
				t.Fatalf("ChangeModel() error = %v", err)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("result = %+v, want %s", result, test.wantStatus)
			}
			if got := strings.Join(control.callOrder(), ","); got != modelValidationCallOrder {
				t.Fatalf("provider calls = %q", got)
			}
			if store.operation.State != test.wantState {
				t.Fatalf("operation = %+v, want state=%s", store.operation, test.wantState)
			}
			assertModelFence(t, topic, test.wantFence)
		})
	}
}

func TestChangeModelCommitUncertaintyDoesNotExposeStoreDetails(t *testing.T) {
	const secret = "store-token=private-model-secret"
	tests := []struct {
		name       string
		configure  func(*publicationStore)
		wantStatus ModelChangeResultStatus
		wantState  baldastate.RuntimeOperationState
		wantFound  bool
		wantFence  bool
	}{
		{
			name: "begin not committed", wantStatus: ModelChangeUnavailable,
			configure: func(store *publicationStore) { store.beginErr = errors.New(secret) },
		},
		{
			name: "begin committed prepared", wantStatus: ModelChangeUnavailable,
			wantState: baldastate.RuntimeOperationPrepared, wantFound: true,
			configure: func(store *publicationStore) {
				store.beginErr = errors.New(secret)
				store.beginCommitErr = true
			},
		},
		{
			name: "begin reread unavailable", wantStatus: ModelChangeRecoveryRequired,
			wantFence: true,
			configure: func(store *publicationStore) {
				store.beginErr = errors.New(secret)
				store.getErrAfterBegin = errors.New(secret)
			},
		},
		{
			name: "mark not committed", wantStatus: ModelChangeUnavailable,
			wantState: baldastate.RuntimeOperationPrepared, wantFound: true,
			configure: func(store *publicationStore) { store.markApplyingErr = errors.New(secret) },
		},
		{
			name: "mark committed applying", wantStatus: ModelChangeRecoveryRequired,
			wantState: baldastate.RuntimeOperationApplying, wantFound: true, wantFence: true,
			configure: func(store *publicationStore) {
				store.markApplyingErr = errors.New(secret)
				store.markCommitErr = true
			},
		},
		{
			name: "mark reread unavailable", wantStatus: ModelChangeRecoveryRequired,
			wantState: baldastate.RuntimeOperationPrepared, wantFound: true, wantFence: true,
			configure: func(store *publicationStore) {
				store.markApplyingErr = errors.New(secret)
				store.getErrAfterMark = errors.New(secret)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &modelTestController{
				validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
				change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
			}
			topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
			test.configure(store)

			result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
			if result.Status != test.wantStatus {
				t.Fatalf("ChangeModel() result = %+v, want status %s", result, test.wantStatus)
			}
			if test.wantFence {
				if !errors.Is(err, ErrRuntimeRecoveryRequired) || err.Error() != ErrRuntimeRecoveryRequired.Error() {
					t.Fatalf("ChangeModel() error = %v, want bounded recovery error", err)
				}
			} else if err != nil {
				t.Fatalf("ChangeModel() error = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("ChangeModel() exposed store detail: %v", err)
			}
			if got := strings.Join(control.callOrder(), ","); got != modelValidationCallOrder {
				t.Fatalf("provider calls = %q, want validation only", got)
			}
			if found := store.operation.OperationID != ""; found != test.wantFound || store.operation.State != test.wantState {
				t.Fatalf("operation = %+v, want found=%t state=%s", store.operation, test.wantFound, test.wantState)
			}
			assertModelFence(t, topic, test.wantFence)
		})
	}
}

func assertModelFence(t *testing.T, topic *TopicSession, wantFence bool) {
	t.Helper()
	lease, err := topic.AcquireRuntime()
	if wantFence {
		if !errors.Is(err, ErrRuntimeRecoveryRequired) {
			t.Fatalf("AcquireRuntime() = %v, want recovery fence", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("AcquireRuntime() = %v", err)
	}
	lease.Release()
}

func TestChangeModelPublicationConflictRemainsApplyingAndFenced(t *testing.T) {
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
	store.publishErr = baldastate.ErrRuntimeOperationConflict

	result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || result.Status != ModelChangeRecoveryRequired {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if store.operation.State != baldastate.RuntimeOperationApplying || store.operation.TerminalAt != nil {
		t.Fatalf("operation = %+v", store.operation)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v", err)
	}
}

func TestChangeModelStalePublicationGuardRemainsApplyingAndFenced(t *testing.T) {
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	topic, manager, store, _, _ := modelChangeFixture(t, control, nil)
	control.changeHook = func() {
		topic.runtimeMu.Lock()
		topic.runtime = newPublishedRuntimeSlot(runtimeSlotConfig{
			runtime:   &BuiltRuntime{RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding},
			archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel,
		})
		topic.runtimeMu.Unlock()
	}

	result, err := manager.ChangeModel(t.Context(), modelChangeRequest())
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || result.Status != ModelChangeRecoveryRequired {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if store.operation.State != baldastate.RuntimeOperationApplying || store.operation.TerminalAt != nil {
		t.Fatalf("operation = %+v", store.operation)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v", err)
	}
}

func modelChangeFixture(
	t *testing.T,
	control RuntimeModelController,
	closeFn func() error,
) (*TopicSession, *Manager, *publicationStore, adksession.Service, adksession.Session) {
	t.Helper()
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: "app", UserID: "user", SessionID: "session", State: map[string]any{"history": "kept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &BuiltRuntime{
		ModelController: control, SessionSvc: service, AppName: "app",
		RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding, Close: closeFn,
	}
	slot := newRuntimeSlot(runtimeSlotConfig{
		runtime: runtime, session: created.Session, archiveID: publicationArchiveID,
		generation: 1, effectiveModel: publicationOriginalModel, outcome: publicationOutcome,
	})
	topic := &TopicSession{sessionID: "session", runtime: slot}
	store := &publicationStore{}
	return topic, publicationManager(topic, store), store, service, created.Session
}

func addModelChangeTopic(t *testing.T, manager *Manager, sessionID string, control RuntimeModelController) *TopicSession {
	t.Helper()
	service := adksession.InMemoryService()
	created, err := service.Create(t.Context(), &adksession.CreateRequest{AppName: "app", UserID: "user", SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	topic := &TopicSession{
		sessionID: sessionID,
		runtime: newPublishedRuntimeSlot(runtimeSlotConfig{
			runtime: &BuiltRuntime{
				ModelController: control, SessionSvc: service, AppName: "app",
				RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding,
			},
			session: created.Session, archiveID: publicationArchiveID,
			generation: 1, effectiveModel: publicationOriginalModel, outcome: publicationOutcome,
		}),
	}
	manager.mu.Lock()
	manager.sessions[sessionID] = topic
	manager.mu.Unlock()
	return topic
}

func modelChangeRequest() ModelChangeRequest {
	return ModelChangeRequest{
		OperationID: "model-operation", SessionID: "session",
		InputHash: strings.Repeat("a", 64), TargetModel: modelChangeTargetModel,
	}
}
