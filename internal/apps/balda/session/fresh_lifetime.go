package session

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/v2/session"
)

const baldaSessionIDStateKey = "balda_session_id"

// FreshLifetimeStatus is bounded command-neutral evidence for one local reset.
type FreshLifetimeStatus string

const (
	FreshLifetimeReset            FreshLifetimeStatus = "reset"
	FreshLifetimeConflict         FreshLifetimeStatus = "conflict"
	FreshLifetimeUnavailable      FreshLifetimeStatus = "unavailable"
	FreshLifetimeRecoveryRequired FreshLifetimeStatus = "recovery_required"
)

// FreshLifetimeRequest identifies one idempotent local lifetime rotation.
type FreshLifetimeRequest struct {
	OperationID string
	SessionID   string
	InputHash   string
}

// FreshLifetimeResult contains no provider or target-session details.
type FreshLifetimeResult struct {
	Status     FreshLifetimeStatus
	Generation uint64
	Reason     baldastate.RuntimeOperationReason
}

// PreparedFreshLifetime owns one unpublished candidate reference until it is
// published or aborted. Only the plan that created the durable preparation may
// destructively roll it back; exact replay plans merely release their own ref.
type PreparedFreshLifetime struct {
	request          FreshLifetimeRequest
	operation        baldastate.RuntimeOperationRecord
	archiveToken     string
	ownsPreparation  bool
	incumbent        *RuntimeSlot
	candidate        *RuntimeSlot
	runtimeCandidate UnpublishedSessionRuntimeCandidate
	settleOnce       sync.Once
	settleResult     FreshLifetimeResult
	settleErr        error
}

