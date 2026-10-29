package session

import (
	"errors"
	"testing"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

const (
	recoveryProviderID = "provider"
	recoverySessionID  = "session"
	recoveryUserID     = "user"
)

func TestRestoreSessionAbortsPreparedRuntimeOperation(t *testing.T) {
	manager, topic, store, locator := recoveryManager(t, baldastate.RuntimeOperationPrepared, 1)

	restored, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID})
	if err != nil || restored != topic {
		t.Fatalf("RestoreSession() = %p, %v; want active topic", restored, err)
	}
	operation, found, err := store.Get(t.Context(), "operation")
	if err != nil || !found {
		t.Fatalf("Get() = %+v, %t, %v", operation, found, err)
	}
	if operation.State != baldastate.RuntimeOperationTerminal ||
		operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) ||
		operation.ReasonCode != string(baldastate.RuntimeOperationReasonRecoveryAborted) {
		t.Fatalf("recovered operation = %+v", operation)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("AcquireRuntime() error = %v", err)
	}
	lease.Release()
}

func TestRestoreSessionFencesApplyingRuntimeOperation(t *testing.T) {
	manager, topic, _, locator := recoveryManager(t, baldastate.RuntimeOperationApplying, 1)

	if _, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID}); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("RestoreSession() error = %v, want recovery required", err)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() error = %v, want recovery fence", err)
	}
}

func TestRestoreSessionFencesCommitBeforeMemorySwap(t *testing.T) {
	manager, topic, store, locator := recoveryManager(t, "", 2)
	store.operation = baldastate.RuntimeOperationRecord{}

	if _, err := manager.RestoreSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID}); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("RestoreSession() error = %v, want recovery required", err)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() error = %v, want recovery fence", err)
	}
}

func TestGetSessionInfoReadsOnlyDurableRuntimeState(t *testing.T) {
	manager, topic, _, locator := recoveryManager(t, baldastate.RuntimeOperationApplying, 1)
	topic.userID = "transient-user"

	info, err := manager.GetSessionInfo(t.Context(), locator.SessionID)
	if err != nil {
		t.Fatalf("GetSessionInfo() error = %v", err)
	}
	if info.UserID != recoveryUserID || info.ProviderID != recoveryProviderID || info.Model != "" || info.ActiveModel != "" ||
		info.Generation != 1 || info.ArchiveID != publicationArchiveID || info.Outcome != publicationOutcome {
		t.Fatalf("GetSessionInfo() = %+v", info)
	}
	if info.Status != sessionStatusRecoveryRequired || info.Recovery != "ambiguous" {
		t.Fatalf("recovery status = %q/%q", info.Status, info.Recovery)
	}
}

func TestGetSessionInfoReturnsDurablePreferenceAndCurrentObservationOnly(t *testing.T) {
	const activeModel = "model-active"

	manager, topic, store, locator := recoveryManager(t, "", 1)
	store.operation = baldastate.RuntimeOperationRecord{}
	requested := "model-requested"
	sessionStore := manager.sessionStore.(*fakeSessionStore)
	record := sessionStore.recordsByID[locator.SessionID]
	record.RequestedModel = &requested
	record.EffectiveModel = "legacy-display-must-not-surface"
	sessionStore.recordsByID[locator.SessionID] = record
	topic.runtime.mu.Lock()
	topic.runtime.observation = ModelObservation{
		Generation: 1, Model: activeModel, ControlRevision: "revision", Kind: ModelObservationProviderObserved,
	}
	topic.runtime.hasObservation = true
	topic.runtime.mu.Unlock()

	info, err := manager.GetSessionInfo(t.Context(), locator.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if info.RequestedModel == nil || *info.RequestedModel != requested || info.ActiveModel != activeModel ||
		info.ActiveModelKind != ModelObservationProviderObserved || info.Model != activeModel {
		t.Fatalf("GetSessionInfo() = %+v", info)
	}

	record.RuntimeGeneration = 2
	sessionStore.recordsByID[locator.SessionID] = record
	info, err = manager.GetSessionInfo(t.Context(), locator.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if info.ActiveModel != "" || info.Model != "" || info.RequestedModel == nil || *info.RequestedModel != requested {
		t.Fatalf("stale observation surfaced: %+v", info)
	}
}

func TestEnsureSessionAbortsPreparedOperationWithoutMetadataRow(t *testing.T) {
	manager, builder, store, locator := missingRowRecoveryManager(t, baldastate.RuntimeOperationPrepared)

	topic, err := manager.EnsureSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID}, "agent")
	if err != nil || topic == nil {
		t.Fatalf("EnsureSession() = %p, %v", topic, err)
	}
	operation, found, err := store.Get(t.Context(), "operation")
	if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
		operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) {
		t.Fatalf("recovered operation = %+v, %t, %v", operation, found, err)
	}
	if builder.freshCalls != 1 {
		t.Fatalf("fresh runtime calls = %d, want 1", builder.freshCalls)
	}
}

