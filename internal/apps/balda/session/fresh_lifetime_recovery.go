package session

import (
	"context"
	"errors"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/v2/session"
)

type runtimeSessionServiceProvider interface {
	RuntimeSessionService() adksession.Service
}

func (m *Manager) recoverPreparedFreshLifetimeOperation(ctx context.Context, operation baldastate.RuntimeOperationRecord) error {
	if (operation.Kind != string(baldastate.RuntimeOperationKindLifetime) && operation.Kind != string(baldastate.RuntimeOperationKindLifetimePreference)) || operation.State != baldastate.RuntimeOperationPrepared {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found || !freshLifetimeIncumbentRecordMatches(record, operation) {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	service, appName, userID, ok := m.freshLifetimeRecoveryService(operation.SessionID, record)
	if !ok {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	if err := m.deletePreparedFreshSession(ctx, service, appName, userID, operation); err != nil {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	if pending, pendingErr := m.archive.Pending(ctx, record.ChannelType, record.AddressKey); pendingErr == nil {
		if !freshLifetimeArchiveIntentMatches(pending, operation, record) ||
			m.archive.Complete(ctx, pending.Token, chatarchivecmd.OutcomeAborted) != nil {
			m.fenceRuntimeRecovery(operation.SessionID)
			return ErrRuntimeRecoveryRequired
		}
	} else if !errors.Is(pendingErr, chatarchivecmd.ErrNotFound) {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	terminal, ok := m.finalizeFreshLifetimeRecoveryAborted(ctx, operation)
	if !ok || !isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeAborted, baldastate.RuntimeOperationReasonRecoveryAborted) {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	return nil
}

func (m *Manager) recoverApplyingFreshLifetimeOperation(ctx context.Context, operation baldastate.RuntimeOperationRecord) error {
	if operation.Kind == string(baldastate.RuntimeOperationKindLifetimePreference) {
		return m.recoverApplyingIndependentFreshLifetime(ctx, operation)
	}
	if operation.Kind != string(baldastate.RuntimeOperationKindLifetime) || operation.State != baldastate.RuntimeOperationApplying {
		m.fenceRuntimeRecovery(operation.SessionID)
		return ErrRuntimeRecoveryRequired
	}
	m.fenceRuntimeRecovery(operation.SessionID)
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found || !freshLifetimeRecoveryRecordMatches(record, operation) {
		return ErrRuntimeRecoveryRequired
	}
	service, appName, userID, ok := m.freshLifetimeRecoveryService(operation.SessionID, record)
	if !ok {
		return ErrRuntimeRecoveryRequired
	}
	response, err := service.Get(ctx, &adksession.GetRequest{AppName: appName, UserID: userID, SessionID: operation.TargetArchiveID})
	if err != nil || response == nil || !freshLifetimeSessionMatches(response.Session, appName, userID, operation.TargetArchiveID, operation.SessionID) {
		return ErrRuntimeRecoveryRequired
	}

	if record.ArchiveID == operation.ExpectedArchiveID {
		pending, pendingErr := m.archive.Pending(ctx, record.ChannelType, record.AddressKey)
		if pendingErr != nil || !freshLifetimeArchiveIntentMatches(pending, operation, record) {
			return ErrRuntimeRecoveryRequired
		}
		live := freshLifetimeTargetRecord(record, operation)
		if _, activateErr := m.archive.Activate(ctx, pending.Token, &live); activateErr != nil &&
			!m.freshLifetimeDurableTargetMatches(ctx, operation) {
			return ErrRuntimeRecoveryRequired
		}
	}
	if !m.freshLifetimeArchiveTargetMatches(ctx, operation, record) {
		return ErrRuntimeRecoveryRequired
	}
	terminal, finalized := m.finalizeFreshLifetime(ctx, operation)
	if !finalized || !isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeContextReset, "") {
		return ErrRuntimeRecoveryRequired
	}

	topic := m.activeTopicSession(operation.SessionID)
	if topic == nil {
		return nil
	}
	incumbent, value, ok := topic.modelRecoveryRuntime(operation)
	if !ok || !freshLifetimeValueMatches(value, record, operation) {
		return ErrRuntimeRecoveryRequired
	}
	candidate, err := incumbent.sharedLifetimeSuccessor(response.Session, operation.TargetArchiveID, string(baldastate.RuntimeOperationOutcomeContextReset))
	if err != nil || !candidate.claimPublication() {
		if candidate != nil {
			_ = candidate.abortUnpublished(ctx)
		}
		return ErrRuntimeRecoveryRequired
	}
	if !topic.swapRecoveredRuntime(incumbent, candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration) {
		candidate.releasePublicationClaim(false)
		_ = candidate.abortUnpublished(ctx)
		return ErrRuntimeRecoveryRequired
	}
	candidate.releasePublicationClaim(true)
	incumbent.beginDrain()
	return nil
}

func (m *Manager) recoverApplyingIndependentFreshLifetime(ctx context.Context, operation baldastate.RuntimeOperationRecord) error {
	m.fenceRuntimeRecovery(operation.SessionID)
	if operation.State != baldastate.RuntimeOperationApplying || operation.TargetArchiveID == operation.ExpectedArchiveID {
		return ErrRuntimeRecoveryRequired
	}
	scoped, ok := m.runtimeManager.(UnpublishedSessionRuntimeManager)
	if !ok || scoped.FenceSessionRuntimeBase(operation.SessionID) != nil {
		return ErrRuntimeRecoveryRequired
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found || !freshLifetimeRecoveryRecordMatches(record, operation) {
		return ErrRuntimeRecoveryRequired
	}
	locator, err := LocatorFromRecord(record)
	if err != nil {
		return ErrRuntimeRecoveryRequired
	}
	request := SessionRuntimeRequest{
		Locator: locator, UserID: record.UserID, AgentSessionID: operation.TargetArchiveID,
		WorkspaceDir: record.WorkspaceDir, RuntimeSnapshotID: operation.TargetSnapshotID,
		ProviderBindingJSON: operation.TargetProviderBindingJSON,
		ModelOverride:       cloneOptionalModel(operation.TargetRequestedModel),
		ExpectedRecipeModel: operation.TargetModel, RecoveryFence: true,
	}
	topic := m.activeTopicSession(operation.SessionID)
	var incumbent, activeTarget *RuntimeSlot
	var incumbentToken uint64
	if topic != nil {
		value, present := topic.runtimeValue()
		switch {
		case present && freshLifetimeActiveTargetMatches(value, operation):
			activeTarget = topic.runtime
		case present:
			incumbent, value, ok = topic.modelRecoveryRuntime(operation)
			if !ok || !freshLifetimeValueMatches(value, record, operation) {
				return ErrRuntimeRecoveryRequired
			}
			incumbentToken = value.runtime.RuntimeBaseToken
		default:
			return ErrRuntimeRecoveryRequired
		}
	}
	candidate, adopted := scoped.AdoptPromotedSessionRuntime(operation.SessionID, incumbentToken, request)
	if !adopted {
		candidate, err = scoped.BuildUnpublishedSessionRuntime(ctx, request)
	}
	if err != nil || candidate == nil || candidate.Runtime() == nil || candidate.Runtime().RecipeModel != operation.TargetModel {
		if candidate != nil {
			_ = candidate.Abort(ctx)
		}
		return ErrRuntimeRecoveryRequired
	}
	published := false
	defer func() {
		if !published {
			_ = candidate.Abort(context.WithoutCancel(ctx))
		}
	}()
	runtime := candidate.Runtime()
	response, err := runtime.SessionSvc.Get(ctx, &adksession.GetRequest{
		AppName: runtime.AppName, UserID: record.UserID, SessionID: operation.TargetArchiveID,
	})
	if err != nil || response == nil || !freshLifetimeSessionMatches(response.Session, runtime.AppName, record.UserID, operation.TargetArchiveID, operation.SessionID) {
		return ErrRuntimeRecoveryRequired
	}
	if incumbent != nil {
		if !adopted && candidate.ExpectedBaseToken() != incumbentToken {
			return ErrRuntimeRecoveryRequired
		}
		if !adopted && candidate.ClaimForPublication() != nil {
			return ErrRuntimeRecoveryRequired
		}
	}
	if activeTarget != nil && (!adopted || !freshLifetimeRuntimeMatchesSlot(candidate.Runtime(), activeTarget)) {
		return ErrRuntimeRecoveryRequired
	}
	if record.ArchiveID == operation.ExpectedArchiveID {
		pending, pendingErr := m.archive.Pending(ctx, record.ChannelType, record.AddressKey)
		if pendingErr != nil || !freshLifetimeArchiveIntentMatches(pending, operation, record) {
			return ErrRuntimeRecoveryRequired
		}
		live := freshLifetimeTargetRecord(record, operation)
		if _, err = m.archive.Activate(ctx, pending.Token, &live); err != nil && !m.freshLifetimeDurableTargetMatches(ctx, operation) {
			return ErrRuntimeRecoveryRequired
		}
	}
	if !m.freshLifetimeArchiveTargetMatches(ctx, operation, record) {
		return ErrRuntimeRecoveryRequired
	}
	if incumbent != nil {
		slot, slotErr := incumbent.independentLifetimeSuccessor(runtime, response.Session, operation.TargetArchiveID, operation.TargetModel, string(baldastate.RuntimeOperationOutcomeContextReset))
		if slotErr != nil || !slot.claimPublication() || !adopted && candidate.Promote() != nil {
			if slot != nil {
				slot.releasePublicationClaim(false)
				_ = slot.abortUnpublished(ctx)
			}
			return ErrRuntimeRecoveryRequired
		}
		if !topic.swapRecoveredRuntime(incumbent, slot, operation.ExpectedArchiveID, operation.ExpectedGeneration) {
			slot.releasePublicationClaim(true)
			published = true
			return ErrRuntimeRecoveryRequired
		}
		slot.releasePublicationClaim(true)
		candidate.MarkPublished()
		published = true
		incumbent.beginDrain()
	} else if activeTarget != nil {
		candidate.MarkPublished()
		published = true
	}
	terminal, finalized := m.finalizeFreshLifetime(ctx, operation)
	if !finalized || !isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeContextReset, "") {
		return ErrRuntimeRecoveryRequired
	}
	if topic == nil {
		return candidate.Abort(context.WithoutCancel(ctx))
	}
	if err := candidate.CompletePublication(); err != nil {
		return err
	}
	if activeTarget != nil && !topic.completeRecoveredRuntime(activeTarget, operation.TargetArchiveID, operation.ExpectedGeneration+1) {
		return ErrRuntimeRecoveryRequired
	}
	return nil
}

func freshLifetimeActiveTargetMatches(value runtimeSlotValue, operation baldastate.RuntimeOperationRecord) bool {
	return value.archiveID == operation.TargetArchiveID && value.sessionID == operation.TargetArchiveID &&
		value.generation == operation.ExpectedGeneration+1 && value.runtime.RuntimeSnapshotID == operation.TargetSnapshotID &&
		value.runtime.ProviderBindingJSON == operation.TargetProviderBindingJSON && value.runtime.RecipeModel == operation.TargetModel &&
		value.effectiveModel == operation.TargetModel && value.outcome == string(baldastate.RuntimeOperationOutcomeContextReset) &&
		freshLifetimeSessionMatches(value.session, value.appName, value.userID, operation.TargetArchiveID, operation.SessionID)
}

func freshLifetimeRuntimeMatchesSlot(runtime *BuiltRuntime, slot *RuntimeSlot) bool {
	if runtime == nil || slot == nil {
		return false
	}
	value := slot.value
	return runtime.RuntimeBaseToken != 0 && runtime.RuntimeBaseToken == value.runtime.RuntimeBaseToken &&
		sameIdentityObject(runtime.Agent, value.runtime.Agent) && sameIdentityObject(runtime.Runner, value.runtime.Runner) &&
		sameIdentityObject(runtime.SessionSvc, value.runtime.SessionSvc)
}

func (m *Manager) freshLifetimeRecoveryService(sessionID string, record baldastate.SessionRecord) (adksession.Service, string, string, bool) {
	if topic := m.activeTopicSession(sessionID); topic != nil {
		_, value, ok := topic.modelRecoveryRuntime(baldastate.RuntimeOperationRecord{
			ExpectedArchiveID: record.ArchiveID, ExpectedGeneration: record.RuntimeGeneration,
		})
		if ok && value.runtime.SessionSvc != nil {
			return value.runtime.SessionSvc, value.appName, value.userID, true
		}
		// Prepared recovery is intentionally not fenced yet.
		if value, present := topic.runtimeValue(); present && value.runtime.SessionSvc != nil {
			return value.runtime.SessionSvc, value.appName, value.userID, true
		}
	}
	provider, ok := m.agentBuilder.(runtimeSessionServiceProvider)
	if !ok || provider.RuntimeSessionService() == nil || strings.TrimSpace(record.UserID) == "" {
		return nil, "", "", false
	}
	return provider.RuntimeSessionService(), baldaRuntimeAppName, record.UserID, true
}

func freshLifetimeIncumbentRecordMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return record.SessionID == operation.SessionID && record.ArchiveID == operation.ExpectedArchiveID &&
		record.RuntimeGeneration == operation.ExpectedGeneration && record.RuntimeSnapshotID == operation.TargetSnapshotID &&
		record.ProviderBindingJSON == operation.TargetProviderBindingJSON && record.EffectiveModel == operation.TargetModel &&
		(operation.Kind != string(baldastate.RuntimeOperationKindLifetimePreference) || sameOptionalModel(record.RequestedModel, operation.TargetRequestedModel))
}

func freshLifetimeRecoveryRecordMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	if record.SessionID != operation.SessionID || operation.TargetArchiveID == "" || operation.TargetSnapshotID == "" {
		return false
	}
	if freshLifetimeIncumbentRecordMatches(record, operation) {
		return true
	}
	return record.ArchiveID == operation.TargetArchiveID && record.RuntimeGeneration == operation.ExpectedGeneration+1 &&
		record.RuntimeSnapshotID == operation.TargetSnapshotID && record.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		record.EffectiveModel == operation.TargetModel && record.RuntimeOutcome == string(baldastate.RuntimeOperationOutcomeContextReset) &&
		(operation.Kind != string(baldastate.RuntimeOperationKindLifetimePreference) || sameOptionalModel(record.RequestedModel, operation.TargetRequestedModel))
}

func freshLifetimeValueMatches(value runtimeSlotValue, record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return value.archiveID == operation.ExpectedArchiveID && value.generation == operation.ExpectedGeneration &&
		value.runtime.RuntimeSnapshotID == operation.TargetSnapshotID &&
		value.runtime.ProviderBindingJSON == operation.TargetProviderBindingJSON && value.effectiveModel == operation.TargetModel &&
		value.userID == record.UserID && value.sessionID != operation.TargetArchiveID
}

func freshLifetimeArchiveIntentMatches(intent chatarchivecmd.Intent, operation baldastate.RuntimeOperationRecord, record baldastate.SessionRecord) bool {
	return intent.Kind == chatarchivecmd.KindFresh && intent.OldID == operation.ExpectedArchiveID &&
		intent.Session.ID == operation.TargetArchiveID && intent.Session.RuntimeID == operation.TargetArchiveID &&
		intent.Session.PredecessorID == operation.ExpectedArchiveID && intent.Session.StableID == operation.SessionID &&
		intent.Session.Location.Channel == record.ChannelType && intent.Session.Location.AddressKey == record.AddressKey
}

func freshLifetimeTargetRecord(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) baldastate.SessionRecord {
	record.ArchiveID = operation.TargetArchiveID
	record.RuntimeGeneration = operation.ExpectedGeneration + 1
	record.RuntimeSnapshotID = operation.TargetSnapshotID
	record.ProviderBindingJSON = operation.TargetProviderBindingJSON
	record.EffectiveModel = operation.TargetModel
	record.RequestedModel = cloneOptionalModel(operation.TargetRequestedModel)
	record.RuntimeOutcome = string(baldastate.RuntimeOperationOutcomeContextReset)
	return record
}

func (m *Manager) freshLifetimeArchiveTargetMatches(ctx context.Context, operation baldastate.RuntimeOperationRecord, record baldastate.SessionRecord) bool {
	target, err := m.archive.Get(ctx, operation.TargetArchiveID)
	return err == nil && target.Status == chatarchivecmd.StatusActive && target.ID == operation.TargetArchiveID &&
		target.RuntimeID == operation.TargetArchiveID && target.StableID == operation.SessionID &&
		target.PredecessorID == operation.ExpectedArchiveID && target.Location.Channel == record.ChannelType &&
		target.Location.AddressKey == record.AddressKey && m.freshLifetimeDurableTargetMatches(ctx, operation)
}

func (m *Manager) finalizeFreshLifetimeRecoveryAborted(ctx context.Context, operation baldastate.RuntimeOperationRecord) (baldastate.RuntimeOperationRecord, bool) {
	at := m.currentTime().UTC()
	if operation.UpdatedAt.After(at) {
		at = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID, InputHash: operation.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeAborted, ReasonCode: baldastate.RuntimeOperationReasonRecoveryAborted, At: at,
	})
	return terminal, err == nil
}
