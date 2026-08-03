package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/git"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

const cleanupTimeout = 10 * time.Second

const sessionStatusPersisted = "persisted"

const baldaRuntimeAppName = "norma-balda"

const workspaceSyncSkippedNotice = "Workspace restore hit a sync conflict, so Balda reset the workspace to the last saved session-branch state without applying the latest base changes. Ask Balda to retry the sync later."

var ErrNoPersistedSession = errors.New("no persisted session")

type AgentBuilder interface {
	CreateRuntimeSession(
		ctx context.Context,
		runtime *BuiltRuntime,
		agentName string,
		userID string,
		sessionID string,
		workspaceDir string,
		sessionCtx RuntimeSessionContext,
	) (adksession.Session, error)
	GetAgentMetadata(agentName string) AgentMetadata
}

type RuntimeManager interface {
	Runtime(ctx context.Context) (*BuiltRuntime, error)
	ProviderID() string
}

type WorkspaceManager interface {
	CanonicalWorkspaceDir(key string) string
	ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (EnsureWorkspaceResult, error)
	EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (EnsureWorkspaceResult, error)
	Import(ctx context.Context, workspaceDir string) error
	Export(ctx context.Context, workspaceDir, branchName, commitMessage string) error
	CleanupWorkspace(ctx context.Context, workspaceDir string) error
}

type AgentMetadata struct {
	Type       string
	Model      string
	MCPServers []string
}

type RuntimeSessionContext struct {
	BaldaSessionID string
	SessionBranch  string
}

type BuiltRuntime struct {
	Agent      agent.Agent
	Runner     *runner.Runner
	SessionSvc adksession.Service
	AppName    string
}

type EnsureWorkspaceResult struct {
	Dir         string
	SyncSkipped bool
}

// Manager manages Balda sessions backed by the configured runtime and persists session metadata.
type Manager struct {
	agentBuilder       AgentBuilder
	runtimeManager     RuntimeManager
	baldaMCPServerIDs  []string
	baldaProviderName  string
	workingDir         string
	workspaces         WorkspaceManager
	workspaceEnabled   bool
	workspaceBaseRef   string
	sessionsPersistent bool
	sessionStore       baldastate.SessionStore
	logger             zerolog.Logger
	boundaryObserver   BoundaryObserver
	now                func() time.Time

	mu                 sync.RWMutex
	sessions           map[string]*TopicSession
	agentSessionSeq    uint64
	boundarySeq        uint64
	shutdownBoundaries map[string]string
}

// ManagerParams provides dependencies for Manager.
type ManagerParams struct {
	fx.In

	AgentBuilder       AgentBuilder
	RuntimeManager     RuntimeManager
	BaldaMCPServerIDs  []string `name:"balda_mcp_servers"`
	BaldaProviderID    string   `name:"balda_provider"`
	WorkingDir         string
	WorkspaceEnabled   bool   `name:"balda_workspace_enabled"`
	WorkspaceBaseRef   string `name:"balda_workspace_base_branch"`
	Workspaces         WorkspaceManager
	SessionsPersistent bool `name:"balda_sessions_persistent"`
	SessionStore       baldastate.SessionStore
	BoundaryObserver   BoundaryObserver `optional:"true"`
	Logger             zerolog.Logger
}

// NewManager creates a session Manager.
func NewManager(p ManagerParams) (*Manager, error) {
	if p.SessionStore == nil {
		return nil, fmt.Errorf("session store is required")
	}

	m := &Manager{
		agentBuilder:       p.AgentBuilder,
		runtimeManager:     p.RuntimeManager,
		baldaMCPServerIDs:  append([]string(nil), p.BaldaMCPServerIDs...),
		baldaProviderName:  strings.TrimSpace(p.BaldaProviderID),
		workingDir:         p.WorkingDir,
		workspaces:         p.Workspaces,
		workspaceEnabled:   p.WorkspaceEnabled,
		workspaceBaseRef:   p.WorkspaceBaseRef,
		sessionsPersistent: p.SessionsPersistent,
		sessionStore:       p.SessionStore,
		boundaryObserver:   p.BoundaryObserver,
		logger:             p.Logger.With().Str("component", "balda.session_manager").Logger(),
		sessions:           make(map[string]*TopicSession),
		now:                time.Now,
		shutdownBoundaries: make(map[string]string),
	}

	return m, nil
}