// PrepareFreshLifetime creates and verifies the empty local ADK session before
// the caller enters the TurnDispatcher replacement boundary.
func (m *Manager) PrepareFreshLifetime(ctx context.Context, request FreshLifetimeRequest) (*PreparedFreshLifetime, FreshLifetimeResult, error) {
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	if m == nil || m.runtimeOperations == nil || m.archive == nil || m.agentBuilder == nil ||
		!validFreshLifetimeText(request.OperationID, 128) || !validFreshLifetimeText(request.SessionID, 256) ||
		!validModelInputHash(request.InputHash) {
		return nil, FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, ErrRuntimePublicationInvalid
	}

	unlockOperation := m.lockSessionOperation("\x00lifetime-operation:" + request.OperationID)
	defer unlockOperation()
	unlockSession := m.lockSessionOperation(request.SessionID)
	defer unlockSession()

	existing, found, err := m.readFreshLifetimeOperation(ctx, request.OperationID)
	if err != nil {
		m.fenceRuntimeRecovery(request.SessionID)
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}
	if found && !sameFreshLifetimeRequest(existing, request) {
		return nil, FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
	}
	topic := m.activeTopicSession(request.SessionID)
	if found {
		switch existing.State {
		case baldastate.RuntimeOperationTerminal:
			if existing.Kind == string(baldastate.RuntimeOperationKindLifetimePreference) &&
				existing.Outcome == string(baldastate.RuntimeOperationOutcomeContextReset) &&
				!m.completeTerminalFreshLifetimePublication(ctx, existing, topic) {
				result, resultErr := freshLifetimeRecoveryRequired()
				return nil, result, resultErr
			}
			return nil, m.replayFreshLifetime(ctx, topic, existing), nil
		case baldastate.RuntimeOperationApplying:
			topic.fenceRuntimeRecovery()
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		case baldastate.RuntimeOperationPrepared:
		default:
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		}
	}
	if topic == nil {
		if found {
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		}
		return nil, freshLifetimeUnavailable(), nil
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		result, resultErr := freshLifetimeRuntimeUnavailable(topic)
		return nil, result, resultErr
	}
	defer lease.Release()
	incumbent := lease.slot
	value := incumbent.value
	if value.runtime.SessionSvc == nil || value.session == nil || value.providerID == "" || value.archiveID == "" ||
		value.generation == math.MaxUint64 || value.runtime.RuntimeSnapshotID == "" {
		return nil, freshLifetimeUnavailable(), nil
	}
	if result, blocked := m.checkFreshLifetimePending(ctx, topic, value, existing, found); blocked {
		return nil, result, nil
	}
	if found && existing.Kind == string(baldastate.RuntimeOperationKindLifetime) {
		if existing.State != baldastate.RuntimeOperationPrepared || existing.ExpectedArchiveID != value.archiveID ||
			existing.ExpectedGeneration != value.generation || existing.TargetSnapshotID != value.runtime.RuntimeSnapshotID ||
			existing.TargetProviderBindingJSON != value.runtime.ProviderBindingJSON || existing.TargetModel != value.effectiveModel {
			topic.fenceRuntimeRecovery()
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		}
		archiveIntent, reserveErr := m.reserveFreshLifetimeArchive(ctx, topic, value, existing)
		if reserveErr != nil {
			return nil, freshLifetimeUnavailable(), nil
		}
		sess, sessionErr := m.ensureFreshLifetimeSession(ctx, topic, value, existing.TargetArchiveID)
		if sessionErr != nil {
			return nil, freshLifetimeUnavailable(), nil
		}
		candidate, candidateErr := incumbent.sharedLifetimeSuccessor(sess, existing.TargetArchiveID, string(baldastate.RuntimeOperationOutcomeContextReset))
		if candidateErr != nil {
			return nil, freshLifetimeUnavailable(), nil
		}
		return &PreparedFreshLifetime{
			request: request, operation: existing, archiveToken: archiveIntent.Token,
			incumbent: incumbent, candidate: candidate,
		}, FreshLifetimeResult{}, nil
	}
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	record, recordFound, err := m.sessionStore.GetBySessionID(recordCtx, request.SessionID)
	cancelRecord()
	if err != nil || !recordFound || record.ArchiveID != value.archiveID || record.RuntimeGeneration != value.generation ||
		record.RuntimeSnapshotID != value.runtime.RuntimeSnapshotID || record.ProviderBindingJSON != value.runtime.ProviderBindingJSON {
		return nil, freshLifetimeUnavailable(), nil
	}
	projection, projectedRequested := restoreFallbackModelProjection(record.RequestedModel, value.runtime.RecipeModel)
	if !sameOptionalModel(projectedRequested, record.RequestedModel) || projection != value.effectiveModel {
		return nil, freshLifetimeUnavailable(), nil
	}

	operation := existing
	ownsPreparation := !found
	if !found {
		operation = freshLifetimeIntent(request, value, record.RequestedModel, projection, m.currentTime())
		operation, err = m.runtimeOperations.Begin(ctx, operation)
		if err != nil {
			operation, found, err = m.readFreshLifetimeOperation(ctx, request.OperationID)
			if err != nil {
				topic.fenceRuntimeRecovery()
				result, resultErr := freshLifetimeRecoveryRequired()
				return nil, result, resultErr
			}
			if !found {
				return nil, freshLifetimeUnavailable(), nil
			}
			if !sameFreshLifetimeRequest(operation, request) {
				return nil, FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, nil
			}
		}
	}
	if operation.State == baldastate.RuntimeOperationTerminal {
		return nil, m.replayFreshLifetime(context.WithoutCancel(ctx), topic, operation), nil
	}
	if operation.State == baldastate.RuntimeOperationApplying {
		topic.fenceRuntimeRecovery()
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}
	if operation.State != baldastate.RuntimeOperationPrepared || !samePublicationIntent(operation, freshLifetimeIntent(request, value, record.RequestedModel, projection, operation.CreatedAt)) {
		topic.fenceRuntimeRecovery()
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}

	archiveIntent, err := m.reserveFreshLifetimeArchive(ctx, topic, value, operation)
	if err != nil {
		if !ownsPreparation {
			return nil, freshLifetimeUnavailable(), nil
		}
		return m.rollbackFreshLifetimePreparation(ctx, topic, value, operation, value.runtime.SessionSvc)
	}
	scoped, ok := m.runtimeManager.(UnpublishedSessionRuntimeManager)
	if !ok {
		if !ownsPreparation {
			return nil, freshLifetimeUnavailable(), nil
		}
		return m.rollbackFreshLifetimePreparation(ctx, topic, value, operation, value.runtime.SessionSvc)
	}
	runtimeCandidate, err := scoped.BuildUnpublishedSessionRuntime(ctx, SessionRuntimeRequest{
		Locator: topic.locator, UserID: value.userID, AgentSessionID: operation.TargetArchiveID,
		WorkspaceDir: topic.workspaceDir, RuntimeSnapshotID: operation.TargetSnapshotID,
		ProviderBindingJSON: operation.TargetProviderBindingJSON,
		ModelOverride:       cloneOptionalModel(operation.TargetRequestedModel),
	})
	if err != nil || runtimeCandidate == nil || runtimeCandidate.Runtime() == nil ||
		runtimeCandidate.ExpectedBaseToken() != value.runtime.RuntimeBaseToken ||
		runtimeCandidate.Runtime().RecipeModel != operation.TargetModel {
		if runtimeCandidate != nil {
			_ = runtimeCandidate.Abort(ctx)
		}
		if !ownsPreparation {
			return nil, freshLifetimeUnavailable(), nil
		}
		return m.rollbackFreshLifetimePreparation(ctx, topic, value, operation, value.runtime.SessionSvc)
	}
	sess, err := m.ensureFreshLifetimeSessionWithRuntime(ctx, topic, runtimeCandidate.Runtime(), value, operation.TargetArchiveID)
	if err != nil {
		if !ownsPreparation {
			_ = runtimeCandidate.Abort(ctx)
			return nil, freshLifetimeUnavailable(), nil
		}
		plan, result, rollbackErr := m.rollbackFreshLifetimePreparation(ctx, topic, value, operation, runtimeCandidate.Runtime().SessionSvc)
		if abortErr := runtimeCandidate.Abort(context.WithoutCancel(ctx)); abortErr != nil {
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		}
		return plan, result, rollbackErr
	}
	candidate, err := incumbent.independentLifetimeSuccessor(runtimeCandidate.Runtime(), sess, operation.TargetArchiveID, operation.TargetModel, string(baldastate.RuntimeOperationOutcomeContextReset))
	if err != nil {
		if !ownsPreparation {
			_ = runtimeCandidate.Abort(ctx)
			return nil, freshLifetimeUnavailable(), nil
		}
		plan, result, rollbackErr := m.rollbackFreshLifetimePreparation(ctx, topic, value, operation, runtimeCandidate.Runtime().SessionSvc)
		if abortErr := runtimeCandidate.Abort(context.WithoutCancel(ctx)); abortErr != nil {
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		}
		return plan, result, rollbackErr
	}
	return &PreparedFreshLifetime{
		request: request, operation: operation, archiveToken: archiveIntent.Token,
		ownsPreparation: ownsPreparation, incumbent: incumbent, candidate: candidate, runtimeCandidate: runtimeCandidate,
	}, FreshLifetimeResult{}, nil
}

