package session

import (
	"context"
	"math"
	"strings"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
)

func (m *Manager) recoverApplyingModelOperation(ctx context.Context, operation baldastate.RuntimeOperationRecord) error {
	if operation.State != baldastate.RuntimeOperationApplying || operation.Kind != string(baldastate.RuntimeOperationKindModel) {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	// Fence before any provider observation. The session-operation lock
	// serializes Manager work, while this flag also rejects direct runtime leases.
	m.fenceRuntimeRecovery(operation.SessionID)
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found || !modelRecoveryRecordMatches(record, operation) {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	if topic := m.activeTopicSession(operation.SessionID); topic != nil {
		return m.recoverActiveModelOperation(ctx, topic, record, operation)
	}
	return m.recoverPersistedModelOperation(ctx, record, operation)
}

func (m *Manager) recoverActiveModelOperation(
	ctx context.Context,
	topic *TopicSession,
	record baldastate.SessionRecord,
	operation baldastate.RuntimeOperationRecord,
) error {
	incumbent, value, ok := topic.modelRecoveryRuntime(operation)
	if !ok || !modelRecoveryValueMatches(value, record, operation) || value.runtime.ModelController == nil {
		topic.fenceRuntimeRecovery()
		return ErrRuntimeRecoveryRequired
	}
	change := value.runtime.ModelController.ChangeModel(ctx, runtimeIdentity(value), operation.TargetModel)
	return m.applyModelRecoveryChange(ctx, topic, incumbent, value, operation, change)
}

func (m *Manager) recoverPersistedModelOperation(
	ctx context.Context,
	record baldastate.SessionRecord,
	operation baldastate.RuntimeOperationRecord,
) error {
	if m.runtimeManager == nil || m.agentBuilder == nil {
		return ErrRuntimeRecoveryRequired
	}
	locator, err := LocatorFromRecord(record)
	if err != nil {
		return ErrRuntimeRecoveryRequired
	}
	workspaceDir := strings.TrimSpace(record.WorkspaceDir)
	if workspaceDir == "" && !m.workspaceEnabled {
		workspaceDir = m.workingDir
	}
	runtime, err := m.bindModelRecoveryRuntime(ctx, locator, record, operation, workspaceDir)
	if err != nil {
		return modelRecoveryContextError(ctx)
	}
	defer closeBuiltRuntime(runtime)
	agentName := strings.TrimSpace(record.AgentName)
	if agentName == "" {
		agentName = defaultSessionLabel
	}
	sess, err := m.agentBuilder.OpenExistingRuntimeSession(
		ctx, runtime, agentName, record.UserID, record.SessionID, workspaceDir,
		RuntimeSessionContext{ArchiveID: record.ArchiveID, BaldaSessionID: record.SessionID, SessionBranch: record.BranchName},
	)
	if err != nil || runtime.ModelController == nil {
		return modelRecoveryContextError(ctx)
	}
	value := runtimeSlotValue{
		runtime: *runtime, session: sess, appName: sess.AppName(), userID: sess.UserID(), sessionID: sess.ID(),
		archiveID: record.ArchiveID, generation: record.RuntimeGeneration,
		effectiveModel: record.EffectiveModel, outcome: record.RuntimeOutcome,
	}
	if !modelRecoveryValueMatches(value, record, operation) {
		return ErrRuntimeRecoveryRequired
	}
	change := runtime.ModelController.ChangeModel(ctx, runtimeIdentity(value), operation.TargetModel)
	return m.applyModelRecoveryChange(ctx, nil, nil, value, operation, change)
}

func (m *Manager) bindModelRecoveryRuntime(
	ctx context.Context,
	locator SessionLocator,
	record baldastate.SessionRecord,
	operation baldastate.RuntimeOperationRecord,
	workspaceDir string,
) (*BuiltRuntime, error) {
	if scopedManager, ok := m.runtimeManager.(SessionRuntimeManager); ok {
		targetModel := operation.TargetModel
		runtime, err := scopedManager.RuntimeForSession(ctx, SessionRuntimeRequest{
			Locator: locator, UserID: record.UserID, AgentSessionID: record.SessionID,
			WorkspaceDir: workspaceDir, RuntimeSnapshotID: operation.TargetSnapshotID,
			ProviderBindingJSON: operation.TargetProviderBindingJSON,
			ModelOverride:       &targetModel,
		})
		if err != nil {
			return nil, err
		}
		return validateSessionRuntimeBinding(runtime, operation.TargetSnapshotID, operation.TargetProviderBindingJSON)
	}
	rootRuntime, err := m.runtimeManager.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	return m.bindSessionRuntime(
		ctx, rootRuntime, locator, record.UserID, record.SessionID, workspaceDir,
		operation.TargetSnapshotID, operation.TargetProviderBindingJSON, &operation.TargetModel,
	)
}

func (m *Manager) applyModelRecoveryChange(
	ctx context.Context,
	topic *TopicSession,
	incumbent *RuntimeSlot,
	value runtimeSlotValue,
	operation baldastate.RuntimeOperationRecord,
	change RuntimeModelChange,
) error {
	switch change.Status {
	case agentproduct.ModelApplied:
		if topic == nil {
			if _, ok := m.finalizeModelRecovery(ctx, operation, value, baldastate.RuntimeOperationOutcomeContextPreserved, baldastate.RuntimeOperationReasonChangedLive); !ok {
				return ErrRuntimeRecoveryRequired
			}
			return nil
		}
		candidate, err := incumbent.sharedModelSuccessor(operation.TargetModel, string(baldastate.RuntimeOperationOutcomeContextPreserved))
		if err != nil || !candidate.claimPublication() {
			if candidate != nil {
				_ = candidate.abortUnpublished(ctx)
			}
			topic.fenceRuntimeRecovery()
			return ErrRuntimeRecoveryRequired
		}
		candidate.refreshModelObservation(ctx)
		if _, ok := m.finalizeModelRecovery(ctx, operation, value, baldastate.RuntimeOperationOutcomeContextPreserved, baldastate.RuntimeOperationReasonChangedLive); !ok ||
			!topic.swapRecoveredRuntime(incumbent, candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration) {
			candidate.releasePublicationClaim(false)
			_ = candidate.abortUnpublished(ctx)
			topic.fenceRuntimeRecovery()
			return ErrRuntimeRecoveryRequired
		}
		candidate.releasePublicationClaim(true)
		incumbent.beginDrain()
		return nil
	case agentproduct.ModelReplacement:
		if change.Candidate == nil || !verifiedModelIdentityMatches(change.Identity, runtimeIdentity(value)) {
			closeModelCandidate(change.Candidate)
			if topic != nil {
				topic.fenceRuntimeRecovery()
			}
			return ErrRuntimeRecoveryRequired
		}
		if topic == nil {
			closeModelCandidate(change.Candidate)
			if _, ok := m.finalizeModelRecovery(ctx, operation, value, baldastate.RuntimeOperationOutcomeContextPreserved, baldastate.RuntimeOperationReasonChangedReplacement); !ok {
				return ErrRuntimeRecoveryRequired
			}
			return nil
		}
		candidate, err := incumbent.independentModelSuccessor(
			change.Candidate, value.session, operation.TargetModel, string(baldastate.RuntimeOperationOutcomeContextPreserved),
		)
		if err != nil || !candidate.claimPublication() {
			closeModelCandidate(change.Candidate)
			topic.fenceRuntimeRecovery()
			return ErrRuntimeRecoveryRequired
		}
		candidate.refreshModelObservation(ctx)
		if _, ok := m.finalizeModelRecovery(ctx, operation, value, baldastate.RuntimeOperationOutcomeContextPreserved, baldastate.RuntimeOperationReasonChangedReplacement); !ok ||
			!topic.swapRecoveredRuntime(incumbent, candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration) {
			candidate.releasePublicationClaim(false)
			_ = candidate.abortUnpublished(ctx)
			topic.fenceRuntimeRecovery()
			return ErrRuntimeRecoveryRequired
		}
		candidate.releasePublicationClaim(true)
		incumbent.beginDrain()
		return nil
	case agentproduct.ModelAmbiguous:
		if topic != nil {
			topic.fenceRuntimeRecovery()
		}
		return ErrRuntimeRecoveryRequired
	default:
		// Applying evidence proves the external boundary may already have been
		// crossed. Only authoritative target confirmation can terminalize it.
		if topic != nil {
			topic.fenceRuntimeRecovery()
		}
		return ErrRuntimeRecoveryRequired
	}
}

func (m *Manager) finalizeModelRecovery(
	ctx context.Context,
	operation baldastate.RuntimeOperationRecord,
	incumbent runtimeSlotValue,
	outcome baldastate.RuntimeOperationOutcome,
	reason baldastate.RuntimeOperationReason,
) (baldastate.RuntimeOperationRecord, bool) {
	terminalAt := m.currentTime().UTC()
	if operation.UpdatedAt.After(terminalAt) {
		terminalAt = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID, InputHash: operation.InputHash,
		Outcome: outcome, ReasonCode: reason, At: terminalAt,
	})
	if err != nil {
		terminal, found, getErr := m.runtimeOperations.Get(ctx, operation.OperationID)
		if getErr != nil || !found {
			return baldastate.RuntimeOperationRecord{}, false
		}
		if !isPublicationTerminal(terminal, operation, outcome, reason) {
			return terminal, false
		}
		return terminal, m.modelRecoveryCommitMatches(ctx, operation, incumbent, outcome)
	}
	if !isPublicationTerminal(terminal, operation, outcome, reason) {
		return terminal, false
	}
	return terminal, m.modelRecoveryCommitMatches(ctx, operation, incumbent, outcome)
}

func (m *Manager) modelRecoveryCommitMatches(
	ctx context.Context,
	operation baldastate.RuntimeOperationRecord,
	incumbent runtimeSlotValue,
	outcome baldastate.RuntimeOperationOutcome,
) bool {
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found {
		return false
	}
	if outcome == baldastate.RuntimeOperationOutcomeAborted {
		return record.ArchiveID == incumbent.archiveID && record.RuntimeGeneration == incumbent.generation &&
			record.RuntimeSnapshotID == incumbent.runtime.RuntimeSnapshotID &&
			record.ProviderBindingJSON == incumbent.runtime.ProviderBindingJSON &&
			record.EffectiveModel == incumbent.effectiveModel && record.RuntimeOutcome == incumbent.outcome
	}
	targetArchive := operation.TargetArchiveID
	if targetArchive == "" {
		targetArchive = incumbent.archiveID
	}
	return record.ArchiveID == targetArchive && record.RuntimeGeneration == operation.ExpectedGeneration+1 &&
		record.RuntimeSnapshotID == operation.TargetSnapshotID &&
		record.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		record.RequestedModel != nil && *record.RequestedModel == operation.TargetModel &&
		record.EffectiveModel == operation.TargetModel && record.RuntimeOutcome == string(outcome)
}

func modelRecoveryRecordMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	targetArchive := strings.TrimSpace(operation.TargetArchiveID)
	recordBinding := strings.TrimSpace(record.ProviderBindingJSON)
	legacyUnknown := record.RuntimeGeneration == 0
	return strings.TrimSpace(record.SessionID) == operation.SessionID &&
		operation.ExpectedGeneration != math.MaxUint64 &&
		(strings.TrimSpace(record.Status) == "" || record.Status == baldastate.SessionStatusActive) &&
		strings.TrimSpace(record.ArchiveID) == operation.ExpectedArchiveID &&
		record.RuntimeGeneration == operation.ExpectedGeneration &&
		operation.TargetSnapshotID != "" &&
		operation.TargetProviderBindingJSON != "" &&
		strings.TrimSpace(record.RuntimeSnapshotID) == operation.TargetSnapshotID &&
		(recordBinding == operation.TargetProviderBindingJSON || legacyUnknown && recordBinding == "") &&
		(targetArchive == "" || targetArchive == operation.ExpectedArchiveID) &&
		validModelChangeText(operation.TargetModel, agentproduct.MaxModelIDBytes)
}

