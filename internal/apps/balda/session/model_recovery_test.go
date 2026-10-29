package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

type recoveryModelController struct {
	snapshot      agentproduct.SessionControlSnapshot
	inspectCalls  atomic.Int32
	validateCalls atomic.Int32
	changeCalls   atomic.Int32
}

func (c *recoveryModelController) InspectSessionControl(context.Context, RuntimeSessionIdentity) agentproduct.SessionControlSnapshot {
	c.inspectCalls.Add(1)
	return c.snapshot
}

func (c *recoveryModelController) ValidateModel(context.Context, RuntimeSessionIdentity, string) agentproduct.ModelValidation {
	c.validateCalls.Add(1)
	return agentproduct.ModelValidation{Status: agentproduct.ModelSupported}
}

func (c *recoveryModelController) ChangeModel(_ context.Context, _ RuntimeSessionIdentity, model string) RuntimeModelChange {
	c.changeCalls.Add(1)
	if c.snapshot.Status == agentproduct.ControlReady && strings.TrimSpace(c.snapshot.CurrentModel) != "" {
		c.snapshot.CurrentModel = model
		return RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: model}
	}
	switch c.snapshot.Status {
	case agentproduct.ControlBusy:
		return RuntimeModelChange{Status: agentproduct.ModelChangeBusy}
	case agentproduct.ControlUnsupported:
		return RuntimeModelChange{Status: agentproduct.ModelChangeUnsupported}
	case agentproduct.ControlClosed:
		return RuntimeModelChange{Status: agentproduct.ModelChangeClosed}
	default:
		return RuntimeModelChange{Status: agentproduct.ModelAmbiguous}
	}
}

type recoveryRuntimeManager struct {
	service    adksession.Service
	control    RuntimeModelController
	closeCalls atomic.Int32
	mu         sync.Mutex
	requests   []SessionRuntimeRequest
}

func (m *recoveryRuntimeManager) Runtime(context.Context) (*BuiltRuntime, error) {
	return &BuiltRuntime{SessionSvc: m.service, AppName: baldaRuntimeAppName}, nil
}

func (*recoveryRuntimeManager) ProviderID() string { return recoveryProviderID }

func (m *recoveryRuntimeManager) RuntimeForSession(_ context.Context, request SessionRuntimeRequest) (*BuiltRuntime, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	return &BuiltRuntime{
		SessionSvc: m.service, AppName: baldaRuntimeAppName, ModelController: m.control,
		RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding,
		Close: func() error { m.closeCalls.Add(1); return nil },
	}, nil
}

func (m *recoveryRuntimeManager) sessionRequests() []SessionRuntimeRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SessionRuntimeRequest(nil), m.requests...)
}

type recoveryAgentBuilder struct {
	service     adksession.Service
	openCalls   atomic.Int32
	createCalls atomic.Int32
	freshCalls  atomic.Int32
}

func (b *recoveryAgentBuilder) CreateRuntimeSession(context.Context, *BuiltRuntime, string, string, string, string, RuntimeSessionContext) (adksession.Session, error) {
	b.createCalls.Add(1)
	return nil, errors.New("unexpected create")
}

