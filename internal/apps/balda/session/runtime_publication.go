package session

import (
	"context"
	"errors"
	"math"
	"time"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

// publishRuntime is the session-owned publication seam. Callers must already
// hold TurnDispatcher admission; this method adds the existing Manager
// operation lock and performs no command-specific work.
func (m *Manager) publishRuntime(
	ctx context.Context,
	incumbent, candidate *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
) (baldastate.RuntimeOperationRecord, bool, error) {
	if m == nil || m.runtimeOperations == nil || incumbent == nil || candidate == nil || incumbent == candidate ||
		intent.State != baldastate.RuntimeOperationPrepared || intent.SessionID == "" ||
		(outcome != baldastate.RuntimeOperationOutcomeContextPreserved && outcome != baldastate.RuntimeOperationOutcomeContextReset) ||
		!runtimeCandidateMatches(incumbent, candidate, intent, outcome) {
		return baldastate.RuntimeOperationRecord{}, false, ErrRuntimePublicationInvalid
	}

	return m.publishRuntimeState(ctx, incumbent, candidate, intent, outcome, false, "")
}

// publishApplyingModelRuntime completes a model operation whose external work
// has already begun. It deliberately shares the guarded durable commit and
// pointer swap with every other runtime publication.
func (m *Manager) publishApplyingModelRuntime(
	ctx context.Context,
	incumbent, candidate *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
) (baldastate.RuntimeOperationRecord, bool, error) {
	if m == nil || m.runtimeOperations == nil ||
		intent.State != baldastate.RuntimeOperationApplying || intent.Kind != string(baldastate.RuntimeOperationKindModel) ||
		intent.SessionID == "" || outcome != baldastate.RuntimeOperationOutcomeContextPreserved {
		return baldastate.RuntimeOperationRecord{}, false, ErrRuntimePublicationInvalid
	}
	return m.publishRuntimeState(ctx, incumbent, candidate, intent, outcome, true, "")
}

func (m *Manager) publishRuntimeState(
	ctx context.Context,
	incumbent, candidate *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
	alreadyApplying bool,
	terminalReason baldastate.RuntimeOperationReason,
) (baldastate.RuntimeOperationRecord, bool, error) {
	unlock := m.lockSessionOperation(intent.SessionID)
	defer unlock()
	return m.publishRuntimeStateLocked(ctx, incumbent, candidate, intent, outcome, alreadyApplying, terminalReason)
}

func (m *Manager) publishRuntimeStateLocked(
	ctx context.Context,
	incumbent, candidate *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
	alreadyApplying bool,
	terminalReason baldastate.RuntimeOperationReason,
) (baldastate.RuntimeOperationRecord, bool, error) {
	topic := m.activeTopicSession(intent.SessionID)
	if topic == nil {
		if alreadyApplying {
			_ = candidate.abortUnpublished(ctx)
			return baldastate.RuntimeOperationRecord{}, false, ErrRuntimeRecoveryRequired
		}
		return baldastate.RuntimeOperationRecord{}, false, ErrRuntimePublicationConflict
	}

	operation, err := m.loadPublicationOperation(ctx, intent, alreadyApplying)
	if err != nil {
		if alreadyApplying {
			return m.failApplyingPublication(ctx, topic, candidate, operation, false)
		}
		return baldastate.RuntimeOperationRecord{}, false, m.publicationStoreError(ctx, topic, intent, err, true)
	}
	if !samePublicationIntent(operation, intent) {
		return m.failPublication(ctx, topic, candidate, operation, alreadyApplying, false)
	}
	if operation.State == baldastate.RuntimeOperationTerminal {
		if isPublicationTerminal(operation, intent, baldastate.RuntimeOperationOutcomeConflict, baldastate.RuntimeOperationReasonGuardConflict) {
			if alreadyApplying {
				_ = candidate.abortUnpublished(ctx)
			}
			return operation, false, ErrRuntimePublicationConflict
		}
		committed := topic.matchesCommittedRuntime(incumbent, intent, outcome)
		if alreadyApplying {
			committed = topic.matchesCommittedModelRuntime(incumbent, intent, outcome)
		}
		if !isPublicationTerminal(operation, intent, outcome, terminalReason) || !committed {
			return m.failPublication(ctx, topic, candidate, operation, alreadyApplying, false)
		}
		return operation, false, nil
	}
	if alreadyApplying && (!runtimeCandidateMatches(incumbent, candidate, intent, outcome) ||
		!modelCandidateIdentityMatches(incumbent, candidate)) {
		return m.failApplyingPublication(ctx, topic, candidate, operation, false)
	}
	wantState := baldastate.RuntimeOperationPrepared
	if alreadyApplying {
		wantState = baldastate.RuntimeOperationApplying
	}
	if operation.State != wantState {
		return m.failPublication(ctx, topic, candidate, operation, alreadyApplying, false)
	}
	if !topic.matchesRuntimeGuard(incumbent, intent.ExpectedArchiveID, intent.ExpectedGeneration) {
		if alreadyApplying {
			return m.failApplyingPublication(ctx, topic, candidate, operation, false)
		}
		terminal, finalizeErr := m.finalizePublicationConflict(ctx, intent)
		if finalizeErr != nil {
			return baldastate.RuntimeOperationRecord{}, false, m.publicationStoreError(ctx, topic, intent, finalizeErr, false)
		}
		if !isPublicationTerminal(terminal, intent, baldastate.RuntimeOperationOutcomeConflict, baldastate.RuntimeOperationReasonGuardConflict) {
			topic.fenceRuntimeRecovery()
			return baldastate.RuntimeOperationRecord{}, false, ErrRuntimeRecoveryRequired
		}
		return terminal, false, ErrRuntimePublicationConflict
	}
	if !candidate.claimPublication() {
		if alreadyApplying {
			return m.failApplyingPublication(ctx, topic, candidate, operation, false)
		}
		terminal, finalizeErr := m.finalizePublicationConflict(ctx, intent)
		if finalizeErr != nil {
			return baldastate.RuntimeOperationRecord{}, false, m.publicationStoreError(ctx, topic, intent, finalizeErr, false)
		}
		if !isPublicationTerminal(terminal, intent, baldastate.RuntimeOperationOutcomeConflict, baldastate.RuntimeOperationReasonGuardConflict) {
			topic.fenceRuntimeRecovery()
			return baldastate.RuntimeOperationRecord{}, false, ErrRuntimeRecoveryRequired
		}
		return terminal, false, ErrRuntimePublicationConflict
	}

	if !alreadyApplying {
		if err := m.runtimeOperations.MarkApplying(ctx, intent.OperationID, intent.InputHash); err != nil {
			candidate.releasePublicationClaim(false)
			return baldastate.RuntimeOperationRecord{}, false, m.publicationStoreError(ctx, topic, intent, err, false)
		}
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: intent.OperationID,
		InputHash:   intent.InputHash,
		Outcome:     outcome,
		ReasonCode:  terminalReason,
		At:          time.Now().UTC(),
	})
	if errors.Is(err, baldastate.ErrRuntimeOperationConflict) {
		if alreadyApplying {
			return m.failApplyingPublication(ctx, topic, candidate, operation, true)
		}
		candidate.releasePublicationClaim(false)
		conflict, finalizeErr := m.finalizePublicationConflict(ctx, intent)
		if finalizeErr != nil {
			return baldastate.RuntimeOperationRecord{}, false, m.publicationStoreError(ctx, topic, intent, finalizeErr, false)
		}
		if !isPublicationTerminal(conflict, intent, baldastate.RuntimeOperationOutcomeConflict, baldastate.RuntimeOperationReasonGuardConflict) {
			topic.fenceRuntimeRecovery()
			return baldastate.RuntimeOperationRecord{}, false, ErrRuntimeRecoveryRequired
		}
		return conflict, false, ErrRuntimePublicationConflict
	}
	if err != nil {
		if alreadyApplying {
			return m.failApplyingPublication(ctx, topic, candidate, operation, true)
		}
		candidate.releasePublicationClaim(false)
		return baldastate.RuntimeOperationRecord{}, false, m.publicationStoreError(ctx, topic, intent, err, false)
	}
	if !isPublicationTerminal(terminal, intent, outcome, terminalReason) {
		return m.failPublication(ctx, topic, candidate, terminal, alreadyApplying, true)
	}
	if !topic.swapRuntime(incumbent, candidate, intent.ExpectedArchiveID, intent.ExpectedGeneration) {
		return m.failPublication(ctx, topic, candidate, terminal, alreadyApplying, true)
	}
	candidate.releasePublicationClaim(true)
	incumbent.beginDrain()
	return terminal, true, nil
}