func (m *Manager) completeTerminalFreshLifetimePublication(ctx context.Context, operation baldastate.RuntimeOperationRecord, topic *TopicSession) bool {
	if topic == nil || !m.freshLifetimeDurableTargetMatches(ctx, operation) {
		return topic == nil
	}
	value, ok := topic.runtimeValue()
	if !ok || !freshLifetimeActiveTargetMatches(value, operation) {
		return false
	}
	scoped, ok := m.runtimeManager.(UnpublishedSessionRuntimeManager)
	if !ok {
		return true
	}
	candidate, adopted := scoped.AdoptPromotedSessionRuntime(operation.SessionID, 0, SessionRuntimeRequest{
		Locator: topic.locator, UserID: value.userID, AgentSessionID: operation.TargetArchiveID,
		WorkspaceDir: topic.workspaceDir, RuntimeSnapshotID: operation.TargetSnapshotID,
		ProviderBindingJSON: operation.TargetProviderBindingJSON,
		ModelOverride:       cloneOptionalModel(operation.TargetRequestedModel), ExpectedRecipeModel: operation.TargetModel,
		RecoveryFence: true,
	})
	if !adopted {
		return scoped.CompleteSessionRuntimeRecovery(operation.SessionID, value.runtime.RuntimeBaseToken) == nil
	}
	if !freshLifetimeRuntimeMatchesSlot(candidate.Runtime(), topic.runtime) {
		return false
	}
	candidate.MarkPublished()
	if candidate.CompletePublication() != nil {
		return false
	}
	topic.runtimeMu.RLock()
	recoveryRequired := topic.recoveryRequired
	topic.runtimeMu.RUnlock()
	return !recoveryRequired || topic.completeRecoveredRuntime(
		topic.runtime,
		operation.TargetArchiveID,
		operation.ExpectedGeneration+1,
	)
}

func (m *Manager) checkFreshLifetimePending(
	ctx context.Context,
	topic *TopicSession,
	value runtimeSlotValue,
	existing baldastate.RuntimeOperationRecord,
	found bool,
) (FreshLifetimeResult, bool) {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	pending, err := m.archive.Pending(checkCtx, topic.locator.ChannelType, topic.locator.AddressKey)
	switch {
	case errors.Is(err, chatarchivecmd.ErrNotFound):
		return FreshLifetimeResult{}, false
	case err != nil:
		return freshLifetimeUnavailable(), true
	case found && existing.State == baldastate.RuntimeOperationPrepared &&
		pending.Token == deterministicFreshLifetimeArchiveToken(existing.OperationID) &&
		freshLifetimeArchiveIntentMatches(pending, existing, m.sessionRecordFromValue(topic, baldastate.SessionStatusActive, value)):
		return FreshLifetimeResult{}, false
	default:
		return FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, true
	}
}