func modelRecoveryValueMatches(value runtimeSlotValue, record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return value.archiveID == operation.ExpectedArchiveID && value.generation == operation.ExpectedGeneration &&
		value.effectiveModel == strings.TrimSpace(record.EffectiveModel) &&
		value.runtime.RuntimeSnapshotID == operation.TargetSnapshotID &&
		value.runtime.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		value.runtime.SessionSvc != nil && value.session != nil && value.appName == value.session.AppName() &&
		value.userID == strings.TrimSpace(record.UserID) && value.userID == value.session.UserID() &&
		value.sessionID != "" && value.sessionID == value.session.ID()
}

func runtimeIdentity(value runtimeSlotValue) RuntimeSessionIdentity {
	return RuntimeSessionIdentity{
		SessionService: value.runtime.SessionSvc, AppName: value.appName,
		UserID: value.userID, SessionID: value.sessionID,
	}
}

func modelRecoveryContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrRuntimeRecoveryRequired
}

func (s *TopicSession) modelRecoveryRuntime(operation baldastate.RuntimeOperationRecord) (*RuntimeSlot, runtimeSlotValue, bool) {
	if s == nil {
		return nil, runtimeSlotValue{}, false
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	if !s.recoveryRequired || s.runtime == nil || s.runtime.value.archiveID != operation.ExpectedArchiveID ||
		s.runtime.value.generation != operation.ExpectedGeneration {
		return nil, runtimeSlotValue{}, false
	}
	return s.runtime, s.runtime.value, true
}

func (s *TopicSession) swapRecoveredRuntime(expected, successor *RuntimeSlot, archiveID string, generation uint64) bool {
	if s == nil || successor == nil {
		return false
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if !s.recoveryRequired || s.runtime != expected || expected == nil ||
		expected.value.archiveID != archiveID || expected.value.generation != generation {
		return false
	}
	s.runtime = successor
	s.recoveryRequired = false
	return true
}