func (m *Manager) failPublication(
	ctx context.Context,
	topic *TopicSession,
	candidate *RuntimeSlot,
	operation baldastate.RuntimeOperationRecord,
	applying, claimed bool,
) (baldastate.RuntimeOperationRecord, bool, error) {
	if applying {
		return m.failApplyingPublication(ctx, topic, candidate, operation, claimed)
	}
	if claimed {
		candidate.releasePublicationClaim(false)
	}
	topic.fenceRuntimeRecovery()
	return operation, false, ErrRuntimeRecoveryRequired
}

func (*Manager) failApplyingPublication(
	ctx context.Context,
	topic *TopicSession,
	candidate *RuntimeSlot,
	operation baldastate.RuntimeOperationRecord,
	claimed bool,
) (baldastate.RuntimeOperationRecord, bool, error) {
	topic.fenceRuntimeRecovery()
	if claimed {
		candidate.releasePublicationClaim(false)
	}
	_ = candidate.abortUnpublished(ctx)
	return operation, false, ErrRuntimeRecoveryRequired
}

func (m *Manager) loadPublicationOperation(ctx context.Context, intent baldastate.RuntimeOperationRecord, applying bool) (baldastate.RuntimeOperationRecord, error) {
	if !applying {
		return m.runtimeOperations.Begin(ctx, intent)
	}
	operation, found, err := m.runtimeOperations.Get(ctx, intent.OperationID)
	if err != nil {
		return baldastate.RuntimeOperationRecord{}, err
	}
	if !found {
		return baldastate.RuntimeOperationRecord{}, baldastate.ErrRuntimeOperationConflict
	}
	return operation, nil
}

