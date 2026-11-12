package session

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/v2/session"
)

// changeColdModelPreference updates an empty provider-cold lifetime without
// attaching the provider merely to validate a construction preference.
func (m *Manager) changeColdModelPreference(
	ctx context.Context,
	record baldastate.SessionRecord,
	request ModelChangeRequest,
	existing baldastate.RuntimeOperationRecord,
	found bool,
) (ModelChangeResult, error) {
	if !m.validColdModelPreferenceLifetime(ctx, record) {
		return unavailableModelRuntimeResult(nil)
	}
	if found && existing.State == baldastate.RuntimeOperationTerminal && sameModelRequest(existing, request) &&
		isColdModelPreferenceOperation(existing) && coldModelPreferenceTargetMatches(record, existing) &&
		isPublicationTerminal(existing, existing, baldastate.RuntimeOperationOutcomeContextReset, baldastate.RuntimeOperationReasonChangedReplacement) {
		return ModelChangeResult{
			Status: ModelChangeChangedReplacement, EffectiveModel: request.TargetModel,
			Reason: baldastate.RuntimeOperationReasonChangedReplacement, Generation: existing.ExpectedGeneration + 1,
		}, nil
	}
	target := request.TargetModel
	now := m.currentTime().UTC()
	intent := baldastate.RuntimeOperationRecord{
		OperationID: request.OperationID, SessionID: request.SessionID, InputHash: request.InputHash,
		Kind: string(baldastate.RuntimeOperationKindModel), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: record.ArchiveID, ExpectedGeneration: record.RuntimeGeneration,
		TargetArchiveID: record.ArchiveID, TargetSnapshotID: record.RuntimeSnapshotID,
		TargetProviderBindingJSON: record.ProviderBindingJSON, TargetModel: target,
		TargetRequestedModel: &target, CreatedAt: now, UpdatedAt: now,
	}
	operation := existing
	if !found {
		var err error
		operation, err = m.runtimeOperations.Begin(ctx, intent)
		if err != nil {
			return unavailableModelRuntimeResult(nil)
		}
	}
	if !samePublicationIntent(operation, intent) || !isColdModelPreferenceOperation(operation) {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	if operation.State == baldastate.RuntimeOperationPrepared {
		if err := m.runtimeOperations.MarkApplying(ctx, operation.OperationID, operation.InputHash); err != nil {
			return recoveryRequiredModelResult()
		}
		operation.State = baldastate.RuntimeOperationApplying
	}
	if operation.State == baldastate.RuntimeOperationApplying {
		if err := m.recoverApplyingColdModelPreference(ctx, operation); err != nil {
			return recoveryRequiredModelResult()
		}
		var err error
		operation, found, err = m.runtimeOperations.Get(ctx, operation.OperationID)
		if err != nil || !found {
			return recoveryRequiredModelResult()
		}
	}
	committed, committedFound, err := m.sessionStore.GetBySessionID(ctx, request.SessionID)
	if err != nil || !committedFound || !coldModelPreferenceTargetMatches(committed, operation) ||
		!isPublicationTerminal(operation, operation, baldastate.RuntimeOperationOutcomeContextReset, baldastate.RuntimeOperationReasonChangedReplacement) {
		return recoveryRequiredModelResult()
	}
	return ModelChangeResult{
		Status: ModelChangeChangedReplacement, EffectiveModel: target,
		Reason: baldastate.RuntimeOperationReasonChangedReplacement, Generation: operation.ExpectedGeneration + 1,
	}, nil
}

func (m *Manager) recoverApplyingColdModelPreference(ctx context.Context, operation baldastate.RuntimeOperationRecord) error {
	if !isColdModelPreferenceOperation(operation) || operation.State != baldastate.RuntimeOperationApplying {
		return ErrRuntimeRecoveryRequired
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found {
		return ErrRuntimeRecoveryRequired
	}
	if coldModelPreferenceTargetMatches(record, operation) {
		return nil
	}
	if !coldModelPreferenceIncumbentMatches(record, operation) || !m.validColdModelPreferenceLifetime(ctx, record) {
		return ErrRuntimeRecoveryRequired
	}
	at := time.Now().UTC()
	if operation.UpdatedAt.After(at) {
		at = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID, InputHash: operation.InputHash,
		Outcome:    baldastate.RuntimeOperationOutcomeContextReset,
		ReasonCode: baldastate.RuntimeOperationReasonChangedReplacement, At: at,
	})
	if err != nil || !isPublicationTerminal(
		terminal, operation, baldastate.RuntimeOperationOutcomeContextReset, baldastate.RuntimeOperationReasonChangedReplacement,
	) {
		return ErrRuntimeRecoveryRequired
	}
	return nil
}

func (m *Manager) validColdModelPreferenceLifetime(ctx context.Context, record baldastate.SessionRecord) bool {
	if m.archive == nil || m.agentBuilder == nil || record.Status != baldastate.SessionStatusActive ||
		strings.TrimSpace(record.ArchiveID) == "" || record.RuntimeOutcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		return false
	}
	archived, err := m.archive.Get(ctx, record.ArchiveID)
	if err != nil || archived.Status != chatarchivecmd.StatusActive || archived.StableID != record.SessionID ||
		archived.RuntimeID != record.ArchiveID || archived.Location.Channel != record.ChannelType ||
		archived.Location.AddressKey != record.AddressKey {
		return false
	}
	local, ok := m.agentBuilder.(runtimeSessionServiceProvider)
	if !ok || local.RuntimeSessionService() == nil {
		return false
	}
	response, err := local.RuntimeSessionService().Get(ctx, &adksession.GetRequest{
		AppName: baldaRuntimeAppName, UserID: record.UserID, SessionID: record.ArchiveID,
	})
	return err == nil && response != nil &&
		freshLifetimeSessionMatches(response.Session, baldaRuntimeAppName, record.UserID, record.ArchiveID, record.SessionID)
}

func isColdModelPreferenceOperation(operation baldastate.RuntimeOperationRecord) bool {
	return operation.Kind == string(baldastate.RuntimeOperationKindModel) &&
		operation.TargetArchiveID != "" && operation.TargetArchiveID == operation.ExpectedArchiveID
}

func coldModelPreferenceIncumbentMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return record.SessionID == operation.SessionID && record.ArchiveID == operation.ExpectedArchiveID &&
		record.RuntimeGeneration == operation.ExpectedGeneration && record.RuntimeSnapshotID == operation.TargetSnapshotID &&
		record.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		record.RuntimeOutcome == string(baldastate.RuntimeOperationOutcomeContextReset)
}

func coldModelPreferenceTargetMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return record.SessionID == operation.SessionID && record.ArchiveID == operation.TargetArchiveID &&
		record.RuntimeGeneration == operation.ExpectedGeneration+1 && record.RuntimeSnapshotID == operation.TargetSnapshotID &&
		record.ProviderBindingJSON == operation.TargetProviderBindingJSON && record.RequestedModel != nil &&
		*record.RequestedModel == operation.TargetModel && record.EffectiveModel == operation.TargetModel &&
		record.RuntimeOutcome == string(baldastate.RuntimeOperationOutcomeContextReset)
}