func (b *recoveryAgentBuilder) OpenExistingRuntimeSession(ctx context.Context, runtime *BuiltRuntime, _ string, userID, sessionID, _ string, _ RuntimeSessionContext) (adksession.Session, error) {
	b.openCalls.Add(1)
	response, err := runtime.SessionSvc.Get(ctx, &adksession.GetRequest{AppName: runtime.AppName, UserID: userID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return response.Session, nil
}

func (b *recoveryAgentBuilder) CreateFreshRuntimeSession(context.Context, *BuiltRuntime, string, string, string, string, RuntimeSessionContext) (adksession.Session, error) {
	b.freshCalls.Add(1)
	return nil, errors.New("unexpected fresh create")
}

func (*recoveryAgentBuilder) GetAgentMetadata(string) AgentMetadata {
	return AgentMetadata{Model: publicationOriginalModel}
}

func TestApplyingModelRecoveryRestoresAuthoritativeObservation(t *testing.T) {
	tests := []struct {
		name           string
		snapshot       agentproduct.SessionControlSnapshot
		wantErr        bool
		wantGeneration uint64
		wantModel      string
		wantOutcome    baldastate.RuntimeOperationOutcome
		wantReason     baldastate.RuntimeOperationReason
	}{
		{
			name: "target model applied", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel},
			wantGeneration: 2, wantModel: modelChangeTargetModel,
			wantOutcome: baldastate.RuntimeOperationOutcomeContextPreserved, wantReason: baldastate.RuntimeOperationReasonChangedLive,
		},
		{
			name: "incumbent model reconciled", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: publicationOriginalModel},
			wantGeneration: 2, wantModel: modelChangeTargetModel,
			wantOutcome: baldastate.RuntimeOperationOutcomeContextPreserved, wantReason: baldastate.RuntimeOperationReasonChangedLive,
		},
		{
			name: "third model reconciled", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: "model-third"},
			wantGeneration: 2, wantModel: modelChangeTargetModel,
			wantOutcome: baldastate.RuntimeOperationOutcomeContextPreserved, wantReason: baldastate.RuntimeOperationReasonChangedLive,
		},
		{name: "empty model is ambiguous", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady}, wantErr: true},
		{name: "busy is ambiguous", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlBusy, Reason: agentproduct.ReasonActiveWork}, wantErr: true},
		{name: "unsupported is ambiguous", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlUnsupported, Reason: agentproduct.ReasonUnavailable}, wantErr: true},
		{name: "failed is ambiguous", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlFailed, Reason: agentproduct.ReasonUncertain}, wantErr: true},
		{name: "closed is ambiguous", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlClosed, Reason: agentproduct.ReasonClosed}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPersistedModelRecoveryFixture(t, test.snapshot)
			topic, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID})
			if test.wantErr {
				if !errors.Is(err, ErrRuntimeRecoveryRequired) || topic != nil {
					t.Fatalf("RestoreSession() = %p, %v; want recovery required", topic, err)
				}
				operation, found, getErr := fixture.operations.Get(t.Context(), "operation")
				if getErr != nil || !found || operation.State != baldastate.RuntimeOperationApplying {
					t.Fatalf("operation = %+v, %t, %v; want applying", operation, found, getErr)
				}
				if fixture.runtime.closeCalls.Load() != 1 {
					t.Fatalf("private runtime closes = %d, want 1", fixture.runtime.closeCalls.Load())
				}
			} else {
				if err != nil || topic == nil {
					t.Fatalf("RestoreSession() = %p, %v", topic, err)
				}
				lease, acquireErr := topic.AcquireRuntime()
				if acquireErr != nil {
					t.Fatal(acquireErr)
				}
				if lease.Generation() != test.wantGeneration || lease.EffectiveModel() != test.wantModel {
					t.Fatalf("runtime = generation %d model %q", lease.Generation(), lease.EffectiveModel())
				}
				lease.Release()
				operation, found, getErr := fixture.operations.Get(t.Context(), "operation")
				if getErr != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
					operation.Outcome != string(test.wantOutcome) || operation.ReasonCode != string(test.wantReason) {
					t.Fatalf("operation = %+v, %t, %v", operation, found, getErr)
				}
				if fixture.runtime.closeCalls.Load() != 1 {
					t.Fatalf("private recovery runtime closes = %d, want 1", fixture.runtime.closeCalls.Load())
				}
			}
			if fixture.control.inspectCalls.Load() != 0 || fixture.control.validateCalls.Load() != 0 || fixture.control.changeCalls.Load() != 1 {
				t.Fatalf("control calls inspect/validate/change = %d/%d/%d", fixture.control.inspectCalls.Load(), fixture.control.validateCalls.Load(), fixture.control.changeCalls.Load())
			}
			if fixture.builder.openCalls.Load() != 1+boolInt(!test.wantErr) {
				t.Fatalf("open calls = %d", fixture.builder.openCalls.Load())
			}
			if fixture.builder.createCalls.Load() != 0 || fixture.builder.freshCalls.Load() != 0 {
				t.Fatalf("recovery created session: create=%d fresh=%d", fixture.builder.createCalls.Load(), fixture.builder.freshCalls.Load())
			}
			committed, found, getErr := fixture.sessions.GetBySessionID(t.Context(), recoverySessionID)
			if getErr != nil || !found {
				t.Fatalf("GetBySessionID() = %+v, %t, %v", committed, found, getErr)
			}
			wantGeneration, wantModel, wantOutcome := uint64(1), publicationOriginalModel, publicationOutcome
			if !test.wantErr {
				wantGeneration, wantModel = test.wantGeneration, test.wantModel
			}
			if committed.ArchiveID != publicationArchiveID || committed.RuntimeSnapshotID != publicationIntent().TargetSnapshotID ||
				committed.ProviderBindingJSON != publicationBinding || committed.RuntimeGeneration != wantGeneration ||
				committed.EffectiveModel != wantModel || committed.RuntimeOutcome != wantOutcome {
				t.Fatalf("committed runtime = %+v, want archive/binding/snapshot/generation/model/outcome unchanged or authoritative", committed)
			}
			if !test.wantErr && (committed.RequestedModel == nil || *committed.RequestedModel != modelChangeTargetModel) {
				t.Fatalf("committed requested model = %v", committed.RequestedModel)
			}
		})
	}
}