func runtimeCandidateMatches(
	incumbent, candidate *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
) bool {
	if incumbent == nil || candidate == nil || intent.ExpectedGeneration == math.MaxUint64 {
		return false
	}
	incumbentValue := incumbent.value
	candidateValue := candidate.value
	targetArchive := intent.TargetArchiveID
	if targetArchive == "" {
		targetArchive = incumbentValue.archiveID
	}
	targetSnapshot := intent.TargetSnapshotID
	if targetSnapshot == "" {
		targetSnapshot = incumbentValue.runtime.RuntimeSnapshotID
	}
	targetBinding := intent.TargetProviderBindingJSON
	if targetBinding == "" {
		targetBinding = incumbentValue.runtime.ProviderBindingJSON
	}
	targetModel := intent.TargetModel
	if targetModel == "" {
		targetModel = incumbentValue.effectiveModel
	}
	return candidateValue.generation == intent.ExpectedGeneration+1 &&
		candidateValue.archiveID == targetArchive &&
		candidateValue.runtime.RuntimeSnapshotID == targetSnapshot &&
		candidateValue.runtime.ProviderBindingJSON == targetBinding &&
		candidateValue.effectiveModel == targetModel &&
		candidateValue.outcome == string(outcome)
}

func modelCandidateIdentityMatches(incumbent, candidate *RuntimeSlot) bool {
	if incumbent == nil || candidate == nil {
		return false
	}
	incumbentValue, candidateValue := incumbent.value, candidate.value
	return sameIdentityObject(candidateValue.runtime.SessionSvc, incumbentValue.runtime.SessionSvc) &&
		sameIdentityObject(candidateValue.session, incumbentValue.session) &&
		candidateValue.appName == incumbentValue.appName && candidateValue.userID == incumbentValue.userID &&
		candidateValue.sessionID == incumbentValue.sessionID
}

