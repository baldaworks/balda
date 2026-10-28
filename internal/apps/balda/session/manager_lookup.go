package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

// GetSession returns the in-memory session for the given locator.
func (m *Manager) GetSession(locator SessionLocator) (*TopicSession, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()

	m.mu.RLock()
	ts := m.sessions[sessionID]
	activeSessions := len(m.sessions)
	m.mu.RUnlock()

	if ts == nil {
		m.logger.Debug().
			Str("session_id", sessionID).
			Str("channel_type", locator.ChannelType).
			Str("address_key", locator.AddressKey).
			Int("active_sessions", activeSessions).
			Msg("session not found")
		return nil, fmt.Errorf("no session for %s", locator.AddressKey)
	}

	if m.archive != nil && ts.GetArchiveID() != "" {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		pending, err := m.archive.Pending(ctx, ts.locator.ChannelType, ts.locator.AddressKey)
		if err != nil && !errors.Is(err, chatarchivecmd.ErrNotFound) {
			return nil, err
		}
		if err == nil && pending.Kind == chatarchivecmd.KindDestroy {
			return nil, chatarchivecmd.ErrConflict
		}
	}
	return ts, nil
}

// EnsureSession returns an active session, restores saved metadata, or creates a
// new session when no metadata exists. agentName is the label for new sessions.
func (m *Manager) EnsureSession(ctx context.Context, sessionCtx SessionContext, agentName string) (*TopicSession, error) {
	return m.prepareSession(ctx, sessionCtx, agentName, true)
}

// EnsureTransientSession creates a private run session without address-indexed metadata.
func (m *Manager) EnsureTransientSession(ctx context.Context, sessionCtx SessionContext, agentName string) (*TopicSession, error) {
	sessionID := strings.TrimSpace(sessionCtx.Locator.SessionID)
	if !isPrivateScheduleSessionID(sessionID) && !isPrivateWebhookSessionID(sessionID) {
		return nil, fmt.Errorf("private execution session id is required")
	}
	resolvedCtx, err := m.resolveExecutionContext(sessionCtx)
	if err != nil {
		return nil, err
	}
	sessionCtx = resolvedCtx
	if existing, err := m.GetSession(sessionCtx.Locator); err == nil {
		if existing.kind != sessionCtx.Kind || existing.ownerJobID != sessionCtx.OwnerJobID {
			return nil, fmt.Errorf("active session execution context does not match requested kind and owner")
		}
		return existing, nil
	}
	if err := m.createSession(ctx, sessionCtx, agentName, nil, true); err != nil {
		return nil, err
	}
	return m.GetSession(sessionCtx.Locator)
}

// RestoreSession restores a saved session without creating one when metadata is absent.
func (m *Manager) RestoreSession(ctx context.Context, sessionCtx SessionContext) (*TopicSession, error) {
	return m.prepareSession(ctx, sessionCtx, "", false)
}

func (m *Manager) prepareSession(ctx context.Context, sessionCtx SessionContext, label string, createIfMissing bool) (*TopicSession, error) {
	endLifecycle, err := m.beginLifecycleOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer endLifecycle()
	if m.sessionStore == nil {
		return nil, fmt.Errorf("session metadata store is required")
	}
	for {
		if err := m.lifecycleInterruption(ctx); err != nil {
			return nil, err
		}
		record, found, err := m.sessionStore.GetByAddress(ctx, sessionCtx.Locator.ChannelType, sessionCtx.Locator.AddressKey)
		if err != nil {
			return nil, fmt.Errorf("read session metadata: %w", err)
		}
		lookupID := strings.TrimSpace(sessionCtx.Locator.SessionID)
		if found {
			lookupID = strings.TrimSpace(record.SessionID)
		}
		unlock := m.lockSessionOperation(lookupID)
		// Metadata can be replaced while waiting for the session operation. Resolve
		// its authoritative identity again before opening or creating any runtime.
		record, found, err = m.sessionStore.GetByAddress(ctx, sessionCtx.Locator.ChannelType, sessionCtx.Locator.AddressKey)
		if err != nil {
			unlock()
			return nil, fmt.Errorf("read session metadata: %w", err)
		}
		currentID := strings.TrimSpace(sessionCtx.Locator.SessionID)
		if found {
			currentID = strings.TrimSpace(record.SessionID)
		}
		if currentID != lookupID {
			unlock()
			continue
		}
		if err := m.recoverRuntimeOperation(ctx, currentID); err != nil {
			unlock()
			return nil, err
		}
		// Model recovery may atomically advance the canonical generation. Reload
		// metadata before opening the runtime so the restored slot uses that exact
		// committed generation and model.
		record, found, err = m.sessionStore.GetByAddress(ctx, sessionCtx.Locator.ChannelType, sessionCtx.Locator.AddressKey)
		if err != nil {
			unlock()
			return nil, fmt.Errorf("read session metadata after recovery: %w", err)
		}
		sess, err := m.prepareSessionLocked(ctx, sessionCtx, label, createIfMissing, record, found)
		unlock()
		return sess, err
	}
}