// PublishFreshLifetime commits one already prepared local lifetime. The caller
// must hold the delivered TurnDispatcher replacement boundary.
func (m *Manager) PublishFreshLifetime(ctx context.Context, prepared *PreparedFreshLifetime) (FreshLifetimeResult, error) {
	if m == nil || prepared == nil || prepared.incumbent == nil || prepared.candidate == nil {
		return FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, ErrRuntimePublicationInvalid
	}
	prepared.settleOnce.Do(func() {
		prepared.settleResult, prepared.settleErr = m.publishFreshLifetime(ctx, prepared)
	})
	return prepared.settleResult, prepared.settleErr
}

func (m *Manager) publishFreshLifetime(ctx context.Context, prepared *PreparedFreshLifetime) (result FreshLifetimeResult, resultErr error) {
	published := false
	defer func() {
		if published {
			return
		}
		if err := m.releaseFreshLifetimeCandidate(ctx, prepared); err != nil {
			m.fenceRuntimeRecovery(prepared.request.SessionID)
			result, resultErr = freshLifetimeRecoveryRequired()
		}
	}()
	request := prepared.request
	unlockOperation := m.lockSessionOperation("\x00lifetime-operation:" + request.OperationID)
	defer unlockOperation()
	unlockSession := m.lockSessionOperation(request.SessionID)
	defer unlockSession()

	topic := m.activeTopicSession(request.SessionID)
	if topic == nil {
		return freshLifetimeRecoveryRequired()
	}
	operation, found, err := m.runtimeOperations.Get(ctx, request.OperationID)
	if err != nil || !found || !sameFreshLifetimeRequest(operation, request) {
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}
	if operation.State == baldastate.RuntimeOperationTerminal {
		return m.replayFreshLifetime(ctx, topic, operation), nil
	}
	if operation.State == baldastate.RuntimeOperationPrepared {
		if err := m.runtimeOperations.MarkApplying(ctx, operation.OperationID, operation.InputHash); err != nil {
			operation, found, err = m.runtimeOperations.Get(ctx, operation.OperationID)
			if err != nil || !found || !sameFreshLifetimeRequest(operation, request) || operation.State != baldastate.RuntimeOperationApplying {
				if topic != nil {
					topic.fenceRuntimeRecovery()
				}
				return freshLifetimeRecoveryRequired()
			}
		} else {
			operation.State = baldastate.RuntimeOperationApplying
		}
	}
	legacy := operation.Kind == string(baldastate.RuntimeOperationKindLifetime)
	candidateMatches := legacyFreshLifetimeCandidateMatches(prepared.incumbent, prepared.candidate, operation)
	if !legacy {
		candidateMatches = freshLifetimeCandidateMatches(prepared.incumbent, prepared.candidate, operation)
	}
	if operation.State != baldastate.RuntimeOperationApplying ||
		!topic.matchesRuntimeGuard(prepared.incumbent, operation.ExpectedArchiveID, operation.ExpectedGeneration) ||
		!candidateMatches ||
		(!legacy && (prepared.runtimeCandidate == nil || prepared.runtimeCandidate.ClaimForPublication() != nil)) ||
		!prepared.candidate.claimPublication() {
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}

	live := m.sessionRecordFromValue(topic, baldastate.SessionStatusActive, prepared.candidate.value)
	if !legacy {
		live.RequestedModel = cloneOptionalModel(operation.TargetRequestedModel)
	}
	if _, err := m.archive.Activate(ctx, prepared.archiveToken, &live); err != nil &&
		!m.freshLifetimeDurableTargetMatches(ctx, operation) {
		prepared.candidate.releasePublicationClaim(false)
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}
	if legacy {
		terminal, ok := m.finalizeFreshLifetime(ctx, operation)
		if !ok || !isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeContextReset, "") ||
			!topic.swapRuntime(prepared.incumbent, prepared.candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration) {
			prepared.candidate.releasePublicationClaim(false)
			topic.fenceRuntimeRecovery()
			return freshLifetimeRecoveryRequired()
		}
		prepared.candidate.releasePublicationClaim(true)
		published = true
		prepared.incumbent.beginDrain()
		return FreshLifetimeResult{Status: FreshLifetimeReset, Generation: operation.ExpectedGeneration + 1}, nil
	}
	if !legacy && prepared.runtimeCandidate.Promote() != nil {
		prepared.candidate.releasePublicationClaim(false)
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}
	if !topic.swapRuntime(prepared.incumbent, prepared.candidate, operation.ExpectedArchiveID, operation.ExpectedGeneration) {
		// The base is already promoted and remains protected by its barrier.
		// Retain its owner for forward recovery; abort must not erase it.
		prepared.candidate.releasePublicationClaim(true)
		published = true
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}
	prepared.candidate.releasePublicationClaim(true)
	if !legacy {
		prepared.runtimeCandidate.MarkPublished()
	}
	published = true
	prepared.incumbent.beginDrain()
	terminal, ok := m.finalizeFreshLifetime(ctx, operation)
	if !ok || !isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeContextReset, "") {
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}
	if prepared.runtimeCandidate.CompletePublication() != nil {
		topic.fenceRuntimeRecovery()
		return freshLifetimeRecoveryRequired()
	}
	return FreshLifetimeResult{Status: FreshLifetimeReset, Generation: operation.ExpectedGeneration + 1}, nil
}