// Start marks the session manager ready after its provider runtime is initialized.
func (m *Manager) Start(context.Context) error {
	m.logger.Info().Str("balda_provider", m.getProviderName()).Msg("session manager ready")
	return nil
}

// SetBoundaryObserver installs the optional lifecycle adapter at the
// composition root without changing existing session-manager callers.
func (m *Manager) SetBoundaryObserver(observer BoundaryObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.boundaryObserver = observer
	m.mu.Unlock()
}

// Stop terminates all active sessions.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.RLock()
	activeSessions := len(m.sessions)
	m.mu.RUnlock()
	m.logger.Info().Int("active_sessions", activeSessions).Msg("session manager stopping")
	m.stopAllWithContext(ctx)
	return nil
}

// PublishShutdownBoundaries hands active session identities to the optional
// boundary observer while the durable transport is still alive. It does not
// remove or clean up sessions; Manager.Stop performs that later.
func (m *Manager) PublishShutdownBoundaries(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	sessions := make([]*TopicSession, 0, len(m.sessions))
	for _, ts := range m.sessions {
		sessions = append(sessions, ts)
	}
	m.mu.RUnlock()
	var errs []error
	for _, ts := range sessions {
		if ts == nil {
			continue
		}
		if err := m.notifyBoundary(ctx, SessionBoundary{
			Locator:        ts.locator,
			SessionID:      ts.sessionID,
			AgentSessionID: ts.GetAgentSessionID(),
			Reason:         BoundaryReasonShutdown,
		}); err != nil {
			errs = append(errs, fmt.Errorf("publish shutdown boundary for %s: %w", ts.sessionID, err))
		}
	}
	return errors.Join(errs...)
}

// NotifySessionBoundary exposes the rotation seam for composition-root
// session replacement code. Reset/close/shutdown use the same observer path.
func (m *Manager) NotifySessionBoundary(ctx context.Context, boundary SessionBoundary) error {
	if m == nil {
		return nil
	}
	return m.notifyBoundaryRequest(ctx, boundary)
}

