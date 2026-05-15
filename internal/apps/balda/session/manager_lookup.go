package session

import (
	"context"
	"fmt"
	"strings"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/session"
)

// GetSession returns the in-memory session for the given locator.
func (m *Manager) GetSession(locator SessionLocator) (*TopicSession, error) {
	sessionID := strings.TrimSpace(locator.SessionID)

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

	return ts, nil
}

// HasSession reports whether a session exists in memory or persisted metadata.
// It does not create or restore sessions.
func (m *Manager) HasSession(ctx context.Context, locator SessionLocator) (bool, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	if sessionID == "" {
		return false, nil
	}

	m.mu.RLock()
	_, active := m.sessions[sessionID]
	m.mu.RUnlock()
	if active {
		return true, nil
	}

	if m.sessionStore == nil {
		return false, nil
	}

	record, ok, err := m.sessionStore.GetByAddress(ctx, locator.ChannelType, locator.AddressKey)
	if err != nil {
		return false, fmt.Errorf("read session metadata: %w", err)
	}
	if !ok {
		return false, nil
	}

	if status := strings.TrimSpace(record.Status); status != "" && status != baldastate.SessionStatusActive {
		return false, nil
	}
	return true, nil
}

// EnsureSession returns the existing session or creates a new one if it doesn't exist.
func (m *Manager) EnsureSession(ctx context.Context, sessionCtx SessionContext, agentName string) (*TopicSession, error) {
	sessionID := strings.TrimSpace(sessionCtx.Locator.SessionID)

	m.mu.RLock()
	ts := m.sessions[sessionID]
	m.mu.RUnlock()

	if ts != nil {
		m.logger.Debug().Str("session_id", sessionID).Msg("returning existing session")
		return ts, nil
	}

	if err := m.CreateSession(ctx, sessionCtx, agentName); err != nil {
		return nil, err
	}
	return m.GetSession(sessionCtx.Locator)
}

// RestoreSession restores a session from persisted metadata when it is not active in memory.
func (m *Manager) RestoreSession(ctx context.Context, sessionCtx SessionContext) (*TopicSession, error) {
	locator := sessionCtx.Locator
	sessionID := strings.TrimSpace(locator.SessionID)

	m.mu.RLock()
	if ts := m.sessions[sessionID]; ts != nil {
		m.mu.RUnlock()
		return ts, nil
	}
	m.mu.RUnlock()

	record, ok, err := m.sessionStore.GetByAddress(ctx, locator.ChannelType, locator.AddressKey)
	if err != nil {
		return nil, fmt.Errorf("read session metadata: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("%w for %s", ErrNoPersistedSession, locator.AddressKey)
	}
	if strings.TrimSpace(record.Status) != "" && record.Status != baldastate.SessionStatusActive {
		return nil, fmt.Errorf("persisted session for %s is not active", locator.AddressKey)
	}

	recordLocator, err := LocatorFromRecord(record)
	if err != nil {
		return nil, fmt.Errorf("decode persisted session locator: %w", err)
	}
	sessionLabel := strings.TrimSpace(record.AgentName)
	if sessionLabel == "" {
		sessionLabel = "auto"
	}

	m.logger.Info().
		Str("session_id", sessionID).
		Str("channel_type", recordLocator.ChannelType).
		Str("address_key", recordLocator.AddressKey).
		Str("label", sessionLabel).
		Msg("restoring session from persisted metadata")

	restoredCtx := SessionContext{
		Locator: recordLocator,
		UserID:  restoredSessionUserID(record, sessionCtx.UserID),
	}
	if err := m.createSession(ctx, restoredCtx, sessionLabel, &record); err != nil {
		return nil, err
	}
	return m.GetSession(recordLocator)
}

func restoredSessionUserID(record baldastate.SessionRecord, fallback string) string {
	if userID := strings.TrimSpace(record.UserID); userID != "" {
		return userID
	}
	return fallback
}

// ListSessions returns info about all active sessions.
func (m *Manager) ListSessions() []TopicSessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]TopicSessionInfo, 0, len(m.sessions))
	for _, ts := range m.sessions {
		out = append(out, topicSessionInfo(ts, baldastate.SessionStatusActive))
	}
	return out
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
		return topicSessionInfo(ts, baldastate.SessionStatusActive), nil
	}

	record, ok, err := m.sessionStore.GetBySessionID(ctx, trimmedID)
	if err != nil {
		return TopicSessionInfo{}, fmt.Errorf("read session metadata: %w", err)
	}
	if !ok {
		return TopicSessionInfo{}, fmt.Errorf("session %q not found", trimmedID)
	}

	info, err := topicSessionInfoFromRecord(record)
	if err != nil {
		return TopicSessionInfo{}, err
	}
	info.Status = sessionStatusForInactiveRecord(record.Status)
	return info, nil
}