func TestEnsureSessionFencesApplyingOperationWithoutMetadataRow(t *testing.T) {
	manager, builder, _, locator := missingRowRecoveryManager(t, baldastate.RuntimeOperationApplying)

	if _, err := manager.EnsureSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID}, "agent"); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("EnsureSession() error = %v, want recovery required", err)
	}
	if builder.freshCalls != 0 {
		t.Fatalf("fresh runtime calls = %d, want 0", builder.freshCalls)
	}
}

func TestCreateSessionCannotBypassApplyingOperationWithoutMetadataRow(t *testing.T) {
	manager, builder, _, locator := missingRowRecoveryManager(t, baldastate.RuntimeOperationApplying)

	if err := manager.CreateSession(t.Context(), SessionContext{Locator: locator, UserID: recoveryUserID}, "agent"); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("CreateSession() error = %v, want recovery required", err)
	}
	if builder.freshCalls != 0 {
		t.Fatalf("fresh runtime calls = %d, want 0", builder.freshCalls)
	}
}

func recoveryManager(t *testing.T, operationState baldastate.RuntimeOperationState, recordGeneration uint64) (*Manager, *TopicSession, *publicationStore, SessionLocator) {
	t.Helper()
	locator := testTelegramLocator(61, 0)
	locator.SessionID = recoverySessionID
	record := baldastate.SessionRecord{
		SessionID: recoverySessionID, UserID: recoveryUserID, ChannelType: locator.ChannelType,
		AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "agent",
		ArchiveID: publicationArchiveID, RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding,
		RuntimeGeneration: recordGeneration, EffectiveModel: "model-1", RuntimeOutcome: publicationOutcome,
		Status: baldastate.SessionStatusActive,
	}
	sessionStore := &fakeSessionStore{
		recordsByAddress: map[string]baldastate.SessionRecord{sessionAddressKey(locator.ChannelType, locator.AddressKey): record},
		recordsByID:      map[string]baldastate.SessionRecord{recoverySessionID: record},
	}
	operationStore := &publicationStore{}
	if operationState != "" {
		operationStore.operation = publicationIntent()
		operationStore.operation.State = operationState
	}
	topic := &TopicSession{
		sessionID: recoverySessionID, userID: recoveryUserID, locator: locator, kind: KindChat,
		runtime: newPublishedRuntimeSlot(runtimeSlotConfig{
			runtime: publicationRuntime(nil), archiveID: publicationArchiveID,
			generation: 1, effectiveModel: "model-1", outcome: publicationOutcome,
		}),
	}
	manager := &Manager{
		logger: zerolog.Nop(), sessions: map[string]*TopicSession{recoverySessionID: topic},
		sessionStore: sessionStore, runtimeOperations: operationStore,
	}
	return manager, topic, operationStore, locator
}

func missingRowRecoveryManager(t *testing.T, operationState baldastate.RuntimeOperationState) (*Manager, *sqliteAgentBuilder, *publicationStore, SessionLocator) {
	t.Helper()
	locator := testTelegramLocator(62, 0)
	locator.SessionID = recoverySessionID
	service := adksession.InMemoryService()
	runtime := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: "snapshot"}
	runtimeManager := &fakeScopedRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: recoveryProviderID, runtime: runtime},
		sessionRuntime:          runtime,
	}
	builder := &sqliteAgentBuilder{}
	operationStore := &publicationStore{operation: publicationIntent()}
	operationStore.operation.State = operationState
	manager := &Manager{
		logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: &fakeSessionStore{},
		runtimeOperations: operationStore, runtimeManager: runtimeManager, agentBuilder: builder,
		workingDir: t.TempDir(),
	}
	return manager, builder, operationStore, locator
}
