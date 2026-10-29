package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
)

// ModelChangeResultStatus is a bounded command-neutral model operation result.
type ModelChangeResultStatus string

const (
	ModelChangeChangedLive        ModelChangeResultStatus = "changed_live"
	ModelChangeChangedReplacement ModelChangeResultStatus = "changed_replacement"
	ModelChangeUnsupported        ModelChangeResultStatus = "unsupported"
	ModelChangeIncompatible       ModelChangeResultStatus = "incompatible"
	ModelChangeRejected           ModelChangeResultStatus = "rejected"
	ModelChangeBusy               ModelChangeResultStatus = "busy"
	ModelChangeConflict           ModelChangeResultStatus = "conflict"
	ModelChangeUnavailable        ModelChangeResultStatus = "unavailable"
	ModelChangeRecoveryRequired   ModelChangeResultStatus = "recovery_required"
)

// ModelChangeRequest identifies one idempotent same-provider model operation.
type ModelChangeRequest struct {
	OperationID string
	SessionID   string
	InputHash   string
	TargetModel string
}

// ModelChangeResult contains only bounded public operation evidence.
type ModelChangeResult struct {
	Status         ModelChangeResultStatus
	EffectiveModel string
	Reason         baldastate.RuntimeOperationReason
	Generation     uint64
}