func (m *Manager) prepareSessionLocked(ctx context.Context, sessionCtx SessionContext, label string, createIfMissing bool, record baldastate.SessionRecord, found bool) (*TopicSession, error) {
	var persisted *baldastate.SessionRecord
	if found {
		if strings.TrimSpace(record.Status) != "" && record.Status != baldastate.SessionStatusActive {
			return nil, fmt.Errorf("persisted session for %s is not active", sessionCtx.Locator.AddressKey)
		}
		locator, err := LocatorFromRecord(record)
		if err != nil {
			return nil, fmt.Errorf("decode persisted session locator: %w", err)
		}
		sessionCtx.Locator = locator
		if userID := strings.TrimSpace(record.UserID); userID != "" {
			sessionCtx.UserID = userID
		}
		label = strings.TrimSpace(record.AgentName)
		if label == "" {
			label = defaultSessionLabel
		}
		persisted = &record
	}
	resolvedCtx, err := m.resolveExecutionContext(sessionCtx)
	if err != nil {
		return nil, err
	}
	sessionCtx = resolvedCtx
	sessionID := strings.TrimSpace(sessionCtx.Locator.SessionID)
	m.mu.RLock()
	active := m.sessions[sessionID]
	m.mu.RUnlock()
	if active != nil && found && !runtimeMatchesSessionRecord(active, record) {
		active.fenceRuntimeRecovery()
		return nil, ErrRuntimeRecoveryRequired
	}
	if active != nil {
		if err := m.reconcileArchiveDestruction(ctx, sessionCtx.Locator, sessionCtx.UserID); err != nil {
			return nil, err
		}
		m.mu.RLock()
		active = m.sessions[sessionID]
		m.mu.RUnlock()
	}
	if active != nil {
		if active.kind != sessionCtx.Kind || active.ownerJobID != strings.TrimSpace(sessionCtx.OwnerJobID) {
			return nil, fmt.Errorf("active session execution context does not match requested kind and owner")
		}
		if found && !runtimeMatchesSessionRecord(active, record) {
			active.fenceRuntimeRecovery()
			return nil, ErrRuntimeRecoveryRequired
		}
		return active, nil
	}
	if !found && !createIfMissing {
		return nil, fmt.Errorf("%w for %s", ErrNoPersistedSession, sessionCtx.Locator.AddressKey)
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		return nil, err
	}
	if found {
		m.logger.Info().Str("session_id", sessionID).Str("channel_type", sessionCtx.Locator.ChannelType).
			Str("address_key", sessionCtx.Locator.AddressKey).Str("label", label).Msg("restoring session from persisted metadata")
	}
	if err := m.createSessionLocked(ctx, sessionCtx, label, persisted, false); err != nil {
		return nil, err
	}
	m.mu.RLock()
	prepared := m.sessions[sessionID]
	m.mu.RUnlock()
	if prepared == nil {
		return nil, fmt.Errorf("prepared session %q was not published", sessionID)
	}
	return prepared, nil
}