// AbortFreshLifetime safely abandons a prepared candidate before the
// replacement boundary has begun.
func (m *Manager) AbortFreshLifetime(ctx context.Context, prepared *PreparedFreshLifetime) (FreshLifetimeResult, error) {
	if m == nil || prepared == nil {
		return FreshLifetimeResult{Status: FreshLifetimeConflict, Reason: baldastate.RuntimeOperationReasonInvalid}, ErrRuntimePublicationInvalid
	}
	prepared.settleOnce.Do(func() {
		prepared.settleResult, prepared.settleErr = m.abortFreshLifetime(ctx, prepared)
	})
	return prepared.settleResult, prepared.settleErr
}

func (m *Manager) abortFreshLifetime(ctx context.Context, prepared *PreparedFreshLifetime) (result FreshLifetimeResult, resultErr error) {
	defer func() {
		if err := m.releaseFreshLifetimeCandidate(ctx, prepared); err != nil {
			m.fenceRuntimeRecovery(prepared.request.SessionID)
			result, resultErr = freshLifetimeRecoveryRequired()
		}
	}()
	unlockOperation := m.lockSessionOperation("\x00lifetime-operation:" + prepared.request.OperationID)
	defer unlockOperation()
	unlockSession := m.lockSessionOperation(prepared.request.SessionID)
	defer unlockSession()
	if !prepared.ownsPreparation {
		operation, found, err := m.readFreshLifetimeOperation(ctx, prepared.request.OperationID)
		if err != nil || !found || !sameFreshLifetimeRequest(operation, prepared.request) {
			return freshLifetimeUnavailable(), nil
		}
		if operation.State == baldastate.RuntimeOperationTerminal {
			return m.replayFreshLifetime(context.WithoutCancel(ctx), m.activeTopicSession(prepared.request.SessionID), operation), nil
		}
		return freshLifetimeUnavailable(), nil
	}
	operation, found, err := m.readFreshLifetimeOperation(ctx, prepared.request.OperationID)
	if err != nil || !found || !sameFreshLifetimeRequest(operation, prepared.request) {
		m.fenceRuntimeRecovery(prepared.request.SessionID)
		return freshLifetimeRecoveryRequired()
	}
	topic := m.activeTopicSession(prepared.request.SessionID)
	if operation.State == baldastate.RuntimeOperationTerminal {
		return m.replayFreshLifetime(context.WithoutCancel(ctx), topic, operation), nil
	}
	if operation.State != baldastate.RuntimeOperationPrepared || topic == nil {
		m.fenceRuntimeRecovery(prepared.request.SessionID)
		return freshLifetimeRecoveryRequired()
	}
	service := prepared.incumbent.value.runtime.SessionSvc
	if prepared.runtimeCandidate != nil && prepared.runtimeCandidate.Runtime() != nil {
		service = prepared.runtimeCandidate.Runtime().SessionSvc
	}
	_, result, err = m.rollbackFreshLifetimePreparation(ctx, topic, prepared.incumbent.value, operation, service)
	return result, err
}

func freshLifetimeIntent(request FreshLifetimeRequest, value runtimeSlotValue, requested *string, projection string, at time.Time) baldastate.RuntimeOperationRecord {
	at = at.UTC()
	targetID := deterministicFreshLifetimeID(request)
	return baldastate.RuntimeOperationRecord{
		OperationID: request.OperationID, SessionID: request.SessionID, InputHash: request.InputHash,
		Kind: string(baldastate.RuntimeOperationKindLifetimePreference), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: value.archiveID, ExpectedGeneration: value.generation,
		TargetArchiveID: targetID, TargetSnapshotID: value.runtime.RuntimeSnapshotID,
		TargetProviderBindingJSON: value.runtime.ProviderBindingJSON, TargetModel: projection,
		TargetRequestedModel: cloneOptionalModel(requested),
		CreatedAt:            at, UpdatedAt: at,
	}
}