func TestApplyingLegacyModelRecoveryUsesOperationBinding(t *testing.T) {
	tests := []struct {
		name      string
		active    bool
		snapshot  agentproduct.SessionControlSnapshot
		wantReady bool
	}{
		{name: "cold target", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel}, wantReady: true},
		{name: "active target", active: true, snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel}, wantReady: true},
		{name: "cold third", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: "model-third"}, wantReady: true},
		{name: "cold unavailable", snapshot: agentproduct.SessionControlSnapshot{Status: agentproduct.ControlFailed, Reason: agentproduct.ReasonUnavailable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyModelRecoveryFixture(t, test.snapshot, test.active)
			topic, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID})
			if !test.wantReady {
				if !errors.Is(err, ErrRuntimeRecoveryRequired) || topic != nil {
					t.Fatalf("RestoreSession() = %p, %v; want recovery required", topic, err)
				}
				operation, found, getErr := fixture.operations.Get(t.Context(), "operation")
				if getErr != nil || !found || operation.State != baldastate.RuntimeOperationApplying {
					t.Fatalf("operation = %+v, %t, %v; want applying", operation, found, getErr)
				}
				if test.active {
					if _, acquireErr := fixture.manager.sessions[recoverySessionID].AcquireRuntime(); !errors.Is(acquireErr, ErrRuntimeRecoveryRequired) {
						t.Fatalf("AcquireRuntime() error = %v, want recovery required", acquireErr)
					}
				}
			} else {
				if err != nil || topic == nil {
					t.Fatalf("RestoreSession() = %p, %v", topic, err)
				}
				lease, acquireErr := topic.AcquireRuntime()
				if acquireErr != nil {
					t.Fatal(acquireErr)
				}
				if lease.Generation() != 1 || lease.EffectiveModel() != modelChangeTargetModel ||
					lease.ProviderBindingJSON() != publicationBinding || lease.RuntimeSnapshotID() != "snapshot" {
					t.Fatalf("legacy recovery runtime = generation %d model %q binding %q snapshot %q", lease.Generation(), lease.EffectiveModel(), lease.ProviderBindingJSON(), lease.RuntimeSnapshotID())
				}
				lease.Release()
			}
			requests := fixture.runtime.sessionRequests()
			wantRequests := 1
			if test.active {
				wantRequests = 0
			} else if test.wantReady {
				wantRequests = 2
			}
			if len(requests) != wantRequests {
				t.Fatalf("RuntimeForSession() calls = %d, want %d", len(requests), wantRequests)
			}
			for _, request := range requests {
				if request.ProviderBindingJSON != publicationBinding || request.RuntimeSnapshotID != "snapshot" {
					t.Fatalf("RuntimeForSession() request = binding %q snapshot %q", request.ProviderBindingJSON, request.RuntimeSnapshotID)
				}
			}
			if fixture.builder.createCalls.Load() != 0 || fixture.builder.freshCalls.Load() != 0 {
				t.Fatalf("legacy recovery created session: create=%d fresh=%d", fixture.builder.createCalls.Load(), fixture.builder.freshCalls.Load())
			}
			if fixture.control.validateCalls.Load() != 0 || fixture.control.changeCalls.Load() != 1 {
				t.Fatalf("legacy recovery reconcile calls: validate=%d change=%d", fixture.control.validateCalls.Load(), fixture.control.changeCalls.Load())
			}
			committed, found, getErr := fixture.sessions.GetBySessionID(t.Context(), recoverySessionID)
			if getErr != nil || !found {
				t.Fatalf("GetBySessionID() = %+v, %t, %v", committed, found, getErr)
			}
			if test.wantReady {
				if committed.RuntimeGeneration != 1 || committed.EffectiveModel != modelChangeTargetModel ||
					committed.ProviderBindingJSON != publicationBinding || committed.RuntimeSnapshotID != publicationIntent().TargetSnapshotID ||
					committed.ArchiveID != publicationArchiveID || committed.RuntimeOutcome != publicationOutcome {
					t.Fatalf("committed legacy recovery = %+v", committed)
				}
			} else if committed.RuntimeGeneration != 0 || committed.EffectiveModel != "" || committed.ProviderBindingJSON != "" ||
				committed.RuntimeSnapshotID != publicationIntent().TargetSnapshotID || committed.ArchiveID != publicationArchiveID {
				t.Fatalf("unresolved legacy record changed = %+v", committed)
			}
		})
	}
}

