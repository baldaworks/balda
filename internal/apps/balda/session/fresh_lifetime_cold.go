package session

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
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
	canonicalBinding, err := providers.CanonicalBindingJSON(record.ProviderBindingJSON)
	if err != nil || canonicalBinding != record.ProviderBindingJSON {
		return nil, freshLifetimeUnavailable(), err
	}
	var binding providers.Binding
	if err := json.Unmarshal([]byte(canonicalBinding), &binding); err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	agentName := strings.TrimSpace(record.AgentName)
	if agentName == "" {
		agentName = defaultSessionLabel
	}
	workspaceDir := strings.TrimSpace(record.WorkspaceDir)
	if workspaceDir == "" && !m.workspaceEnabled {
		workspaceDir = m.workingDir
	}
	if workspaceDir == "" {
		return nil, freshLifetimeUnavailable(), ErrRuntimePublicationInvalid
	}
	workspaceDir, err = filepath.Abs(workspaceDir)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	workspaceInfo, err := os.Stat(workspaceDir)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	if !workspaceInfo.IsDir() {
		return nil, freshLifetimeUnavailable(), ErrRuntimePublicationInvalid
	}
	if m.workspaceEnabled && strings.TrimSpace(record.BranchName) == "" {
		return nil, freshLifetimeUnavailable(), ErrRuntimePublicationInvalid
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
	localBuilder, ok := m.agentBuilder.(runtimeSessionServiceProvider)
	if !ok || localBuilder.RuntimeSessionService() == nil {
		return nil, freshLifetimeUnavailable(), nil
	}
	service, appName := localBuilder.RuntimeSessionService(), baldaRuntimeAppName
	selector, ok := m.runtimeManager.(currentSessionSnapshotSelector)
	if !ok {
		return nil, freshLifetimeUnavailable(), nil
	}
	snapshotID, err := selector.CurrentSessionSnapshot(ctx, workspaceDir)
	if err != nil || snapshotID == "" {
		return nil, freshLifetimeUnavailable(), err
	}
	configuration, err := m.describeSessionConfiguration(ctx, SessionRuntimeRequest{
		WorkspaceDir: workspaceDir, RuntimeSnapshotID: snapshotID,
		ProviderBindingJSON: record.ProviderBindingJSON, ModelOverride: cloneOptionalModel(record.RequestedModel),
	})
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	configuration.RequestedModel = cloneOptionalModel(record.RequestedModel)
	configurationJSON, err := encodeConfigurationSummary(configuration)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	operation := baldastate.RuntimeOperationRecord{
		OperationID: request.OperationID, SessionID: record.SessionID, InputHash: request.InputHash,
		Kind: string(baldastate.RuntimeOperationKindLifetimePreference), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: record.ArchiveID, ExpectedGeneration: record.RuntimeGeneration,
		TargetArchiveID: deterministicFreshLifetimeID(request), TargetSnapshotID: snapshotID,
		TargetProviderBindingJSON: record.ProviderBindingJSON, TargetConfigurationSummaryJSON: configurationJSON,
		TargetModel:          record.EffectiveModel,
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
	var topic *TopicSession
	defer func() {
		if prepared != nil {
			return
		}
		_, result, err = m.rollbackFreshLifetimeRecord(ctx, record, operation, service, appName)
		resultErr = errors.Join(resultErr, err)
		m.endSessionHooksForRuntime(context.WithoutCancel(ctx), topic, operation.TargetArchiveID, binding.RootProviderID)
	}()
	intent, err := m.beginFreshLifetimeArchive(ctx, old, operation)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	topic = &TopicSession{
		sessionID: record.SessionID, userID: record.UserID, locator: locator,
		kind: sessionCtx.Kind, ownerJobID: sessionCtx.OwnerJobID,
		agentName: agentName, workspaceDir: workspaceDir, branchName: record.BranchName,
	}
	get := &adksession.GetRequest{AppName: appName, UserID: record.UserID, SessionID: operation.TargetArchiveID}
	response, err := service.Get(ctx, get)
	if errors.Is(err, adksession.ErrNotFound) {
		// The target is created only after its startup gate. Applying recovery
		// can therefore validate its existence without repeating this hook.
		if err := m.startSessionHooksForRuntime(ctx, topic, operation.TargetArchiveID, binding.RootProviderID, sessionHookStartup); err != nil {
			return nil, freshLifetimeUnavailable(), err
		}
		_, createErr := localBuilder.CreateFreshLocalSession(ctx, agentName, record.UserID, operation.TargetArchiveID, workspaceDir,
			RuntimeSessionContext{ArchiveID: operation.TargetArchiveID, BaldaSessionID: record.SessionID, SessionBranch: record.BranchName})
		response, err = service.Get(ctx, get)
		if err != nil && createErr != nil {
			return nil, freshLifetimeUnavailable(), createErr
		}
	}
	if err != nil || response == nil || !freshLifetimeSessionMatches(response.Session, appName, record.UserID, operation.TargetArchiveID, record.SessionID) {
		return nil, freshLifetimeUnavailable(), ErrRuntimeUnavailable
	}
	configuration, err = decodeConfigurationSummary(operation.TargetConfigurationSummaryJSON, operation.TargetRequestedModel)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	activation, err := buildActivationFromReceipt(topic, sessioncmd.ActivationCreated, sessioncmd.PublicRuntimeMetadata{}, "", configuration, nil)
	if err != nil {
		return nil, freshLifetimeUnavailable(), err
	}
	return &PreparedFreshLifetime{
		request: request, operation: operation, archiveToken: intent.Token, ownsPreparation: true,
		activation: activation, coldTopic: topic, coldProviderID: binding.RootProviderID, predecessor: &record,
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
	return topic.swapRuntime(prepared.incumbent, prepared.candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration)
}