func deterministicFreshLifetimeID(request FreshLifetimeRequest) string {
	name := request.SessionID + "\x00" + request.OperationID + "\x00" + request.InputHash
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

func deterministicFreshLifetimeArchiveToken(operationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+"\x00archive")).String()
}

func sameFreshLifetimeRequest(operation baldastate.RuntimeOperationRecord, request FreshLifetimeRequest) bool {
	return operation.OperationID == request.OperationID && operation.SessionID == request.SessionID &&
		operation.InputHash == request.InputHash &&
		(operation.Kind == string(baldastate.RuntimeOperationKindLifetime) || operation.Kind == string(baldastate.RuntimeOperationKindLifetimePreference)) &&
		operation.TargetArchiveID == deterministicFreshLifetimeID(request)
}

func (m *Manager) reserveFreshLifetimeArchive(ctx context.Context, topic *TopicSession, value runtimeSlotValue, operation baldastate.RuntimeOperationRecord) (chatarchivecmd.Intent, error) {
	old, err := m.archive.Get(ctx, value.archiveID)
	if err != nil || old.Status != chatarchivecmd.StatusActive || old.StableID != topic.sessionID ||
		old.RuntimeID != value.sessionID || old.Location.Channel != topic.locator.ChannelType || old.Location.AddressKey != topic.locator.AddressKey {
		return chatarchivecmd.Intent{}, chatarchivecmd.ErrConflict
	}
	now := operation.CreatedAt.UTC()
	target := old
	target.ID = operation.TargetArchiveID
	target.RuntimeID = operation.TargetArchiveID
	target.Status = ""
	target.StartedAt = now
	target.ObservedAt = now
	target.EndedAt = time.Time{}
	target.EndReason = ""
	target.CoverageStart = time.Time{}
	target.PredecessorID = old.ID
	target.BootID = m.bootID
	intent := chatarchivecmd.Intent{
		Token: deterministicFreshLifetimeArchiveToken(operation.OperationID),
		Kind:  chatarchivecmd.KindFresh, Session: target, OldID: old.ID, Reason: string(BoundaryReasonReset), At: now,
	}
	return m.archive.Begin(ctx, intent)
}

func (m *Manager) readFreshLifetimeOperation(ctx context.Context, operationID string) (baldastate.RuntimeOperationRecord, bool, error) {
	operation, found, err := m.runtimeOperations.Get(ctx, operationID)
	if err == nil {
		return operation, found, nil
	}
	resolutionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return m.runtimeOperations.Get(resolutionCtx, operationID)
}

func (m *Manager) rollbackFreshLifetimePreparation(
	ctx context.Context,
	topic *TopicSession,
	value runtimeSlotValue,
	operation baldastate.RuntimeOperationRecord,
	service adksession.Service,
) (*PreparedFreshLifetime, FreshLifetimeResult, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	current, found, err := m.runtimeOperations.Get(cleanupCtx, operation.OperationID)
	if err != nil || !found || !samePublicationIntent(current, operation) ||
		current.State != baldastate.RuntimeOperationPrepared {
		topic.fenceRuntimeRecovery()
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}
	if err := m.deletePreparedFreshSession(cleanupCtx, service, value.appName, value.userID, current); err != nil {
		topic.fenceRuntimeRecovery()
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}
	pending, err := m.archive.Pending(cleanupCtx, topic.locator.ChannelType, topic.locator.AddressKey)
	switch {
	case err == nil:
		if pending.Token != deterministicFreshLifetimeArchiveToken(current.OperationID) ||
			!freshLifetimeArchiveIntentMatches(pending, current, m.sessionRecordFromValue(topic, baldastate.SessionStatusActive, value)) ||
			m.archive.Complete(cleanupCtx, pending.Token, chatarchivecmd.OutcomeAborted) != nil {
			topic.fenceRuntimeRecovery()
			result, resultErr := freshLifetimeRecoveryRequired()
			return nil, result, resultErr
		}
	case errors.Is(err, chatarchivecmd.ErrNotFound):
	default:
		topic.fenceRuntimeRecovery()
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}
	reason := baldastate.RuntimeOperationReasonUnavailable
	if errors.Is(ctx.Err(), context.Canceled) {
		reason = baldastate.RuntimeOperationReasonCanceled
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = baldastate.RuntimeOperationReasonDeadlineExceeded
	}
	if _, ok := m.finalizeFreshLifetimeAbortedWithReason(cleanupCtx, current, reason); !ok {
		topic.fenceRuntimeRecovery()
		result, resultErr := freshLifetimeRecoveryRequired()
		return nil, result, resultErr
	}
	return nil, FreshLifetimeResult{Status: FreshLifetimeUnavailable, Reason: reason}, nil
}