// ChangeModel validates and applies a same-provider model operation. Its caller
// must already hold TurnDispatcher admission for the addressed session.
func (m *Manager) ChangeModel(ctx context.Context, request ModelChangeRequest) (ModelChangeResult, error) {
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.TargetModel = strings.TrimSpace(request.TargetModel)
	if m == nil || m.runtimeOperations == nil || !validModelChangeText(request.OperationID, 128) ||
		!validModelChangeText(request.SessionID, 256) || !validModelChangeText(request.TargetModel, agentproduct.MaxModelIDBytes) ||
		!validModelInputHash(request.InputHash) {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, ErrRuntimePublicationInvalid
	}

	unlockOperation := m.lockSessionOperation("\x00model-operation:" + request.OperationID)
	defer unlockOperation()
	unlockSession := m.lockSessionOperation(request.SessionID)
	defer unlockSession()
	existing, found, err := m.runtimeOperations.Get(ctx, request.OperationID)
	if err != nil {
		return ModelChangeResult{Status: ModelChangeUnavailable, Reason: baldastate.RuntimeOperationReasonUnavailable}, nil
	}
	if found && !sameModelRequest(existing, request) {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	topic := m.activeTopicSession(request.SessionID)
	if found && existing.State == baldastate.RuntimeOperationApplying {
		if topic != nil {
			topic.fenceRuntimeRecovery()
		}
		return recoveryRequiredModelResult()
	}
	incumbent, value, ok := modelRuntimeValue(topic)
	if !ok {
		return unavailableModelRuntimeResult(topic)
	}
	intent := modelOperationIntent(request, value, m.currentTime())
	if found {
		if existing.State == baldastate.RuntimeOperationTerminal {
			return replayModelResult(topic, value, existing)
		}
		intent = existing
	}

	identity := RuntimeSessionIdentity{
		SessionService: value.runtime.SessionSvc, AppName: value.appName,
		UserID: value.userID, SessionID: value.sessionID,
	}
	if value.runtime.ModelController == nil {
		return ModelChangeResult{Status: ModelChangeUnsupported, EffectiveModel: value.effectiveModel, Reason: baldastate.RuntimeOperationReasonUnsupported, Generation: value.generation}, nil
	}
	validation := value.runtime.ModelController.ValidateModel(ctx, identity, request.TargetModel)
	if validation.Status != agentproduct.ModelSupported {
		return validationModelResult(validation.Status, value), nil
	}

	operation, err := m.runtimeOperations.Begin(ctx, intent)
	if err != nil {
		return m.resolveBeginFailure(ctx, topic, value, request, err)
	}
	if !samePublicationIntent(operation, intent) {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	if operation.State == baldastate.RuntimeOperationTerminal {
		return replayModelResult(topic, value, operation)
	}
	if operation.State == baldastate.RuntimeOperationApplying {
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	}
	if operation.State != baldastate.RuntimeOperationPrepared {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	if err := m.runtimeOperations.MarkApplying(ctx, request.OperationID, request.InputHash); err != nil {
		return m.resolveMarkApplyingFailure(ctx, topic, value, request, err)
	}
	intent.State = baldastate.RuntimeOperationApplying

	change := value.runtime.ModelController.ChangeModel(ctx, identity, request.TargetModel)
	switch change.Status {
	case agentproduct.ModelApplied:
		candidate, successorErr := incumbent.sharedModelSuccessor(request.TargetModel, string(baldastate.RuntimeOperationOutcomeContextPreserved))
		if successorErr != nil {
			topic.fenceRuntimeRecovery()
			return recoveryRequiredModelResult()
		}
		candidate.refreshModelObservation(ctx)
		return m.publishModelChange(ctx, incumbent, candidate, intent, ModelChangeChangedLive, baldastate.RuntimeOperationReasonChangedLive)
	case agentproduct.ModelReplacement:
		if change.Candidate == nil || !verifiedModelIdentityMatches(change.Identity, identity) {
			closeModelCandidate(change.Candidate)
			topic.fenceRuntimeRecovery()
			return recoveryRequiredModelResult()
		}
		candidate, candidateErr := incumbent.independentModelSuccessor(
			change.Candidate, value.session, request.TargetModel, string(baldastate.RuntimeOperationOutcomeContextPreserved),
		)
		if candidateErr != nil {
			closeModelCandidate(change.Candidate)
			topic.fenceRuntimeRecovery()
			return recoveryRequiredModelResult()
		}
		candidate.refreshModelObservation(ctx)
		return m.publishModelChange(ctx, incumbent, candidate, intent, ModelChangeChangedReplacement, baldastate.RuntimeOperationReasonChangedReplacement)
	case agentproduct.ModelAmbiguous:
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	default:
		status, reason := changeModelNoop(change.Status)
		return m.finalizeModelNoop(ctx, topic, intent, status, reason)
	}
}

func (m *Manager) publishModelChange(ctx context.Context, incumbent, candidate *RuntimeSlot, intent baldastate.RuntimeOperationRecord, status ModelChangeResultStatus, reason baldastate.RuntimeOperationReason) (ModelChangeResult, error) {
	_, _, err := m.publishRuntimeStateLocked(ctx, incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved, true, reason)
	if err != nil {
		return recoveryRequiredModelResult()
	}
	return ModelChangeResult{Status: status, EffectiveModel: intent.TargetModel, Reason: reason, Generation: intent.ExpectedGeneration + 1}, nil
}

func (m *Manager) finalizeModelNoop(ctx context.Context, topic *TopicSession, intent baldastate.RuntimeOperationRecord, status ModelChangeResultStatus, reason baldastate.RuntimeOperationReason) (ModelChangeResult, error) {
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: intent.OperationID, InputHash: intent.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeAborted, ReasonCode: reason, At: m.currentTime(),
	})
	if err != nil || !isPublicationTerminal(terminal, intent, baldastate.RuntimeOperationOutcomeAborted, reason) {
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	}
	value, _ := topic.runtimeValue()
	return ModelChangeResult{Status: status, EffectiveModel: value.effectiveModel, Reason: reason, Generation: value.generation}, nil
}

func replayModelResult(topic *TopicSession, current runtimeSlotValue, operation baldastate.RuntimeOperationRecord) (ModelChangeResult, error) {
	if operation.Outcome == string(baldastate.RuntimeOperationOutcomeContextPreserved) &&
		current.archiveID == operation.ExpectedArchiveID && current.generation == operation.ExpectedGeneration+1 &&
		current.effectiveModel == operation.TargetModel && current.runtime.RuntimeSnapshotID == operation.TargetSnapshotID &&
		current.runtime.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		current.outcome == string(baldastate.RuntimeOperationOutcomeContextPreserved) {
		status := modelSuccessResultFromReason(baldastate.RuntimeOperationReason(operation.ReasonCode))
		if status != "" {
			return ModelChangeResult{Status: status, EffectiveModel: operation.TargetModel, Reason: baldastate.RuntimeOperationReason(operation.ReasonCode), Generation: operation.ExpectedGeneration + 1}, nil
		}
	}
	if operation.Outcome == string(baldastate.RuntimeOperationOutcomeAborted) &&
		current.archiveID == operation.ExpectedArchiveID && current.generation == operation.ExpectedGeneration &&
		current.runtime.RuntimeSnapshotID == operation.TargetSnapshotID &&
		current.runtime.ProviderBindingJSON == operation.TargetProviderBindingJSON {
		status := modelResultFromReason(baldastate.RuntimeOperationReason(operation.ReasonCode))
		return ModelChangeResult{Status: status, EffectiveModel: current.effectiveModel, Reason: baldastate.RuntimeOperationReason(operation.ReasonCode), Generation: current.generation}, nil
	}
	topic.fenceRuntimeRecovery()
	return recoveryRequiredModelResult()
}

func sameModelRequest(operation baldastate.RuntimeOperationRecord, request ModelChangeRequest) bool {
	return operation.OperationID == request.OperationID && operation.SessionID == request.SessionID &&
		operation.InputHash == request.InputHash && operation.Kind == string(baldastate.RuntimeOperationKindModel) &&
		operation.TargetModel == request.TargetModel
}

func modelOperationIntent(request ModelChangeRequest, value runtimeSlotValue, at time.Time) baldastate.RuntimeOperationRecord {
	at = at.UTC()
	targetModel := request.TargetModel
	return baldastate.RuntimeOperationRecord{
		OperationID: request.OperationID, SessionID: request.SessionID, InputHash: request.InputHash,
		Kind: string(baldastate.RuntimeOperationKindModel), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: value.archiveID, ExpectedGeneration: value.generation,
		TargetSnapshotID: value.runtime.RuntimeSnapshotID, TargetProviderBindingJSON: value.runtime.ProviderBindingJSON,
		TargetModel: request.TargetModel, TargetRequestedModel: &targetModel, CreatedAt: at, UpdatedAt: at,
	}
}

func modelRuntimeValue(topic *TopicSession) (*RuntimeSlot, runtimeSlotValue, bool) {
	if topic == nil {
		return nil, runtimeSlotValue{}, false
	}
	topic.runtimeMu.RLock()
	defer topic.runtimeMu.RUnlock()
	if topic.recoveryRequired || topic.runtime == nil {
		return nil, runtimeSlotValue{}, false
	}
	return topic.runtime, topic.runtime.value, true
}

func validationModelResult(status agentproduct.ModelValidationStatus, value runtimeSlotValue) ModelChangeResult {
	result := ModelChangeResult{EffectiveModel: value.effectiveModel, Generation: value.generation}
	switch status {
	case agentproduct.ModelUnsupported:
		result.Status, result.Reason = ModelChangeUnsupported, baldastate.RuntimeOperationReasonUnsupported
	case agentproduct.ModelIncompatible:
		result.Status, result.Reason = ModelChangeIncompatible, baldastate.RuntimeOperationReasonIncompatible
	case agentproduct.ModelBusy:
		result.Status, result.Reason = ModelChangeBusy, baldastate.RuntimeOperationReasonBusy
	case agentproduct.ModelClosed:
		result.Status, result.Reason = ModelChangeUnavailable, baldastate.RuntimeOperationReasonClosed
	default:
		result.Status, result.Reason = ModelChangeUnavailable, baldastate.RuntimeOperationReasonFailed
	}
	return result
}

func changeModelNoop(status agentproduct.ModelChangeStatus) (ModelChangeResultStatus, baldastate.RuntimeOperationReason) {
	switch status {
	case agentproduct.ModelChangeUnsupported:
		return ModelChangeUnsupported, baldastate.RuntimeOperationReasonUnsupported
	case agentproduct.ModelChangeIncompatible:
		return ModelChangeIncompatible, baldastate.RuntimeOperationReasonIncompatible
	case agentproduct.ModelRejected:
		return ModelChangeRejected, baldastate.RuntimeOperationReasonRejected
	case agentproduct.ModelChangeBusy:
		return ModelChangeBusy, baldastate.RuntimeOperationReasonBusy
	case agentproduct.ModelChangeClosed:
		return ModelChangeUnavailable, baldastate.RuntimeOperationReasonClosed
	default:
		return ModelChangeUnavailable, baldastate.RuntimeOperationReasonPreDispatch
	}
}

func modelResultFromReason(reason baldastate.RuntimeOperationReason) ModelChangeResultStatus {
	switch reason {
	case baldastate.RuntimeOperationReasonUnsupported:
		return ModelChangeUnsupported
	case baldastate.RuntimeOperationReasonIncompatible:
		return ModelChangeIncompatible
	case baldastate.RuntimeOperationReasonRejected:
		return ModelChangeRejected
	case baldastate.RuntimeOperationReasonBusy:
		return ModelChangeBusy
	default:
		return ModelChangeUnavailable
	}
}

func modelSuccessResultFromReason(reason baldastate.RuntimeOperationReason) ModelChangeResultStatus {
	switch reason {
	case baldastate.RuntimeOperationReasonChangedLive:
		return ModelChangeChangedLive
	case baldastate.RuntimeOperationReasonChangedReplacement:
		return ModelChangeChangedReplacement
	default:
		return ""
	}
}

func (m *Manager) resolveBeginFailure(
	ctx context.Context,
	topic *TopicSession,
	value runtimeSlotValue,
	request ModelChangeRequest,
	writeErr error,
) (ModelChangeResult, error) {
	operation, found, err := m.runtimeOperations.Get(ctx, request.OperationID)
	if err != nil {
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	}
	if !found {
		return preBoundaryWriteResult(writeErr), nil
	}
	if !sameModelRequest(operation, request) {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	return resolveAuthoritativeModelWrite(topic, value, operation, writeErr)
}

func (m *Manager) resolveMarkApplyingFailure(
	ctx context.Context,
	topic *TopicSession,
	value runtimeSlotValue,
	request ModelChangeRequest,
	writeErr error,
) (ModelChangeResult, error) {
	operation, found, err := m.runtimeOperations.Get(ctx, request.OperationID)
	if err != nil || !found || !sameModelRequest(operation, request) {
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	}
	return resolveAuthoritativeModelWrite(topic, value, operation, writeErr)
}

func resolveAuthoritativeModelWrite(
	topic *TopicSession,
	value runtimeSlotValue,
	operation baldastate.RuntimeOperationRecord,
	writeErr error,
) (ModelChangeResult, error) {
	switch operation.State {
	case baldastate.RuntimeOperationPrepared:
		return preBoundaryWriteResult(writeErr), nil
	case baldastate.RuntimeOperationApplying:
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	case baldastate.RuntimeOperationTerminal:
		return replayModelResult(topic, value, operation)
	default:
		topic.fenceRuntimeRecovery()
		return recoveryRequiredModelResult()
	}
}

func preBoundaryWriteResult(err error) ModelChangeResult {
	if errors.Is(err, baldastate.ErrRuntimeOperationConflict) || errors.Is(err, baldastate.ErrRuntimeOperationInvalid) {
		return ModelChangeResult{Status: ModelChangeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}
	}
	return ModelChangeResult{Status: ModelChangeUnavailable, Reason: baldastate.RuntimeOperationReasonUnavailable}
}

func recoveryRequiredModelResult() (ModelChangeResult, error) {
	return ModelChangeResult{Status: ModelChangeRecoveryRequired, Reason: baldastate.RuntimeOperationReasonUnavailable}, ErrRuntimeRecoveryRequired
}

func unavailableModelRuntimeResult(topic *TopicSession) (ModelChangeResult, error) {
	if topic != nil {
		topic.runtimeMu.RLock()
		recoveryRequired := topic.recoveryRequired
		topic.runtimeMu.RUnlock()
		if recoveryRequired {
			return recoveryRequiredModelResult()
		}
	}
	return ModelChangeResult{Status: ModelChangeUnavailable, Reason: baldastate.RuntimeOperationReasonUnavailable}, nil
}

func verifiedModelIdentityMatches(got VerifiedRuntimeSessionIdentity, want RuntimeSessionIdentity) bool {
	return sameIdentityObject(got.SessionService, want.SessionService) && got.AppName == want.AppName &&
		got.UserID == want.UserID && got.SessionID == want.SessionID
}

func closeModelCandidate(candidate *BuiltRuntime) {
	if candidate != nil && candidate.Close != nil {
		_ = candidate.Close()
	}
}

func validModelChangeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func validModelInputHash(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