func (m *Manager) ListSessionInfos(ctx context.Context) ([]TopicSessionInfo, error) {
	persisted, err := m.sessionStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session metadata: %w", err)
	}

	infos := make(map[string]TopicSessionInfo, len(persisted))
	for _, record := range persisted {
		info, err := topicSessionInfoFromRecord(record)
		if err != nil {
			return nil, err
		}
		info.Status = sessionStatusForInactiveRecord(record.Status)
		infos[info.SessionID] = info
	}

	for _, info := range m.ListSessions() {
		info.Status = baldastate.SessionStatusActive
		infos[info.SessionID] = info
	}

	out := make([]TopicSessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, info)
	}
	return out, nil
}

func (m *Manager) StopSessionByID(ctx context.Context, sessionID string) error {
	info, err := m.GetSessionInfo(ctx, sessionID)
	if err != nil {
		return err
	}
	if info.Status == baldastate.SessionStatusActive {
		m.hardDeleteSession(info.Locator)
		return nil
	}

	if m.workspaceEnabled && strings.TrimSpace(info.WorkspaceDir) != "" {
		if err := m.workspaces.CleanupWorkspace(ctx, info.WorkspaceDir); err != nil {
			return fmt.Errorf("cleanup workspace: %w", err)
		}
	}
	if m.sessionsPersistent {
		if err := m.deletePersistedADKSession(ctx, info); err != nil {
			return err
		}
	}
	if err := m.sessionStore.DeleteBySessionID(ctx, info.SessionID); err != nil {
		return fmt.Errorf("delete session metadata: %w", err)
	}
	return nil
}

func (m *Manager) deletePersistedADKSession(ctx context.Context, info TopicSessionInfo) error {
	if strings.TrimSpace(info.UserID) == "" {
		return nil
	}
	runtimeManager := m.runtimeManager
	if runtimeManager == nil {
		return fmt.Errorf("balda runtime manager is required")
	}
	rootRuntime, err := runtimeManager.Runtime(ctx)
	if err != nil {
		return err
	}
	if rootRuntime == nil || rootRuntime.SessionSvc == nil {
		return fmt.Errorf("session service is required")
	}
	appName := strings.TrimSpace(rootRuntime.AppName)
	if appName == "" {
		appName = baldaADKAppName
	}
	if err := rootRuntime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{
		AppName:   appName,
		UserID:    strings.TrimSpace(info.UserID),
		SessionID: strings.TrimSpace(info.SessionID),
	}); err != nil {
		return fmt.Errorf("delete adk session: %w", err)
	}
	return nil
}

func topicSessionInfo(ts *TopicSession, status string) TopicSessionInfo {
	if ts == nil {
		return TopicSessionInfo{}
	}
	return TopicSessionInfo{
		SessionID:    ts.sessionID,
		UserID:       ts.userID,
		Locator:      ts.locator,
		ChannelType:  ts.locator.ChannelType,
		AgentName:    ts.agentName,
		WorkspaceDir: ts.workspaceDir,
		BranchName:   ts.branchName,
		Status:       status,
	}
}

func topicSessionInfoFromRecord(record baldastate.SessionRecord) (TopicSessionInfo, error) {
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
	return info, nil
}

func sessionStatusForInactiveRecord(recordStatus string) string {
	if strings.TrimSpace(recordStatus) == "" || recordStatus == baldastate.SessionStatusActive {
		return sessionStatusPersisted
	}
	return recordStatus
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