func TestApplyingModelRecoveryRepairsActiveFence(t *testing.T) {
	for _, currentModel := range []string{modelChangeTargetModel, publicationOriginalModel} {
		t.Run(currentModel, func(t *testing.T) {
			fixture := newPersistedModelRecoveryFixture(t, agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: currentModel})
			response, err := fixture.service.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: recoveryUserID, SessionID: recoverySessionID})
			if err != nil {
				t.Fatal(err)
			}
			topic := &TopicSession{
				sessionID: recoverySessionID, userID: recoveryUserID, locator: fixture.locator, kind: KindChat,
				runtime: newPublishedRuntimeSlot(runtimeSlotConfig{
					runtime: &BuiltRuntime{SessionSvc: fixture.service, AppName: baldaRuntimeAppName, ModelController: fixture.control, RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding},
					session: response.Session, archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel, outcome: publicationOutcome,
				}),
			}
			fixture.manager.sessions[recoverySessionID] = topic

			restored, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID})
			if err != nil || restored != topic {
				t.Fatalf("RestoreSession() = %p, %v", restored, err)
			}
			lease, err := topic.AcquireRuntime()
			if err != nil {
				t.Fatal(err)
			}
			wantGeneration, wantModel := uint64(2), modelChangeTargetModel
			if lease.Generation() != wantGeneration || lease.EffectiveModel() != wantModel {
				t.Fatalf("runtime = generation %d model %q", lease.Generation(), lease.EffectiveModel())
			}
			lease.Release()
			if fixture.builder.openCalls.Load() != 0 || fixture.runtime.closeCalls.Load() != 0 {
				t.Fatalf("active recovery rebuilt runtime: opens=%d closes=%d", fixture.builder.openCalls.Load(), fixture.runtime.closeCalls.Load())
			}
		})
	}
}

func TestApplyingModelRecoverySerializesConcurrentAdmission(t *testing.T) {
	fixture := newPersistedModelRecoveryFixture(t, agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel})
	response, err := fixture.service.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: recoveryUserID, SessionID: recoverySessionID})
	if err != nil {
		t.Fatal(err)
	}
	topic := &TopicSession{
		sessionID: recoverySessionID, userID: recoveryUserID, locator: fixture.locator, kind: KindChat,
		runtime: newPublishedRuntimeSlot(runtimeSlotConfig{
			runtime: &BuiltRuntime{SessionSvc: fixture.service, AppName: baldaRuntimeAppName, ModelController: fixture.control, RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding},
			session: response.Session, archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel, outcome: publicationOutcome,
		}),
	}
	fixture.manager.sessions[recoverySessionID] = topic

	const callers = 24
	start := make(chan struct{})
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, restoreErr := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID})
			errs <- restoreErr
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("RestoreSession() error = %v", err)
		}
	}
	if fixture.control.inspectCalls.Load() != 1 {
		t.Fatalf("Inspect calls = %d, want 1", fixture.control.inspectCalls.Load())
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Generation() != 2 || lease.EffectiveModel() != modelChangeTargetModel {
		t.Fatalf("runtime = generation %d model %q", lease.Generation(), lease.EffectiveModel())
	}
}