func (m *Manager) recoverRuntimeOperation(ctx context.Context, sessionID string) error {
	if m.runtimeOperations == nil {
		return nil
	}
	operation, found, err := m.runtimeOperations.GetUnresolvedBySession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.fenceRuntimeRecovery(sessionID)
		return ErrRuntimeRecoveryRequired
	}
	if !found {
		return nil
	}
	if operation.State == baldastate.RuntimeOperationApplying && operation.Kind == string(baldastate.RuntimeOperationKindModel) {
		return m.recoverApplyingModelOperation(ctx, operation)
	}
	if operation.Kind == string(baldastate.RuntimeOperationKindLifetime) {
		switch operation.State {
		case baldastate.RuntimeOperationPrepared:
			return m.recoverPreparedFreshLifetimeOperation(ctx, operation)
		case baldastate.RuntimeOperationApplying:
			return m.recoverApplyingFreshLifetimeOperation(ctx, operation)
		}
	}
	if operation.State != baldastate.RuntimeOperationPrepared {
		m.fenceRuntimeRecovery(sessionID)
		return ErrRuntimeRecoveryRequired
	}
	terminalAt := time.Now().UTC()
	if operation.UpdatedAt.After(terminalAt) {
		terminalAt = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID,
		InputHash:   operation.InputHash,
		Outcome:     baldastate.RuntimeOperationOutcomeAborted,
		ReasonCode:  baldastate.RuntimeOperationReasonRecoveryAborted,
		At:          terminalAt,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.fenceRuntimeRecovery(sessionID)
		return ErrRuntimeRecoveryRequired
	}
	if terminal.State != baldastate.RuntimeOperationTerminal ||
		terminal.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) ||
		terminal.ReasonCode != string(baldastate.RuntimeOperationReasonRecoveryAborted) {
		m.fenceRuntimeRecovery(sessionID)
		return ErrRuntimeRecoveryRequired
	}
	return nil
}

func (m *Manager) fenceRuntimeRecovery(sessionID string) {
	m.mu.RLock()
	topic := m.sessions[strings.TrimSpace(sessionID)]
	m.mu.RUnlock()
	if topic != nil {
		topic.fenceRuntimeRecovery()
	}
}

func runtimeMatchesSessionRecord(topic *TopicSession, record baldastate.SessionRecord) bool {
	value, ok := topic.runtimeValue()
	if !ok {
		return false
	}
	bindingMatches := strings.TrimSpace(value.runtime.ProviderBindingJSON) == strings.TrimSpace(record.ProviderBindingJSON)
	if record.RuntimeGeneration == 0 && strings.TrimSpace(record.ProviderBindingJSON) == "" {
		bindingMatches = true
	}
	return value.archiveID == strings.TrimSpace(record.ArchiveID) &&
		value.generation == record.RuntimeGeneration &&
		strings.TrimSpace(value.runtime.RuntimeSnapshotID) == strings.TrimSpace(record.RuntimeSnapshotID) &&
		bindingMatches &&
		value.effectiveModel == strings.TrimSpace(record.EffectiveModel) &&
		value.outcome == strings.TrimSpace(record.RuntimeOutcome)
}

func (m *Manager) RuntimeStateValue(ctx context.Context, locator SessionLocator, key string) (any, bool, error) {
	ts, err := m.sessionForRuntimeState(ctx, locator)
	if err != nil || ts == nil {
		return nil, false, err
	}
	return ts.RuntimeStateValue(ctx, key)
}

func (m *Manager) UpdateRuntimeState(ctx context.Context, locator SessionLocator, state map[string]any) error {
	ts, err := m.sessionForRuntimeState(ctx, locator)
	if err != nil || ts == nil {
		return err
	}
	return ts.UpdateRuntimeState(ctx, state)
}

func (m *Manager) sessionForRuntimeState(ctx context.Context, locator SessionLocator) (*TopicSession, error) {
	ts, err := m.GetSession(locator)
	if err == nil && ts != nil {
		return ts, nil
	}
	return m.RestoreSession(ctx, SessionContext{Locator: locator})
}

