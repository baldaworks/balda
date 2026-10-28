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
	if operation.Kind != string(baldastate.RuntimeOperationKindLifetime) || operation.State != baldastate.RuntimeOperationPrepared {
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
		record.ProviderBindingJSON == operation.TargetProviderBindingJSON && record.EffectiveModel == operation.TargetModel
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
		record.EffectiveModel == operation.TargetModel && record.RuntimeOutcome == string(baldastate.RuntimeOperationOutcomeContextReset)
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
