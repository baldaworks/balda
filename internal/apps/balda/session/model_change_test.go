package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
	adksession "google.golang.org/adk/v2/session"
)

const modelColdSnapshotID = "snapshot"

type modelStartHooks struct {
	events []hookcmd.Event
	err    error
}

type failBeginRuntimeOperations struct {
	baldastate.RuntimeOperationStore
}

func (s failBeginRuntimeOperations) Begin(context.Context, baldastate.RuntimeOperationRecord) (baldastate.RuntimeOperationRecord, error) {
	return baldastate.RuntimeOperationRecord{}, errors.New("synthetic pre-commit failure")
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
		ModelChangeChangedLive, ModelChangeChangedReplacement, ModelChangePending,
		ModelChangeUnsupported, ModelChangeIncompatible, ModelChangeRejected, ModelChangeBusy,
		ModelChangeConflict, ModelChangeUnavailable, ModelChangeRecoveryRequired,
	}
	want := []string{
		"changed_live", "changed_replacement", "pending", "unsupported", "incompatible", "rejected", "busy",
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

func TestChangeModelColdPostResetChangesPreferenceWithoutProviderRuntime(t *testing.T) {
	f := newArchiveFixture(t)
	f.builder.metadata.Model = publicationOriginalModel
	control := &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelSupported, Model: modelChangeTargetModel},
		change:     RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: modelChangeTargetModel},
	}
	f.runtime.ModelController = control
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.RuntimeSnapshotID = modelColdSnapshotID
	f.runtime.RecipeModel = publicationOriginalModel

	initial := f.manager()
	initial.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := initial.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := initial.PrepareFreshLifetime(t.Context(), FreshLifetimeRequest{
		OperationID: "model-cold-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.PublishFreshLifetime(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
	if err != nil || !found || record.ArchiveID == "" || record.ArchiveID == record.SessionID {
		t.Fatalf("post-reset record = %+v, %t, %v", record, found, err)
	}

	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	hooks := &modelStartHooks{}
	restarted.hooks = hooks
	result, err := restarted.ChangeModel(t.Context(), ModelChangeRequest{
		OperationID: "cold-model-change", SessionID: record.SessionID,
		InputHash: strings.Repeat("d", 64), TargetModel: publicationOriginalModel,
	})
	if err != nil || result.Status != ModelChangePending || result.EffectiveModel != "" {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	active := restarted.activeTopicSession(record.SessionID)
	if active != nil {
		t.Fatalf("cold preference published runtime = %v", active)
	}
	runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
	if len(runtimeManager.unpublishedRequests) != 0 {
		t.Fatalf("runtime requests = %+v, want none", runtimeManager.unpublishedRequests)
	}
	if len(hooks.events) != 0 {
		t.Fatalf("cold preference hooks = %+v", hooks.events)
	}
	changed, found, err := f.provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
	if err != nil || !found || changed.ArchiveID != record.ArchiveID || changed.RuntimeGeneration != record.RuntimeGeneration+1 ||
		changed.RequestedModel != nil || changed.EffectiveModel != "" ||
		changed.RuntimeOutcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("changed preference = %+v, %t, %v", changed, found, err)
	}
	f.builder.metadata.Model = modelChangeOtherModel
	replayed, err := restarted.ChangeModel(t.Context(), ModelChangeRequest{
		OperationID: "cold-model-change", SessionID: record.SessionID,
		InputHash: strings.Repeat("d", 64), TargetModel: publicationOriginalModel,
	})
	if err != nil || replayed != result {
		t.Fatalf("terminal replay after configuration change = %+v, %v; want %+v", replayed, err, result)
	}
}

func TestChangeModelColdPostResetRejectsUnconfiguredTargetWithoutMutation(t *testing.T) {
	f := newArchiveFixture(t)
	f.builder.metadata.Model = publicationOriginalModel
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.RuntimeSnapshotID = modelColdSnapshotID
	f.runtime.RecipeModel = publicationOriginalModel

	initial := f.manager()
	initial.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := initial.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := initial.PrepareFreshLifetime(t.Context(), FreshLifetimeRequest{
		OperationID: "model-cold-reject-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.PublishFreshLifetime(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	before, found, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
	if err != nil || !found {
		t.Fatalf("post-reset record = %+v, %t, %v", before, found, err)
	}

	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	request := ModelChangeRequest{
		OperationID: "cold-model-reject", SessionID: before.SessionID,
		InputHash: strings.Repeat("d", 64), TargetModel: modelChangeTargetModel,
	}
	result, err := restarted.ChangeModel(t.Context(), request)
	if err != nil || result.Status != ModelChangeUnavailable {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	after, found, err := f.provider.Sessions().GetBySessionID(t.Context(), before.SessionID)
	if err != nil || !found || after != before {
		t.Fatalf("record after rejection = %+v, %t, %v; want %+v", after, found, err, before)
	}
	if _, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID); err != nil || found {
		t.Fatalf("operation after rejection = found %t, err %v", found, err)
	}
}

func TestChangeModelRepairsUnattachedFalseEffectivePreference(t *testing.T) {
	f := newArchiveFixture(t)
	f.builder.metadata.Model = publicationOriginalModel
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.RuntimeSnapshotID = modelColdSnapshotID
	f.runtime.RecipeModel = publicationOriginalModel
	f.runtime.ModelController = &modelTestController{
		validation: agentproduct.ModelValidation{Status: agentproduct.ModelUnsupported},
	}

	initial := f.manager()
	initial.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := initial.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := initial.PrepareFreshLifetime(t.Context(), FreshLifetimeRequest{
		OperationID: "model-poison-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.PublishFreshLifetime(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
	if err != nil || !found {
		t.Fatalf("post-reset record = %+v, %t, %v", record, found, err)
	}
	badModel := modelChangeTargetModel
	now := time.Now().UTC()
	poison := baldastate.RuntimeOperationRecord{
		OperationID: "legacy-false-effective", SessionID: record.SessionID, InputHash: strings.Repeat("b", 64),
		Kind: string(baldastate.RuntimeOperationKindModel), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: record.ArchiveID, ExpectedGeneration: record.RuntimeGeneration,
		TargetArchiveID: record.ArchiveID, TargetSnapshotID: record.RuntimeSnapshotID,
		TargetProviderBindingJSON: record.ProviderBindingJSON, TargetModel: badModel, TargetRequestedModel: &badModel,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := f.provider.RuntimeOperations().Begin(t.Context(), poison); err != nil {
		t.Fatal(err)
	}
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), poison.OperationID, poison.InputHash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.provider.RuntimeOperations().Finalize(t.Context(), baldastate.RuntimeOperationFinalization{
		OperationID: poison.OperationID, InputHash: poison.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeContextReset, ReasonCode: baldastate.RuntimeOperationReasonChangedReplacement,
		At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	restored, err := restarted.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	_, restoredValue, ok := modelRuntimeValue(restored)
	if !ok {
		t.Fatal("restored poisoned runtime is unavailable")
	}
	if restoredValue.outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("restored outcome = %q", restoredValue.outcome)
	}
	poisoned, found, err := f.provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
	if err != nil || !found {
		t.Fatalf("poisoned record = %+v, %t, %v", poisoned, found, err)
	}
	if got := restarted.configuredModel(t.Context(), poisoned); got != publicationOriginalModel {
		t.Fatalf("configured model = %q", got)
	}
	if !restarted.validModelPreferenceLifetime(t.Context(), poisoned) {
		t.Fatal("poisoned lifetime identity is invalid")
	}
	restarted.runtimeOperations = failBeginRuntimeOperations{RuntimeOperationStore: f.provider.RuntimeOperations()}
	failedRequest := ModelChangeRequest{
		OperationID: "repair-precommit-failure", SessionID: restored.GetSessionID(),
		InputHash: strings.Repeat("e", 64), TargetModel: publicationOriginalModel,
	}
	failed, err := restarted.ChangeModel(t.Context(), failedRequest)
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || failed.Status != ModelChangeRecoveryRequired {
		t.Fatalf("ChangeModel() before commit = %+v, %v", failed, err)
	}
	if _, err := restored.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() after fenced failure = %v", err)
	}
	if _, found, err := f.provider.RuntimeOperations().Get(t.Context(), failedRequest.OperationID); err != nil || found {
		t.Fatalf("operation after pre-commit failure = found %t, err %v", found, err)
	}

	recoveredManager := f.manager()
	recoveredManager.runtimeOperations = f.provider.RuntimeOperations()
	restored, err = recoveredManager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	request := ModelChangeRequest{
		OperationID: "repair-false-effective", SessionID: restored.GetSessionID(),
		InputHash: strings.Repeat("c", 64), TargetModel: publicationOriginalModel,
	}
	result, err := recoveredManager.ChangeModel(t.Context(), request)
	if err != nil || result.Status != ModelChangePending {
		t.Fatalf("ChangeModel() = %+v, %v", result, err)
	}
	if recoveredManager.activeTopicSession(record.SessionID) != nil {
		t.Fatal("repaired unattached runtime remained published")
	}
	repaired, found, err := f.provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
	if err != nil || !found || repaired.ArchiveID != record.ArchiveID || repaired.RequestedModel != nil ||
		repaired.EffectiveModel != "" || repaired.RuntimeGeneration != record.RuntimeGeneration+2 {
		t.Fatalf("repaired record = %+v, %t, %v", repaired, found, err)
	}
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