// GetAgentMetadata returns balda-provider metadata with provider-scoped MCP IDs.
func (m *Manager) GetAgentMetadata(agentName string) AgentMetadata {
	m.mu.RLock()
	builder := m.agentBuilder
	baldaMCPServerIDs := append([]string(nil), m.baldaMCPServerIDs...)
	m.mu.RUnlock()
	if builder == nil {
		return AgentMetadata{}
	}
	meta := builder.GetAgentMetadata(agentName)
	if len(baldaMCPServerIDs) == 0 {
		return meta
	}
	out := make([]string, 0, len(meta.MCPServers)+len(baldaMCPServerIDs))
	seen := make(map[string]struct{}, len(meta.MCPServers)+len(baldaMCPServerIDs))
	appendUnique := func(raw string) {
		id := strings.TrimSpace(raw)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range meta.MCPServers {
		appendUnique(id)
	}
	for _, id := range baldaMCPServerIDs {
		appendUnique(id)
	}
	meta.MCPServers = out
	return meta
}

// BaldaProviderID returns the configured balda provider ID.
func (m *Manager) BaldaProviderID() string {
	return m.getProviderName()
}

// CreateSession builds an agent for the given locator and stores it in memory.
func (m *Manager) CreateSession(ctx context.Context, sessionCtx SessionContext, agentName string) error {
	return m.createSession(ctx, sessionCtx, agentName, nil)
}

func (m *Manager) createSession(ctx context.Context, sessionCtx SessionContext, agentName string, persisted *baldastate.SessionRecord) error {
	locator := sessionCtx.Locator
	userID := strings.TrimSpace(sessionCtx.UserID)
	if userID == "" {
		return fmt.Errorf("user id is required")
	}

	sessionID := strings.TrimSpace(locator.SessionID)
	m.mu.RLock()
	builder := m.agentBuilder
	m.mu.RUnlock()
	if builder == nil {
		return fmt.Errorf("agent builder is required")
	}

	m.logger.Info().
		Str("user_id", userID).
		Str("agent", agentName).
		Str("session_id", sessionID).
		Str("channel_type", locator.ChannelType).
		Msg("creating session")

	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		m.logger.Warn().Str("session_id", sessionID).Msg("session already exists")
		return fmt.Errorf("session already exists for %s", locator.AddressKey)
	}
	m.mu.Unlock()

	branchName := ""
	workspaceDir := m.workingDir
	startupNotice := ""
	if m.workspaceEnabled {
		branchName = fmt.Sprintf("norma/balda/%s", sessionID)
		canonicalPath := m.workspaces.CanonicalWorkspaceDir(sessionID)
		if persisted != nil {
			if persistedBranch := strings.TrimSpace(persisted.BranchName); persistedBranch != "" {
				branchName = persistedBranch
				if !git.BranchExists(ctx, m.workingDir, branchName) {
					return fmt.Errorf("persisted workspace branch %q not found", branchName)
				}
			}
		}

		workspace, err := m.workspaces.EnsureWorkspace(ctx, sessionID, branchName, canonicalPath)
		if err != nil {
			if errors.Is(err, ErrWorkspaceCollision) {
				m.logger.Warn().
					Err(err).
					Str("session_id", sessionID).
					Str("canonical_workspace", canonicalPath).
					Str("branch", branchName).
					Msg("workspace collision detected; force-remounting canonical workspace path")
				workspace, err = m.workspaces.ForceRemountCanonicalWorkspace(ctx, sessionID, branchName)
			}
			if err != nil {
				m.logger.Error().Err(err).Str("session_id", sessionID).Msg("failed to create workspace")
				return fmt.Errorf("create workspace: %w", err)
			}
		}
		workspaceDir = workspace.Dir
		if workspace.SyncSkipped {
			startupNotice = workspaceSyncSkippedNotice
		}
		m.logger.Debug().Str("session_id", sessionID).Str("workspace", workspaceDir).Msg("workspace created")
	}

	runtimeManager := m.runtimeManager
	if runtimeManager == nil {
		if m.workspaceEnabled {
			_ = m.workspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return fmt.Errorf("balda runtime manager is required")
	}

	rootRuntime, err := runtimeManager.Runtime(ctx)
	if err != nil {
		if m.workspaceEnabled {
			_ = m.workspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return err
	}
	baldaProvider := strings.TrimSpace(runtimeManager.ProviderID())
	if baldaProvider == "" {
		baldaProvider = m.getProviderName()
	}

	agentSessionID := m.newAgentSessionID(sessionID)
	if m.sessionsPersistent {
		agentSessionID = sessionID
	}
	sess, err := builder.CreateRuntimeSession(
		ctx,
		rootRuntime,
		baldaProvider,
		userID,
		agentSessionID,
		workspaceDir,
		RuntimeSessionContext{
			BaldaSessionID: sessionID,
			SessionBranch:  branchName,
		},
	)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("session_id", sessionID).
			Str("agent_session_id", agentSessionID).
			Str("agent", baldaProvider).
			Str("label", agentName).
			Msg("failed to create runtime session")
		if m.workspaceEnabled {
			_ = m.workspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return err
	}

	ts := &TopicSession{
		sessionID:      sessionID,
		agentSessionID: agentSessionID,
		userID:         userID,
		locator:        locator,
		agentName:      agentName,
		agent:          rootRuntime.Agent,
		runner:         rootRuntime.Runner,
		sessionSvc:     rootRuntime.SessionSvc,
		sess:           sess,
		workspaceDir:   workspaceDir,
		branchName:     branchName,
		startupNotice:  startupNotice,
	}

	if err := m.persistSessionRecord(ctx, ts, baldastate.SessionStatusActive); err != nil {
		if closeErr := m.cleanupTopicSession(ctx, ts, sessionCleanupOptions{deleteRuntimeSession: true, cleanupWorkspace: true}); closeErr != nil {
			m.logger.Warn().Err(closeErr).Str("session_id", sessionID).Msg("failed to rollback session after persist error")
		}
		return fmt.Errorf("persist session metadata: %w", err)
	}

	m.mu.Lock()
	m.sessions[sessionID] = ts
	m.mu.Unlock()

	m.logger.Info().
		Str("user_id", userID).
		Str("agent", agentName).
		Str("session_id", sessionID).
		Str("channel_type", locator.ChannelType).
		Msg("session created successfully")

	return nil
}

// TakeStartupNotice returns and clears the pending session startup notice.
func (m *Manager) TakeStartupNotice(sessionID string) string {
	trimmedID := strings.TrimSpace(sessionID)
	if trimmedID == "" {
		return ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	ts := m.sessions[trimmedID]
	if ts == nil {
		return ""
	}

	notice := strings.TrimSpace(ts.startupNotice)
	ts.startupNotice = ""
	return notice
}

// StopSession removes a session from memory and cleans up.
func (m *Manager) StopSession(locator SessionLocator) {
	sessionID := strings.TrimSpace(locator.SessionID)
	if m.sessionsPersistent {
		m.logger.Info().
			Str("session_id", sessionID).
			Str("channel_type", locator.ChannelType).
			Str("address_key", locator.AddressKey).
			Msg("suspending persistent session")
		if _, err := m.removeActiveSession(context.Background(), locator, sessionCleanupOptions{cleanupWorkspace: true}, BoundaryReasonClose); err != nil {
			m.logger.Warn().Err(err).Str("session_id", sessionID).Msg("persistent session close cleanup failed")
		}
		return
	}
	m.hardDeleteSession(locator)
}

// ResetSession deletes the conversation history for the current session while
// preserving balda metadata so the same chat can start fresh.
func (m *Manager) ResetSession(ctx context.Context, locator SessionLocator) error {
	return m.ResetSessionWithReason(ctx, locator, BoundaryReasonReset)
}

// ResetSessionWithReason resets one session while preserving its metadata and
// classifies the lifecycle transition for boundary adapters.
func (m *Manager) ResetSessionWithReason(ctx context.Context, locator SessionLocator, reason BoundaryReason) error {
	if !validBoundaryReason(reason) {
		return fmt.Errorf("unsupported session boundary reason %q", reason)
	}
	return m.resetSessionWithReason(ctx, locator, reason)
}

func (m *Manager) resetSessionWithReason(ctx context.Context, locator SessionLocator, reason BoundaryReason) error {
	sessionID := strings.TrimSpace(locator.SessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	m.logger.Info().
		Str("session_id", sessionID).
		Str("channel_type", locator.ChannelType).
		Str("address_key", locator.AddressKey).
		Str("boundary_reason", string(reason)).
		Msg("resetting session")

	if removed, boundaryErr := m.removeActiveSession(ctx, locator, sessionCleanupOptions{deleteRuntimeSession: true}, reason); removed {
		return boundaryErr
	}

	record, ok, err := m.sessionStore.GetBySessionID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read session metadata: %w", err)
	}
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	recordLocator, err := LocatorFromRecord(record)
	if err != nil {
		return fmt.Errorf("decode persisted session locator: %w", err)
	}
	boundaryErr := m.notifyBoundary(ctx, SessionBoundary{
		Locator:        recordLocator,
		SessionID:      record.SessionID,
		AgentSessionID: record.SessionID,
		Reason:         reason,
	})
	if !m.sessionsPersistent {
		return boundaryErr
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
	userID := strings.TrimSpace(record.UserID)
	if userID == "" {
		return fmt.Errorf("persisted session %q has no user_id", sessionID)
	}
	appName := strings.TrimSpace(rootRuntime.AppName)
	if appName == "" {
		appName = baldaRuntimeAppName
	}
	if err := rootRuntime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	}); err != nil {
		return errors.Join(boundaryErr, fmt.Errorf("delete runtime session: %w", err))
	}
	return boundaryErr
}

func (m *Manager) hardDeleteSession(locator SessionLocator) {
	sessionID := strings.TrimSpace(locator.SessionID)

	m.logger.Info().
		Str("session_id", sessionID).
		Str("channel_type", locator.ChannelType).
		Str("address_key", locator.AddressKey).
		Msg("stopping session")

	removed, boundaryErr := m.removeActiveSession(context.Background(), locator, sessionCleanupOptions{deleteRuntimeSession: true, cleanupWorkspace: true}, BoundaryReasonClose)
	if !removed {
		m.logger.Warn().Str("session_id", sessionID).Msg("session not found for stop")
	}
	if boundaryErr != nil {
		m.logger.Warn().Err(boundaryErr).Str("session_id", sessionID).Msg("session close boundary failed")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := m.sessionStore.DeleteBySessionID(cleanupCtx, sessionID); err != nil {
		m.logger.Warn().Err(err).Str("session_id", sessionID).Msg("failed to delete persisted session metadata")
	}

	m.logger.Info().Str("session_id", sessionID).Msg("session stopped")
}

func (m *Manager) stopAllWithContext(ctx context.Context) {
	m.mu.Lock()
	sessions := make([]*TopicSession, 0, len(m.sessions))
	for _, ts := range m.sessions {
		sessions = append(sessions, ts)
	}
	m.sessions = make(map[string]*TopicSession)
	m.mu.Unlock()

	m.logger.Info().Int("count", len(sessions)).Msg("stopping all sessions")

	opts := sessionCleanupOptions{deleteRuntimeSession: !m.sessionsPersistent, cleanupWorkspace: true}
	for _, ts := range sessions {
		if ts == nil {
			continue
		}
		if err := m.notifyBoundary(ctx, SessionBoundary{
			Locator:        ts.locator,
			SessionID:      ts.sessionID,
			AgentSessionID: ts.GetAgentSessionID(),
			Reason:         BoundaryReasonShutdown,
		}); err != nil {
			m.logger.Warn().Err(err).Str("session_id", ts.sessionID).Msg("session shutdown boundary failed")
		}
		if err := m.cleanupTopicSession(ctx, ts, opts); err != nil {
			m.logger.Warn().Err(err).Str("session_id", ts.sessionID).Msg("failed to close topic session")
		}
	}

	m.logger.Info().Msg("all sessions stopped")
}

type sessionCleanupOptions struct {
	deleteRuntimeSession bool
	cleanupWorkspace     bool
}

func (m *Manager) removeActiveSession(ctx context.Context, locator SessionLocator, opts sessionCleanupOptions, reason BoundaryReason) (bool, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	m.mu.Lock()
	ts, exists := m.sessions[sessionID]
	if exists {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !exists {
		return false, nil
	}
	boundaryErr := m.notifyBoundary(ctx, SessionBoundary{
		Locator:        ts.locator,
		SessionID:      ts.sessionID,
		AgentSessionID: ts.GetAgentSessionID(),
		Reason:         reason,
	})
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := m.cleanupTopicSession(cleanupCtx, ts, opts); err != nil {
		m.logger.Warn().Err(err).Str("session_id", sessionID).Msg("failed to cleanup topic session")
		return true, errors.Join(boundaryErr, err)
	}
	return true, boundaryErr
}

func validBoundaryReason(reason BoundaryReason) bool {
	switch reason {
	case BoundaryReasonReset, BoundaryReasonClose, BoundaryReasonRotation, BoundaryReasonShutdown:
		return true
	default:
		return false
	}
}

func (m *Manager) notifyBoundary(ctx context.Context, boundary SessionBoundary) error {
	err := m.notifyBoundaryRequest(ctx, boundary)
	if err != nil {
		m.logger.Warn().
			Err(err).
			Str("session_id", strings.TrimSpace(boundary.SessionID)).
			Str("boundary_reason", string(boundary.Reason)).
			Msg("session boundary observer failed")
	}
	return err
}

func (m *Manager) notifyBoundaryRequest(ctx context.Context, boundary SessionBoundary) error {
	if m == nil {
		return nil
	}
	if !validBoundaryReason(boundary.Reason) {
		return fmt.Errorf("unsupported session boundary reason %q", boundary.Reason)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(boundary.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(boundary.Locator.SessionID)
	}
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	boundary.SessionID = sessionID
	boundary.Locator.SessionID = sessionID
	if strings.TrimSpace(boundary.AgentSessionID) == "" {
		boundary.AgentSessionID = sessionID
	}
	if boundary.OccurredAt.IsZero() {
		boundary.OccurredAt = m.currentTime()
	}
	if strings.TrimSpace(boundary.TransitionID) == "" {
		boundary.TransitionID = m.newBoundaryID(boundary.Reason, sessionID, boundary.OccurredAt)
	}

	m.mu.RLock()
	observer := m.boundaryObserver
	m.mu.RUnlock()
	if observer == nil {
		return nil
	}

	shutdownKey := ""
	if boundary.Reason == BoundaryReasonShutdown {
		shutdownKey = sessionID + "|" + boundary.Locator.ChannelType + "|" + boundary.Locator.AddressKey
		m.mu.Lock()
		if m.shutdownBoundaries == nil {
			m.shutdownBoundaries = make(map[string]string)
		}
		if _, published := m.shutdownBoundaries[shutdownKey]; published {
			m.mu.Unlock()
			return nil
		}
		m.shutdownBoundaries[shutdownKey] = boundary.TransitionID
		m.mu.Unlock()
	}

	if err := observer.BeforeSessionBoundary(ctx, boundary); err != nil {
		if shutdownKey != "" {
			m.mu.Lock()
			if m.shutdownBoundaries[shutdownKey] == boundary.TransitionID {
				delete(m.shutdownBoundaries, shutdownKey)
			}
			m.mu.Unlock()
		}
		return fmt.Errorf("observe %s boundary: %w", boundary.Reason, err)
	}
	return nil
}

func (m *Manager) currentTime() time.Time {
	m.mu.RLock()
	now := m.now
	m.mu.RUnlock()
	if now == nil {
		return time.Now()
	}
	return now()
}

func (m *Manager) newBoundaryID(reason BoundaryReason, sessionID string, occurredAt time.Time) string {
	seq := atomic.AddUint64(&m.boundarySeq, 1)
	return fmt.Sprintf("%s:%s:%d:%d", reason, strings.TrimSpace(sessionID), occurredAt.UnixNano(), seq)
}

func (m *Manager) cleanupTopicSession(ctx context.Context, ts *TopicSession, opts sessionCleanupOptions) error {
	var firstErr error
	if opts.deleteRuntimeSession && ts != nil && ts.sessionSvc != nil {
		sessionID := strings.TrimSpace(ts.GetAgentSessionID())
		userID := strings.TrimSpace(ts.userID)
		appName := baldaRuntimeAppName
		if ts.sess != nil {
			if sessionAppName := strings.TrimSpace(ts.sess.AppName()); sessionAppName != "" {
				appName = sessionAppName
			}
			if sessionUserID := strings.TrimSpace(ts.sess.UserID()); sessionUserID != "" {
				userID = sessionUserID
			}
		}
		if sessionID != "" && userID != "" {
			if err := ts.sessionSvc.Delete(ctx, &adksession.DeleteRequest{
				AppName:   appName,
				UserID:    userID,
				SessionID: sessionID,
			}); err != nil {
				firstErr = fmt.Errorf("delete runtime session: %w", err)
			}
		}
	}
	if opts.cleanupWorkspace && ts != nil && m.workspaceEnabled && ts.workspaceDir != "" {
		if err := m.workspaces.CleanupWorkspace(ctx, ts.workspaceDir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) persistSessionRecord(ctx context.Context, ts *TopicSession, status string) error {
	if ts == nil {
		return fmt.Errorf("topic session is required")
	}
	if strings.TrimSpace(status) == "" {
		status = baldastate.SessionStatusActive
	}

	return m.sessionStore.Upsert(ctx, baldastate.SessionRecord{
		SessionID:    ts.sessionID,
		UserID:       ts.userID,
		ChannelType:  ts.locator.ChannelType,
		AddressKey:   ts.locator.AddressKey,
		AddressJSON:  ts.locator.AddressJSON,
		AgentName:    ts.agentName,
		WorkspaceDir: ts.workspaceDir,
		BranchName:   ts.branchName,
		Status:       status,
	})
}

func (m *Manager) newAgentSessionID(sessionID string) string {
	seq := atomic.AddUint64(&m.agentSessionSeq, 1)
	return fmt.Sprintf("%s-a%d", strings.TrimSpace(sessionID), seq)
}