func TestApplyingModelRecoveryRejectsContradictoryGuardBeforeInspect(t *testing.T) {
	fixture := newPersistedModelRecoveryFixtureWithIntent(
		t,
		agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel},
		nil,
		func(intent *baldastate.RuntimeOperationRecord) { intent.TargetSnapshotID = "different-snapshot" },
	)

	if _, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID}); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if fixture.control.inspectCalls.Load() != 0 || fixture.builder.openCalls.Load() != 0 {
		t.Fatalf("contradictory guard called provider: inspect=%d open=%d", fixture.control.inspectCalls.Load(), fixture.builder.openCalls.Load())
	}
}

func TestApplyingModelRecoveryRejectsBindingContradictionsBeforeInspect(t *testing.T) {
	tests := []struct {
		name    string
		fixture func(*testing.T) persistedModelRecoveryFixture
	}{
		{
			name: "legacy nonblank contradiction",
			fixture: func(t *testing.T) persistedModelRecoveryFixture {
				return newLegacyModelRecoveryFixtureWithRecord(t, agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel}, false, func(record *baldastate.SessionRecord) {
					record.ProviderBindingJSON = `{"RootProviderID":"different"}`
				})
			},
		},
		{
			name: "nonlegacy blank contradiction",
			fixture: func(t *testing.T) persistedModelRecoveryFixture {
				return newPersistedModelRecoveryFixtureWithIntent(t, agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel}, func(record *baldastate.SessionRecord) {
					record.ProviderBindingJSON = ""
				}, nil)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.fixture(t)
			if _, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID}); !errors.Is(err, ErrRuntimeRecoveryRequired) {
				t.Fatalf("RestoreSession() error = %v, want recovery required", err)
			}
			if fixture.control.inspectCalls.Load() != 0 || len(fixture.runtime.sessionRequests()) != 0 {
				t.Fatalf("contradictory binding reached provider: inspect=%d runtime=%d", fixture.control.inspectCalls.Load(), len(fixture.runtime.sessionRequests()))
			}
		})
	}
}

func TestApplyingNonModelRecoveryRemainsGenericallyFenced(t *testing.T) {
	for _, kind := range []baldastate.RuntimeOperationKind{
		baldastate.RuntimeOperationKindProvider,
		baldastate.RuntimeOperationKindLifetime,
	} {
		t.Run(string(kind), func(t *testing.T) {
			manager, topic, store, locator := recoveryManager(t, baldastate.RuntimeOperationApplying, 1)
			store.operation.Kind = string(kind)
			if _, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID}); !errors.Is(err, ErrRuntimeRecoveryRequired) {
				t.Fatalf("RestoreSession() error = %v, want recovery required", err)
			}
			if store.operation.State != baldastate.RuntimeOperationApplying {
				t.Fatalf("operation = %+v, want applying", store.operation)
			}
			if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
				t.Fatalf("AcquireRuntime() error = %v, want recovery required", err)
			}
		})
	}
}

type uncertainFinalizeStore struct {
	baldastate.RuntimeOperationStore
	commit bool
}

func (s uncertainFinalizeStore) Finalize(ctx context.Context, final baldastate.RuntimeOperationFinalization) (baldastate.RuntimeOperationRecord, error) {
	if !s.commit {
		return baldastate.RuntimeOperationRecord{}, errors.New("private-store-error")
	}
	terminal, err := s.RuntimeOperationStore.Finalize(ctx, final)
	if err != nil {
		return terminal, err
	}
	return baldastate.RuntimeOperationRecord{}, errors.New("private-commit-error")
}