func samePublicationIntent(got, want baldastate.RuntimeOperationRecord) bool {
	return got.OperationID == want.OperationID && got.SessionID == want.SessionID && got.InputHash == want.InputHash &&
		got.Kind == want.Kind && got.ExpectedArchiveID == want.ExpectedArchiveID && got.ExpectedGeneration == want.ExpectedGeneration &&
		got.TargetArchiveID == want.TargetArchiveID && got.TargetSnapshotID == want.TargetSnapshotID &&
		got.TargetProviderBindingJSON == want.TargetProviderBindingJSON && got.TargetModel == want.TargetModel
}

func isPublicationTerminal(
	operation baldastate.RuntimeOperationRecord,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
	reason baldastate.RuntimeOperationReason,
) bool {
	return samePublicationIntent(operation, intent) && operation.State == baldastate.RuntimeOperationTerminal &&
		operation.Outcome == string(outcome) && operation.ReasonCode == string(reason) && operation.TerminalAt != nil
}

func (m *Manager) activeTopicSession(sessionID string) *TopicSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

func (m *Manager) finalizePublicationConflict(
	ctx context.Context,
	intent baldastate.RuntimeOperationRecord,
) (baldastate.RuntimeOperationRecord, error) {
	return m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: intent.OperationID,
		InputHash:   intent.InputHash,
		Outcome:     baldastate.RuntimeOperationOutcomeConflict,
		ReasonCode:  baldastate.RuntimeOperationReasonGuardConflict,
		At:          time.Now().UTC(),
	})
}

func (m *Manager) publicationStoreError(
	ctx context.Context,
	topic *TopicSession,
	intent baldastate.RuntimeOperationRecord,
	err error,
	beforeIntent bool,
) error {
	if beforeIntent && errors.Is(err, baldastate.ErrRuntimeOperationInvalid) {
		return ErrRuntimePublicationInvalid
	}
	if errors.Is(err, baldastate.ErrRuntimeOperationConflict) {
		if existing, found, getErr := m.runtimeOperations.Get(ctx, intent.OperationID); getErr == nil && found && existing.State == baldastate.RuntimeOperationTerminal {
			return ErrRuntimePublicationConflict
		}
	}
	topic.fenceRuntimeRecovery()
	return ErrRuntimeRecoveryRequired
}

func (s *TopicSession) matchesRuntimeGuard(expected *RuntimeSlot, archiveID string, generation uint64) bool {
	if s == nil {
		return false
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return !s.recoveryRequired && s.runtime == expected && expected != nil &&
		expected.value.archiveID == archiveID && expected.value.generation == generation
}

func (s *TopicSession) matchesCommittedRuntime(
	incumbent *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
) bool {
	if s == nil {
		return false
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return !s.recoveryRequired && runtimeCandidateMatches(incumbent, s.runtime, intent, outcome)
}

func (s *TopicSession) matchesCommittedModelRuntime(
	incumbent *RuntimeSlot,
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
) bool {
	if s == nil {
		return false
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return !s.recoveryRequired && runtimeCandidateMatches(incumbent, s.runtime, intent, outcome) &&
		modelCandidateIdentityMatches(incumbent, s.runtime)
}

func (s *TopicSession) swapRuntime(expected, successor *RuntimeSlot, archiveID string, generation uint64) bool {
	if s == nil || successor == nil {
		return false
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.recoveryRequired || s.runtime != expected || expected == nil ||
		expected.value.archiveID != archiveID || expected.value.generation != generation {
		return false
	}
	s.runtime = successor
	return true
}

func (s *TopicSession) fenceRuntimeRecovery() {
	if s == nil {
		return
	}
	s.runtimeMu.Lock()
	s.recoveryRequired = true
	s.runtimeMu.Unlock()
}