type TopicSessionInfo struct {
	SessionID    string
	UserID       string
	Locator      SessionLocator
	Kind         Kind
	OwnerJobID   string
	ChannelType  string
	AgentName    string
	WorkspaceDir string
	BranchName   string
	Status       string
	ArchiveID    string
	Generation   uint64
	Model        string
	Outcome      string
	ProviderID   string
	Recovery     string
}

func (m *Manager) GetSessionInfo(ctx context.Context, sessionID string) (TopicSessionInfo, error) {
	trimmedID := strings.TrimSpace(sessionID)
	if trimmedID == "" {
		return TopicSessionInfo{}, fmt.Errorf("session_id is required")
	}

	record, ok, err := m.sessionStore.GetBySessionID(ctx, trimmedID)
	if err != nil {
		return TopicSessionInfo{}, fmt.Errorf("read session metadata: %w", err)
	}
	if !ok {
		return TopicSessionInfo{}, fmt.Errorf("session %q not found", trimmedID)
	}

	locator, err := LocatorFromRecord(record)
	if err != nil {
		return TopicSessionInfo{}, fmt.Errorf("decode persisted session locator for %q: %w", record.SessionID, err)
	}
	info := TopicSessionInfo{
		SessionID:    record.SessionID,
		UserID:       record.UserID,
		Locator:      locator,
		ChannelType:  locator.ChannelType,
		AgentName:    record.AgentName,
		WorkspaceDir: record.WorkspaceDir,
		BranchName:   record.BranchName,
		ArchiveID:    record.ArchiveID,
		Generation:   record.RuntimeGeneration,
		Model:        record.EffectiveModel,
		Outcome:      record.RuntimeOutcome,
		Recovery:     runtimeRecoveryReady,
	}
	if bindingJSON := strings.TrimSpace(record.ProviderBindingJSON); bindingJSON != "" {
		canonical, canonicalErr := providers.CanonicalBindingJSON(bindingJSON)
		if canonicalErr != nil || canonical != bindingJSON {
			return TopicSessionInfo{}, ErrRuntimeRecoveryRequired
		}
		var binding providers.Binding
		if unmarshalErr := json.Unmarshal([]byte(canonical), &binding); unmarshalErr != nil {
			return TopicSessionInfo{}, ErrRuntimeRecoveryRequired
		}
		info.ProviderID = binding.RootProviderID
	}
	if strings.TrimSpace(record.Status) == "" || record.Status == baldastate.SessionStatusActive {
		info.Status = sessionStatusPersisted
	} else {
		info.Status = record.Status
	}
	if m.runtimeOperations != nil {
		operation, found, operationErr := m.runtimeOperations.GetUnresolvedBySession(ctx, trimmedID)
		if operationErr != nil {
			info.Status = sessionStatusRecoveryRequired
			info.Recovery = runtimeRecoveryAmbiguous
		} else if found {
			info.Status = sessionStatusRecoveryRequired
			switch operation.State {
			case baldastate.RuntimeOperationPrepared:
				info.Recovery = runtimeRecoveryPrepared
			case baldastate.RuntimeOperationApplying:
				info.Recovery = runtimeRecoveryAmbiguous
			default:
				info.Recovery = runtimeRecoveryAmbiguous
			}
		}
	}
	if info.Outcome == string(baldastate.RuntimeOperationOutcomeCleanupIncomplete) {
		info.Status = sessionStatusRecoveryRequired
		info.Recovery = runtimeRecoveryCleanup
	}
	return info, nil
}