func TestApplyingModelRecoveryClassifiesFinalizeUncertaintyFromStoreTruth(t *testing.T) {
	for _, test := range []struct {
		name    string
		commit  bool
		wantErr bool
	}{
		{name: "committed then error", commit: true},
		{name: "not committed", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPersistedModelRecoveryFixture(t, agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel})
			fixture.manager.runtimeOperations = uncertainFinalizeStore{RuntimeOperationStore: fixture.operations, commit: test.commit}

			topic, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID})
			if test.wantErr {
				if !errors.Is(err, ErrRuntimeRecoveryRequired) || err.Error() != ErrRuntimeRecoveryRequired.Error() || topic != nil {
					t.Fatalf("RestoreSession() = %p, %v; want recovery required", topic, err)
				}
				operation, found, getErr := fixture.operations.Get(t.Context(), "operation")
				if getErr != nil || !found || operation.State != baldastate.RuntimeOperationApplying {
					t.Fatalf("operation = %+v, %t, %v; want applying", operation, found, getErr)
				}
				if strings.Contains(err.Error(), "private-") {
					t.Fatalf("RestoreSession() exposed store detail: %v", err)
				}
				return
			}
			if err != nil || topic == nil {
				t.Fatalf("RestoreSession() = %p, %v", topic, err)
			}
			lease, acquireErr := topic.AcquireRuntime()
			if acquireErr != nil {
				t.Fatal(acquireErr)
			}
			if lease.Generation() != 2 || lease.EffectiveModel() != modelChangeTargetModel {
				t.Fatalf("runtime = generation %d model %q", lease.Generation(), lease.EffectiveModel())
			}
			lease.Release()
		})
	}
}

