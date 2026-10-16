package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
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

	if m.archive != nil && ts.archiveID != "" {
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
	if existing, err := m.GetSession(sessionCtx.Locator); err == nil {
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
			label = "auto"
		}
		persisted = &record
	}
	if err := m.reconcileArchiveDestruction(ctx, sessionCtx.Locator, sessionCtx.UserID); err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(sessionCtx.Locator.SessionID)
	m.mu.RLock()
	active := m.sessions[sessionID]
	m.mu.RUnlock()
	if active != nil {
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
	ChannelType  string
	AgentName    string
	WorkspaceDir string
	BranchName   string
	Status       string
}

func (m *Manager) GetSessionInfo(ctx context.Context, sessionID string) (TopicSessionInfo, error) {
	trimmedID := strings.TrimSpace(sessionID)
	if trimmedID == "" {
		return TopicSessionInfo{}, fmt.Errorf("session_id is required")
	}

	m.mu.RLock()
	ts := m.sessions[trimmedID]
	m.mu.RUnlock()
	if ts != nil {
		return TopicSessionInfo{
			SessionID:    ts.sessionID,
			UserID:       ts.userID,
			Locator:      ts.locator,
			ChannelType:  ts.locator.ChannelType,
			AgentName:    ts.agentName,
			WorkspaceDir: ts.workspaceDir,
			BranchName:   ts.branchName,
			Status:       baldastate.SessionStatusActive,
		}, nil
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
	}
	if strings.TrimSpace(record.Status) == "" || record.Status == baldastate.SessionStatusActive {
		info.Status = sessionStatusPersisted
	} else {
		info.Status = record.Status
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
