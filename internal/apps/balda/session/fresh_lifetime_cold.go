package session

import (
	"context"
	"errors"
	"math"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/v2/session"
)

// prepareColdFreshLifetime discards durable predecessor metadata without ever
// activating the conversation being discarded. The caller holds both operation locks.
func (m *Manager) prepareColdFreshLifetime(ctx context.Context, request FreshLifetimeRequest) (prepared *PreparedFreshLifetime, result FreshLifetimeResult, resultErr error) {
	if m.sessionStore == nil {
		return nil, freshLifetimeUnavailable(), ErrNoPersistedSession
	}
	if err := m.recoverRuntimeOperation(ctx, request.SessionID); err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, request.SessionID)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	if !found || record.Status != baldastate.SessionStatusActive {
		return nil, freshLifetimeUnavailable(), ErrNoPersistedSession
	}
	locator, err := LocatorFromRecord(record)
	if err != nil || record.SessionID != request.SessionID || record.UserID == "" || record.ArchiveID == "" || record.RuntimeSnapshotID == "" ||
		record.ProviderBindingJSON == "" || record.RuntimeGeneration == math.MaxUint64 {
		return nil, freshLifetimeUnavailable(), err
	}
	sessionCtx, err := m.resolveExecutionContext(SessionContext{Locator: locator, UserID: record.UserID})
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	old, err := m.archive.Get(ctx, record.ArchiveID)
	if err != nil || old.Status != chatarchivecmd.StatusActive || old.StableID != record.SessionID || old.RuntimeID == "" ||
		old.Location.Channel != record.ChannelType || old.Location.AddressKey != record.AddressKey {
		return nil, freshLifetimeUnavailable(), err
	}
	if _, err := m.archive.Pending(ctx, record.ChannelType, record.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		return nil, FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, err
	}
	service, appName, _, ok := m.freshLifetimeRecoveryService(request.SessionID, record)
	if !ok {
		return nil, freshLifetimeUnavailable(), nil
	}
	selector, ok := m.runtimeManager.(currentSessionSnapshotSelector)
	if !ok {
		return nil, freshLifetimeUnavailable(), nil
	}
	scoped, ok := m.runtimeManager.(UnpublishedSessionRuntimeManager)
	if !ok {
		return nil, freshLifetimeUnavailable(), nil
	}
	snapshotID, err := selector.CurrentSessionSnapshot(ctx, record.WorkspaceDir)
	if err != nil || snapshotID == "" {
		return nil, freshLifetimeUnavailable(), err
	}
	operation := baldastate.RuntimeOperationRecord{
		OperationID: request.OperationID, SessionID: record.SessionID, InputHash: request.InputHash,
		Kind: string(baldastate.RuntimeOperationKindLifetimePreference), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: record.ArchiveID, ExpectedGeneration: record.RuntimeGeneration,
		TargetArchiveID: deterministicFreshLifetimeID(request), TargetSnapshotID: snapshotID,
		TargetProviderBindingJSON: record.ProviderBindingJSON, TargetModel: record.EffectiveModel,
		TargetRequestedModel: cloneOptionalModel(record.RequestedModel), CreatedAt: m.currentTime().UTC(),
	}
	operation.UpdatedAt = operation.CreatedAt
	expected := operation
	operation, err = m.runtimeOperations.Begin(ctx, operation)
	if err != nil {
		operation, found, err = m.readFreshLifetimeOperation(ctx, request.OperationID)
		if err != nil || !found {
			return nil, freshLifetimeUnavailable(), err
		}
	}
	if operation.State != baldastate.RuntimeOperationPrepared || !samePublicationIntent(operation, expected) {
		return nil, FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	var runtimeCandidate UnpublishedSessionRuntimeCandidate
	defer func() {
		if prepared != nil {
			return
		}
		_, result, err = m.rollbackFreshLifetimeRecord(ctx, record, operation, service, appName)
		resultErr = errors.Join(resultErr, err)
		if runtimeCandidate != nil {
			if err := runtimeCandidate.Abort(context.WithoutCancel(ctx)); err != nil {
				m.fenceRuntimeRecovery(record.SessionID)
				result, resultErr = freshLifetimeRecoveryRequired()
			}
		}
	}()
	intent, err := m.beginFreshLifetimeArchive(ctx, old, operation)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	runtimeCandidate, err = scoped.BuildUnpublishedSessionRuntime(ctx, SessionRuntimeRequest{
		Locator: locator, UserID: record.UserID, AgentSessionID: operation.TargetArchiveID,
		WorkspaceDir: record.WorkspaceDir, RuntimeSnapshotID: snapshotID,
		ProviderBindingJSON: record.ProviderBindingJSON, ModelOverride: cloneOptionalModel(record.RequestedModel),
	})
	if err != nil || runtimeCandidate == nil || runtimeCandidate.Runtime() == nil {
		return nil, freshLifetimeUnavailable(), err
	}
	runtime := runtimeCandidate.Runtime()
	if runtimeCandidate.ExpectedBaseToken() != 0 || runtime.RuntimeBaseToken == 0 || runtime.AppName != appName || runtime.ProviderID == "" ||
		runtime.RuntimeSnapshotID != snapshotID || runtime.ProviderBindingJSON != record.ProviderBindingJSON || runtime.RecipeModel != operation.TargetModel || runtime.SessionSvc == nil {
		return nil, freshLifetimeUnavailable(), ErrRuntimePublicationInvalid
	}
	service = runtime.SessionSvc
	topic := &TopicSession{
		sessionID: record.SessionID, userID: record.UserID, locator: locator,
		kind: sessionCtx.Kind, ownerJobID: sessionCtx.OwnerJobID,
		agentName: record.AgentName, workspaceDir: record.WorkspaceDir, branchName: record.BranchName,
	}
	get := &adksession.GetRequest{AppName: appName, UserID: record.UserID, SessionID: operation.TargetArchiveID}
	response, err := service.Get(ctx, get)
	if errors.Is(err, adksession.ErrNotFound) {
		// The target is created only after its startup gate. Applying recovery
		// can therefore validate its existence without repeating this hook.
		if err := m.startSessionHooksForRuntime(ctx, topic, operation.TargetArchiveID, runtime.ProviderID, sessionHookStartup); err != nil {
			return nil, freshLifetimeUnavailable(), err
		}
		_, createErr := m.agentBuilder.CreateFreshRuntimeSession(ctx, runtime, runtime.ProviderID, record.UserID, operation.TargetArchiveID, record.WorkspaceDir,
			RuntimeSessionContext{ArchiveID: operation.TargetArchiveID, BaldaSessionID: record.SessionID, SessionBranch: record.BranchName})
		response, err = service.Get(ctx, get)
		if err != nil && createErr != nil {
			return nil, freshLifetimeUnavailable(), createErr
		}
	}
	if err != nil || response == nil || !freshLifetimeSessionMatches(response.Session, appName, record.UserID, operation.TargetArchiveID, record.SessionID) {
		return nil, freshLifetimeUnavailable(), ErrRuntimeUnavailable
	}
	slot := newRuntimeSlot(runtimeSlotConfig{
		runtime: runtime, session: response.Session, providerID: runtime.ProviderID,
		archiveID: operation.TargetArchiveID, generation: record.RuntimeGeneration + 1,
		effectiveModel: operation.TargetModel, outcome: string(baldastate.RuntimeOperationOutcomeContextReset),
	})
	activation, err := buildActivationFromReceipt(topic, sessioncmd.ActivationCreated, runtime.PublicRuntimeMetadata, operation.TargetModel, nil)
	if err != nil {
		_ = slot.abortUnpublished(context.WithoutCancel(ctx))
		return nil, freshLifetimeUnavailable(), err
	}
	return &PreparedFreshLifetime{
		request: request, operation: operation, archiveToken: intent.Token, ownsPreparation: true,
		candidate: slot, runtimeCandidate: runtimeCandidate, activation: activation,
		coldTopic: topic, predecessor: &record,
	}, FreshLifetimeResult{}, nil
}

func (m *Manager) coldFreshLifetimeGuard(ctx context.Context, predecessor baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	if m.activeTopicSession(operation.SessionID) != nil {
		return false
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	return err == nil && found && freshLifetimeIncumbentRecordMatches(record, operation) &&
		record.Status == baldastate.SessionStatusActive && record.UserID == predecessor.UserID &&
		record.ChannelType == predecessor.ChannelType && record.AddressKey == predecessor.AddressKey && record.AddressJSON == predecessor.AddressJSON &&
		record.AgentName == predecessor.AgentName && record.WorkspaceDir == predecessor.WorkspaceDir && record.BranchName == predecessor.BranchName &&
		record.RuntimeSnapshotID == predecessor.RuntimeSnapshotID
}

func (m *Manager) publishFreshLifetimeSlot(topic *TopicSession, prepared *PreparedFreshLifetime, operation baldastate.RuntimeOperationRecord) bool {
	if prepared.predecessor == nil {
		return topic.swapRuntime(prepared.incumbent, prepared.candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[operation.SessionID] != nil {
		return false
	}
	topic.runtime = prepared.candidate
	m.sessions[operation.SessionID] = topic
	return true
}