func TestApplyingModelRecoveryIsIdempotentAcrossRestart(t *testing.T) {
	fixture := newPersistedModelRecoveryFixture(t, agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: modelChangeTargetModel})
	if _, err := fixture.manager.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID}); err != nil {
		t.Fatal(err)
	}
	firstInspectCalls := fixture.control.inspectCalls.Load()
	restarted, err := NewManager(ManagerParams{
		AgentBuilder: fixture.builder, RuntimeManager: fixture.runtime, WorkingDir: t.TempDir(), SessionsPersistent: true,
		SessionStore: fixture.sessions, RuntimeOperations: fixture.operations, Logger: zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := restarted.RestoreSession(t.Context(), SessionContext{Locator: fixture.locator, UserID: recoveryUserID})
	if err != nil || topic == nil {
		t.Fatalf("second RestoreSession() = %p, %v", topic, err)
	}
	if fixture.control.inspectCalls.Load() != firstInspectCalls {
		t.Fatalf("terminal restart repeated Inspect: before=%d after=%d", firstInspectCalls, fixture.control.inspectCalls.Load())
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.Generation() != 2 || lease.EffectiveModel() != modelChangeTargetModel {
		t.Fatalf("runtime = generation %d model %q", lease.Generation(), lease.EffectiveModel())
	}
	lease.Release()
}

type persistedModelRecoveryFixture struct {
	manager    *Manager
	service    adksession.Service
	control    *recoveryModelController
	runtime    *recoveryRuntimeManager
	builder    *recoveryAgentBuilder
	sessions   baldastate.SessionStore
	operations baldastate.RuntimeOperationStore
	locator    SessionLocator
}

func newPersistedModelRecoveryFixture(t *testing.T, snapshot agentproduct.SessionControlSnapshot) persistedModelRecoveryFixture {
	return newPersistedModelRecoveryFixtureWithIntent(t, snapshot, nil, nil)
}

func newPersistedModelRecoveryFixtureWithIntent(
	t *testing.T,
	snapshot agentproduct.SessionControlSnapshot,
	modifyRecord func(*baldastate.SessionRecord),
	modifyIntent func(*baldastate.RuntimeOperationRecord),
) persistedModelRecoveryFixture {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	service := adksession.InMemoryService()
	if _, err := service.Create(t.Context(), &adksession.CreateRequest{AppName: baldaRuntimeAppName, UserID: recoveryUserID, SessionID: recoverySessionID}); err != nil {
		t.Fatal(err)
	}
	locator := testTelegramLocator(64, 0)
	locator.SessionID = recoverySessionID
	record := baldastate.SessionRecord{
		SessionID: recoverySessionID, UserID: recoveryUserID, ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, AgentName: "agent", WorkspaceDir: t.TempDir(), ArchiveID: publicationArchiveID,
		RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding, RuntimeGeneration: 1,
		EffectiveModel: publicationOriginalModel, RuntimeOutcome: publicationOutcome, Status: baldastate.SessionStatusActive,
	}
	if modifyRecord != nil {
		modifyRecord(&record)
	}
	if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	intent := publicationIntent()
	if modifyIntent != nil {
		modifyIntent(&intent)
	}
	intent.CreatedAt = time.Now().UTC().Add(-time.Minute)
	intent.UpdatedAt = intent.CreatedAt
	if _, err := provider.RuntimeOperations().Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := provider.RuntimeOperations().MarkApplying(t.Context(), intent.OperationID, intent.InputHash); err != nil {
		t.Fatal(err)
	}
	control := &recoveryModelController{snapshot: snapshot}
	runtimeManager := &recoveryRuntimeManager{service: service, control: control}
	builder := &recoveryAgentBuilder{service: service}
	manager, err := NewManager(ManagerParams{
		AgentBuilder: builder, RuntimeManager: runtimeManager, WorkingDir: t.TempDir(), SessionsPersistent: true,
		SessionStore: provider.Sessions(), RuntimeOperations: provider.RuntimeOperations(), Logger: zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return persistedModelRecoveryFixture{
		manager: manager, service: service, control: control, runtime: runtimeManager, builder: builder,
		sessions: provider.Sessions(), operations: provider.RuntimeOperations(), locator: locator,
	}
}

func newLegacyModelRecoveryFixture(
	t *testing.T,
	snapshot agentproduct.SessionControlSnapshot,
	active bool,
) persistedModelRecoveryFixture {
	return newLegacyModelRecoveryFixtureWithRecord(t, snapshot, active, nil)
}

func newLegacyModelRecoveryFixtureWithRecord(
	t *testing.T,
	snapshot agentproduct.SessionControlSnapshot,
	active bool,
	modifyRecord func(*baldastate.SessionRecord),
) persistedModelRecoveryFixture {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	service := adksession.InMemoryService()
	response, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: baldaRuntimeAppName, UserID: recoveryUserID, SessionID: recoverySessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	locator := testTelegramLocator(65, 0)
	locator.SessionID = recoverySessionID
	record := baldastate.SessionRecord{
		SessionID: recoverySessionID, UserID: recoveryUserID, ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, AgentName: "agent", WorkspaceDir: t.TempDir(), ArchiveID: publicationArchiveID,
		RuntimeSnapshotID: "snapshot", Status: baldastate.SessionStatusActive,
	}
	if modifyRecord != nil {
		modifyRecord(&record)
	}
	if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	intent := publicationIntent()
	intent.ExpectedGeneration = 0
	intent.CreatedAt = time.Now().UTC().Add(-time.Minute)
	intent.UpdatedAt = intent.CreatedAt
	if _, err := provider.RuntimeOperations().Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := provider.RuntimeOperations().MarkApplying(t.Context(), intent.OperationID, intent.InputHash); err != nil {
		t.Fatal(err)
	}
	control := &recoveryModelController{snapshot: snapshot}
	runtimeManager := &recoveryRuntimeManager{service: service, control: control}
	builder := &recoveryAgentBuilder{service: service}
	manager, err := NewManager(ManagerParams{
		AgentBuilder: builder, RuntimeManager: runtimeManager, WorkingDir: t.TempDir(), SessionsPersistent: true,
		SessionStore: provider.Sessions(), RuntimeOperations: provider.RuntimeOperations(), Logger: zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if active {
		manager.sessions[recoverySessionID] = &TopicSession{
			sessionID: recoverySessionID, userID: recoveryUserID, locator: locator, kind: KindChat,
			runtime: newPublishedRuntimeSlot(runtimeSlotConfig{
				runtime: &BuiltRuntime{
					SessionSvc: service, AppName: baldaRuntimeAppName, ModelController: control,
					RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding,
				},
				session: response.Session, archiveID: publicationArchiveID,
			}),
		}
	}
	return persistedModelRecoveryFixture{
		manager: manager, service: service, control: control, runtime: runtimeManager, builder: builder,
		sessions: provider.Sessions(), operations: provider.RuntimeOperations(), locator: locator,
	}
}

func boolInt(value bool) int32 {
	if value {
		return 1
	}
	return 0
}