func (m *Manager) releaseFreshLifetimeCandidate(ctx context.Context, prepared *PreparedFreshLifetime) error {
	if prepared == nil || prepared.candidate == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	err := prepared.candidate.abortUnpublished(cleanupCtx)
	if prepared.runtimeCandidate != nil {
		err = errors.Join(err, prepared.runtimeCandidate.Abort(cleanupCtx))
	}
	return err
}

func (m *Manager) ensureFreshLifetimeSession(ctx context.Context, topic *TopicSession, value runtimeSlotValue, targetID string) (adksession.Session, error) {
	return m.ensureFreshLifetimeSessionWithRuntime(ctx, topic, &value.runtime, value, targetID)
}

func (m *Manager) ensureFreshLifetimeSessionWithRuntime(ctx context.Context, topic *TopicSession, runtime *BuiltRuntime, value runtimeSlotValue, targetID string) (adksession.Session, error) {
	if runtime == nil || runtime.SessionSvc == nil {
		return nil, ErrRuntimeUnavailable
	}
	request := &adksession.GetRequest{AppName: value.appName, UserID: value.userID, SessionID: targetID}
	response, err := runtime.SessionSvc.Get(ctx, request)
	if errors.Is(err, adksession.ErrNotFound) {
		_, createErr := m.agentBuilder.CreateFreshRuntimeSession(
			ctx, runtime, runtime.ProviderID, value.userID, targetID, topic.workspaceDir,
			RuntimeSessionContext{ArchiveID: targetID, BaldaSessionID: topic.sessionID, SessionBranch: topic.branchName},
		)
		response, err = runtime.SessionSvc.Get(ctx, request)
		if err != nil && createErr != nil {
			return nil, ErrRuntimeUnavailable
		}
	}
	if err != nil || response == nil || !freshLifetimeSessionMatches(response.Session, value.appName, value.userID, targetID, topic.sessionID) {
		return nil, ErrRuntimeUnavailable
	}
	return response.Session, nil
}

func freshLifetimeSessionMatches(sess adksession.Session, appName, userID, runtimeID, stableID string) bool {
	return sess != nil && sess.AppName() == appName && sess.UserID() == userID && sess.ID() == runtimeID &&
		sess.Events() != nil && sess.Events().Len() == 0 &&
		archiveStateString(sess, chatarchivecmd.EpochStateKey) == runtimeID &&
		archiveStateString(sess, baldaSessionIDStateKey) == stableID
}

func freshLifetimeCandidateMatches(incumbent, candidate *RuntimeSlot, operation baldastate.RuntimeOperationRecord) bool {
	if incumbent == nil || candidate == nil || !runtimeCandidateMatches(incumbent, candidate, operation, baldastate.RuntimeOperationOutcomeContextReset) {
		return false
	}
	left, right := incumbent.value, candidate.value
	return right.runtime.RuntimeBaseToken != 0 && right.runtime.RuntimeBaseToken != left.runtime.RuntimeBaseToken &&
		right.runtime.RuntimeSnapshotID == left.runtime.RuntimeSnapshotID &&
		right.runtime.ProviderBindingJSON == left.runtime.ProviderBindingJSON &&
		right.runtime.RecipeModel == operation.TargetModel && right.runtime.ProviderID == left.providerID &&
		right.providerID == left.providerID &&
		right.sessionID == operation.TargetArchiveID && right.appName == left.appName && right.userID == left.userID &&
		freshLifetimeSessionMatches(right.session, left.appName, left.userID, operation.TargetArchiveID, operation.SessionID)
}

func legacyFreshLifetimeCandidateMatches(incumbent, candidate *RuntimeSlot, operation baldastate.RuntimeOperationRecord) bool {
	if incumbent == nil || candidate == nil || !runtimeCandidateMatches(incumbent, candidate, operation, baldastate.RuntimeOperationOutcomeContextReset) {
		return false
	}
	left, right := incumbent.value, candidate.value
	return sameIdentityObject(left.runtime.Agent, right.runtime.Agent) &&
		sameIdentityObject(left.runtime.Runner, right.runtime.Runner) &&
		sameIdentityObject(left.runtime.SessionSvc, right.runtime.SessionSvc) &&
		sameIdentityObject(left.runtime.SessionControl, right.runtime.SessionControl) &&
		right.providerID == left.providerID && right.sessionID == operation.TargetArchiveID &&
		freshLifetimeSessionMatches(right.session, left.appName, left.userID, operation.TargetArchiveID, operation.SessionID)
}