func (m *Manager) getProviderName() string {
	if m.runtimeManager != nil {
		if providerID := strings.TrimSpace(m.runtimeManager.ProviderID()); providerID != "" {
			return providerID
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.baldaProviderName
}

// ActiveRuntimeStateValue reads only the currently loaded runtime. Final replay
// must never invoke lazy restoration, fallback, hooks or provider creation.
func (m *Manager) ActiveRuntimeStateValue(ctx context.Context, locator SessionLocator, key string) (any, bool, error) {
	unlock := m.lockSessionOperation(locator.SessionID)
	defer unlock()
	m.mu.RLock()
	ts := m.sessions[locator.SessionID]
	m.mu.RUnlock()
	if ts == nil {
		return nil, false, nil
	}
	return ts.RuntimeStateValue(ctx, key)
}

// ActiveRuntimeStateValueForArchive reads only when the loaded runtime still
// belongs to the expected archive. The existing session-operation lock keeps
// the archive proof and leased state read in one critical section.
func (m *Manager) ActiveRuntimeStateValueForArchive(
	ctx context.Context,
	locator SessionLocator,
	archiveID string,
	key string,
) (value any, found bool, matched bool, err error) {
	unlock := m.lockSessionOperation(locator.SessionID)
	defer unlock()
	m.mu.RLock()
	ts := m.sessions[locator.SessionID]
	m.mu.RUnlock()
	if ts == nil || ts.GetArchiveID() != archiveID {
		return nil, false, false, nil
	}
	value, found, err = ts.RuntimeStateValue(ctx, key)
	return value, found, true, err
}

// UpdateRuntimeStateForArchive fences late bookkeeping against replacement.
// The same operation lock protects epoch verification and the runtime write.
func (m *Manager) UpdateRuntimeStateForArchive(ctx context.Context, locator SessionLocator, epoch string, values map[string]any) (bool, error) {
	unlock := m.lockSessionOperation(locator.SessionID)
	defer unlock()
	m.mu.RLock()
	ts := m.sessions[locator.SessionID]
	m.mu.RUnlock()
	if ts == nil || ts.GetArchiveID() != epoch {
		return false, nil
	}
	if err := ts.UpdateRuntimeState(ctx, values); err != nil {
		return false, err
	}
	return true, nil
}

// archiveReplayRestoreKey restricts ordinary final-publication recovery to the
// existing lifetime. It must never take the destructive fresh fallback path.
type archiveReplayRestoreKey struct{}

// ResumeArchiveRuntime loads only the matching durable lifetime for ordinary
// final publication/bookkeeping replay. Archive readers never call this method.
func (m *Manager) ResumeArchiveRuntime(ctx context.Context, locator SessionLocator, epoch string) (bool, error) {
	endLifecycle, err := m.beginLifecycleOperation(ctx)
	if err != nil {
		return false, err
	}
	defer endLifecycle()
	unlock := m.lockSessionOperation(locator.SessionID)
	defer unlock()
	m.mu.RLock()
	active := m.sessions[locator.SessionID]
	m.mu.RUnlock()
	if active != nil {
		return active.GetArchiveID() == epoch, nil
	}
	if !m.sessionsPersistent || m.archive == nil || m.sessionStore == nil {
		return false, nil
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, locator.SessionID)
	if err != nil {
		return false, err
	}
	if !found || record.ArchiveID != epoch || record.Status != baldastate.SessionStatusActive {
		return false, nil
	}
	archived, err := m.archive.Get(ctx, epoch)
	if err != nil {
		return false, err
	}
	if archived.Status != chatarchivecmd.StatusActive || archived.Persistence != chatarchivecmd.PersistenceDurable {
		return false, nil
	}
	pending, err := m.archive.Pending(ctx, locator.ChannelType, locator.AddressKey)
	if err != nil && !errors.Is(err, chatarchivecmd.ErrNotFound) {
		return false, err
	}
	if err == nil && pending.Kind == chatarchivecmd.KindDestroy {
		return false, chatarchivecmd.ErrConflict
	}
	restoreCtx := context.WithValue(ctx, archiveReplayRestoreKey{}, true)
	restored, err := m.prepareSessionLocked(restoreCtx, SessionContext{Locator: locator, UserID: record.UserID}, record.AgentName, false, record, true)
	if err != nil {
		return false, err
	}
	return restored != nil && restored.GetArchiveID() == epoch, nil
}