func (m *Manager) finalizeFreshLifetime(ctx context.Context, operation baldastate.RuntimeOperationRecord) (baldastate.RuntimeOperationRecord, bool) {
	at := m.currentTime().UTC()
	if operation.UpdatedAt.After(at) {
		at = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID, InputHash: operation.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeContextReset, At: at,
	})
	if err == nil {
		return terminal, m.freshLifetimeDurableTargetMatches(ctx, operation)
	}
	terminal, found, getErr := m.runtimeOperations.Get(ctx, operation.OperationID)
	return terminal, getErr == nil && found && isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeContextReset, "") &&
		m.freshLifetimeDurableTargetMatches(ctx, operation)
}

func (m *Manager) finalizeFreshLifetimeAbortedWithReason(
	ctx context.Context,
	operation baldastate.RuntimeOperationRecord,
	reason baldastate.RuntimeOperationReason,
) (baldastate.RuntimeOperationRecord, bool) {
	at := m.currentTime().UTC()
	if operation.UpdatedAt.After(at) {
		at = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID, InputHash: operation.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeAborted, ReasonCode: reason, At: at,
	})
	return terminal, err == nil && isPublicationTerminal(terminal, operation, baldastate.RuntimeOperationOutcomeAborted, reason)
}

func (m *Manager) freshLifetimeDurableTargetMatches(ctx context.Context, operation baldastate.RuntimeOperationRecord) bool {
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found {
		return false
	}
	return record.ArchiveID == operation.TargetArchiveID && record.RuntimeGeneration == operation.ExpectedGeneration+1 &&
		record.RuntimeSnapshotID == operation.TargetSnapshotID && record.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		sameOptionalModel(record.RequestedModel, operation.TargetRequestedModel) &&
		record.EffectiveModel == operation.TargetModel && record.RuntimeOutcome == string(baldastate.RuntimeOperationOutcomeContextReset)
}

func (m *Manager) replayFreshLifetime(ctx context.Context, topic *TopicSession, operation baldastate.RuntimeOperationRecord) FreshLifetimeResult {
	if operation.Outcome == string(baldastate.RuntimeOperationOutcomeContextReset) && m.freshLifetimeDurableTargetMatches(ctx, operation) {
		return FreshLifetimeResult{Status: FreshLifetimeReset, Generation: operation.ExpectedGeneration + 1}
	}
	if operation.Outcome == string(baldastate.RuntimeOperationOutcomeAborted) {
		return freshLifetimeUnavailable()
	}
	if topic != nil {
		topic.fenceRuntimeRecovery()
	}
	result, _ := freshLifetimeRecoveryRequired()
	return result
}

func (m *Manager) deletePreparedFreshSession(ctx context.Context, service adksession.Service, appName, userID string, operation baldastate.RuntimeOperationRecord) error {
	response, err := service.Get(ctx, &adksession.GetRequest{AppName: appName, UserID: userID, SessionID: operation.TargetArchiveID})
	if errors.Is(err, adksession.ErrNotFound) {
		return nil
	}
	if err != nil || response == nil || !freshLifetimeSessionMatches(response.Session, appName, userID, operation.TargetArchiveID, operation.SessionID) {
		return ErrRuntimeRecoveryRequired
	}
	return service.Delete(ctx, &adksession.DeleteRequest{AppName: appName, UserID: userID, SessionID: operation.TargetArchiveID})
}

func validFreshLifetimeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func freshLifetimeUnavailable() FreshLifetimeResult {
	return FreshLifetimeResult{Status: FreshLifetimeUnavailable, Reason: baldastate.RuntimeOperationReasonUnavailable}
}

func freshLifetimeRuntimeUnavailable(topic *TopicSession) (FreshLifetimeResult, error) {
	if topic != nil {
		topic.runtimeMu.RLock()
		recovery := topic.recoveryRequired
		topic.runtimeMu.RUnlock()
		if recovery {
			return freshLifetimeRecoveryRequired()
		}
	}
	return freshLifetimeUnavailable(), nil
}

func freshLifetimeRecoveryRequired() (FreshLifetimeResult, error) {
	return FreshLifetimeResult{Status: FreshLifetimeRecoveryRequired, Reason: baldastate.RuntimeOperationReasonUnavailable}, ErrRuntimeRecoveryRequired
}
