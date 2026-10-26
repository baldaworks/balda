package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/git"
	"github.com/baldaworks/runtime/v2/agentproduct"
	"github.com/google/uuid"
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

var errManagerStopping = errors.New("session manager is stopping")

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
	OpenExistingRuntimeSession(
		ctx context.Context,
		runtime *BuiltRuntime,
		agentName string,
		userID string,
		sessionID string,
		workspaceDir string,
		sessionCtx RuntimeSessionContext,
	) (adksession.Session, error)
	CreateFreshRuntimeSession(
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

type providerAvailabilityChecker interface {
	EnsureProviderAvailable(ctx context.Context) error
}

// SessionRuntimeManager is an optional composition-root extension. It gives
// one session a provider runtime whose internal MCP capability is bound to
// that session, while preserving RuntimeManager compatibility for tests and
// deployments without scoped MCP.
type SessionRuntimeManager interface {
	RuntimeForSession(ctx context.Context, request SessionRuntimeRequest) (*BuiltRuntime, error)
}

type SessionRuntimeRequest struct {
	Locator        SessionLocator
	UserID         string
	AgentSessionID string
	LineageID      string
	WorkspaceDir   string
	// RuntimeSnapshotID requests an exact persisted capability snapshot.
	// An empty value lets the runtime select the current effective snapshot.
	RuntimeSnapshotID   string
	ProviderBindingJSON string
}

type WorkspaceManager interface {
	CanonicalWorkspaceDir(key string) string
	ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (EnsureWorkspaceResult, error)
	EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (EnsureWorkspaceResult, error)
	Import(ctx context.Context, workspaceDir string) error
	Export(ctx context.Context, workspaceDir, branchName, commitMessage string) error
	CleanupWorkspace(ctx context.Context, workspaceDir string) error
	DeleteBranch(ctx context.Context, branchName string) error
}

type AgentMetadata struct {
	Type            string
	Model           string
	ReasoningEffort string
	MCPServers      []string
}

type RuntimeSessionContext struct {
	ArchiveID      string
	BaldaSessionID string
	SessionBranch  string
}

type BuiltRuntime struct {
	Agent          agent.Agent
	SessionControl agentproduct.SessionControl
	Runner         *runner.Runner
	SessionSvc     adksession.Service
	AppName        string
	// RuntimeSnapshotID identifies the capabilities bound to this runtime.
	RuntimeSnapshotID   string
	ProviderBindingJSON string
	Close               func() error
}

type EnsureWorkspaceResult struct {
	Dir         string
	SyncSkipped bool
	Created     bool
}

// Manager manages Balda sessions backed by the configured runtime and persists session metadata.
type Manager struct {
	archive            ArchiveLifecycle
	bootID             string
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
	runtimeOperations  baldastate.RuntimeOperationStore
	logger             zerolog.Logger
	hooks              HookRunner
	boundaryObserver   BoundaryObserver
	kindResolver       KindResolver
	now                func() time.Time

	mu                 sync.RWMutex
	lifecycleMu        sync.RWMutex
	sessions           map[string]*TopicSession
	agentSessionSeq    uint64
	boundarySeq        uint64
	stopping           atomic.Bool
	shutdownBoundaries map[string]string
	operationMu        sync.Mutex
	operations         map[string]*sessionOperation
}

type sessionOperation struct {
	mu   sync.Mutex
	refs int
}

// ManagerParams provides dependencies for Manager.
type ManagerParams struct {
	fx.In

	Archive            ArchiveLifecycle
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
	RuntimeOperations  baldastate.RuntimeOperationStore `optional:"true"`
	BoundaryObserver   BoundaryObserver                 `optional:"true"`
	KindResolver       KindResolver                     `optional:"true"`
	Hooks              HookRunner                       `optional:"true"`
	Logger             zerolog.Logger
}

// NewManager creates a session Manager.
func NewManager(p ManagerParams) (*Manager, error) {
	if p.SessionStore == nil {
		return nil, fmt.Errorf("session store is required")
	}

	m := &Manager{
		archive: p.Archive, bootID: uuid.NewString(),
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
		runtimeOperations:  p.RuntimeOperations,
		boundaryObserver:   p.BoundaryObserver,
		kindResolver:       p.KindResolver,
		hooks:              p.Hooks,
		logger:             p.Logger.With().Str("component", "balda.session_manager").Logger(),
		sessions:           make(map[string]*TopicSession),
		now:                time.Now,
		shutdownBoundaries: make(map[string]string),
	}

	if m.archive != nil {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := m.archive.ReconcileMemory(ctx, m.bootID); err != nil {
			return nil, fmt.Errorf("reconcile former memory lifetimes: %w", err)
		}
	}
	return m, nil
}

// Start marks the session manager ready after its provider runtime is initialized.
func (m *Manager) Start(context.Context) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.stopping.Store(false)
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
	m.stopping.Store(true)
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
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
		value, ok := ts.runtimeValue()
		if !ok {
			continue
		}
		if err := m.notifyBoundary(ctx, SessionBoundary{
			Locator:        ts.locator,
			SessionID:      ts.sessionID,
			AgentSessionID: value.sessionID,
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
	return m.createSession(ctx, sessionCtx, agentName, nil, false)
}

func (m *Manager) createSession(ctx context.Context, sessionCtx SessionContext, agentName string,
	persisted *baldastate.SessionRecord, transient bool,
) error {
	sessionID := strings.TrimSpace(sessionCtx.Locator.SessionID)
	endLifecycle, err := m.beginLifecycleOperation(ctx)
	if err != nil {
		return err
	}
	defer endLifecycle()
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
	return m.createSessionLocked(ctx, sessionCtx, agentName, persisted, transient)
}

func (m *Manager) createSessionLocked(ctx context.Context, sessionCtx SessionContext, agentName string,
	persisted *baldastate.SessionRecord, transient bool,
) error {
	if err := m.lifecycleInterruption(ctx); err != nil {
		return err
	}
	var err error
	sessionCtx, err = m.resolveExecutionContext(sessionCtx)
	if err != nil {
		return err
	}
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

	m.mu.RLock()
	_, exists := m.sessions[sessionID]
	m.mu.RUnlock()
	if exists {
		m.logger.Warn().Str("session_id", sessionID).Msg("session already exists")
		return fmt.Errorf("session already exists for %s", locator.AddressKey)
	}

	runtimeManager := m.runtimeManager
	if runtimeManager == nil {
		return fmt.Errorf("balda runtime manager is required")
	}
	if checker, ok := runtimeManager.(providerAvailabilityChecker); ok {
		if err := checker.EnsureProviderAvailable(ctx); err != nil {
			return err
		}
	}
	rootRuntime, err := runtimeManager.Runtime(ctx)
	if err != nil {
		return err
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		return err
	}

	branchName, workspaceDir, startupNotice, workspaceCreated, err := m.prepareSessionWorkspace(ctx, sessionID, persisted)
	if err != nil {
		return err
	}
	cleanupWorkspace := m.workspaceEnabled && persisted == nil && workspaceCreated
	if err := m.lifecycleInterruption(ctx); err != nil {
		if cleanupWorkspace {
			_ = m.workspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return err
	}
	cleanupPreparedWorkspace := func() {
		if cleanupWorkspace {
			_ = m.workspaces.CleanupWorkspace(ctx, workspaceDir)
		}
	}
	baldaProvider := strings.TrimSpace(runtimeManager.ProviderID())
	if baldaProvider == "" {
		baldaProvider = m.getProviderName()
	}

	agentSessionID := m.newAgentSessionID(sessionID)
	if m.sessionsPersistent || transient || (persisted != nil && m.archive == nil) {
		agentSessionID = sessionID
	}
	if !transient {
		if err := m.reconcileArchiveDestruction(ctx, locator, userID); err != nil {
			cleanupPreparedWorkspace()
			return err
		}
	}
	// Memory runtime metadata can survive in SQL, but its provider history did
	// not survive the previous process. Adoption would invent an exact runtime.
	if m.archive != nil && !m.sessionsPersistent && persisted != nil && persisted.ArchiveID == "" {
		persisted = nil
	}
	var activation *chatarchivecmd.Intent
	var interrupted bool
	if !transient {
		activation, interrupted, err = m.beginArchiveActivation(ctx, locator, agentSessionID, agentName, persisted)
	}
	if err != nil {
		cleanupPreparedWorkspace()
		return fmt.Errorf("reserve archive activation: %w", err)
	}
	if interrupted {
		recovered, err := m.recoverArchiveActivation(ctx, rootRuntime, builder, activation, userID, baldaProvider)
		if err != nil {
			return err
		}
		recovered.kind = sessionCtx.Kind
		recovered.ownerJobID = strings.TrimSpace(sessionCtx.OwnerJobID)
		m.mu.Lock()
		m.sessions[sessionID] = recovered
		m.mu.Unlock()
		return nil
	}
	runtimeSessionCtx := RuntimeSessionContext{
		BaldaSessionID: sessionID,
		SessionBranch:  branchName,
	}

	if activation != nil && activation.Kind == chatarchivecmd.KindFresh {
		persisted = nil
	}
	if activation != nil && activation.Kind != chatarchivecmd.KindAdopt {
		runtimeSessionCtx.ArchiveID = activation.Session.ID
	}
	var sessionRuntime *BuiltRuntime
	var sess adksession.Session
	createdFresh := false
	if persisted == nil {
		sessionRuntime, err = m.bindSessionRuntime(ctx, rootRuntime, locator, userID, agentSessionID, workspaceDir, "", "")
		if err == nil {
			if transient {
				sess, err = builder.CreateRuntimeSession(
					ctx, sessionRuntime, baldaProvider, userID, agentSessionID, workspaceDir, runtimeSessionCtx,
				)
			} else {
				sess, err = builder.CreateFreshRuntimeSession(
					ctx, sessionRuntime, baldaProvider, userID, agentSessionID, workspaceDir, runtimeSessionCtx,
				)
			}
			createdFresh = err == nil
		}
	} else {
		sessionRuntime, sess, createdFresh, err = m.restoreRuntimeSession(
			ctx, builder, rootRuntime, locator, userID, baldaProvider, workspaceDir, runtimeSessionCtx, *persisted, &activation,
		)
	}
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("session_id", sessionID).
			Str("agent_session_id", agentSessionID).
			Str("agent", baldaProvider).
			Str("label", agentName).
			Msg("failed to open runtime session")
		closeBuiltRuntime(sessionRuntime)
		cleanupPreparedWorkspace()
		if activation != nil {
			m.failArchiveActivation(ctx, rootRuntime, activation, userID)
		}
		return err
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		closeBuiltRuntime(sessionRuntime)
		cleanupPreparedWorkspace()
		return err
	}

	ts := &TopicSession{
		sessionID:     sessionID,
		userID:        userID,
		locator:       locator,
		kind:          sessionCtx.Kind,
		ownerJobID:    strings.TrimSpace(sessionCtx.OwnerJobID),
		agentName:     agentName,
		workspaceDir:  workspaceDir,
		branchName:    branchName,
		startupNotice: startupNotice,
	}
	archiveID := ""
	if activation != nil {
		archiveID = activation.Session.ID
	}
	var generation uint64
	var effectiveModel, runtimeOutcome string
	if persisted != nil && !createdFresh {
		generation = persisted.RuntimeGeneration
		effectiveModel = persisted.EffectiveModel
		runtimeOutcome = persisted.RuntimeOutcome
	} else {
		effectiveModel = builder.GetAgentMetadata(baldaProvider).Model
	}
	ts.runtime = newPublishedRuntimeSlot(runtimeSlotConfig{
		runtime:        sessionRuntime,
		session:        sess,
		userID:         userID,
		sessionID:      agentSessionID,
		archiveID:      archiveID,
		generation:     generation,
		effectiveModel: strings.TrimSpace(effectiveModel),
		outcome:        strings.TrimSpace(runtimeOutcome),
	})
	source := "startup"
	if persisted != nil && !createdFresh {
		source = "resume"
	}
	if err := m.startSessionHooks(ctx, ts, baldaProvider, source); err != nil {
		cleanupErr := m.cleanupTopicSession(ctx, ts, sessionCleanupOptions{deleteRuntimeSession: createdFresh, cleanupWorkspace: cleanupWorkspace})
		if activation != nil {
			outcome := chatarchivecmd.OutcomeAborted
			if cleanupErr != nil || !createdFresh {
				outcome = chatarchivecmd.StatusUncertain
			}
			_ = m.archive.Complete(ctx, activation.Token, outcome)
		}
		return err
	}

	if activation != nil {
		if err := m.markArchiveReady(ctx, ts, activation); err != nil {
			return errors.Join(err, ts.DrainRuntime(ctx))
		}
		live := m.sessionRecord(ts, baldastate.SessionStatusActive)
		if _, err := m.archive.Activate(ctx, activation.Token, &live); err != nil {
			return errors.Join(fmt.Errorf("commit archive activation: %w", err), ts.DrainRuntime(ctx))
		}
	}
	shouldPersist := activation == nil && !transient && (persisted == nil || createdFresh)
	value, hasRuntime := ts.runtimeValue()
	if !hasRuntime {
		return ErrRuntimeUnavailable
	}
	if activation == nil && !transient && persisted != nil && strings.TrimSpace(persisted.ProviderBindingJSON) == "" && strings.TrimSpace(value.runtime.ProviderBindingJSON) != "" {
		shouldPersist = true
	}
	if shouldPersist {
		if err := m.sessionStore.Upsert(ctx, m.sessionRecordFromValue(ts, baldastate.SessionStatusActive, value)); err != nil {
			if closeErr := m.cleanupTopicSession(ctx, ts, sessionCleanupOptions{
				deleteRuntimeSession: createdFresh,
				cleanupWorkspace:     cleanupWorkspace,
			}); closeErr != nil {
				m.logger.Warn().Err(closeErr).Str("session_id", sessionID).Msg("failed to rollback session after persist error")
			}
			return fmt.Errorf("persist session metadata: %w", err)
		}
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

func (m *Manager) resolveExecutionContext(sessionCtx SessionContext) (SessionContext, error) {
	if sessionCtx.Kind == "" {
		if m.kindResolver != nil {
			kind, err := m.kindResolver.ResolveKind(sessionCtx.Locator)
			if err != nil {
				return SessionContext{}, fmt.Errorf("resolve session kind: %w", err)
			}
			sessionCtx.Kind = kind
		} else {
			// Direct Manager construction is retained for package-level tests and
			// compatibility embedders. Production composition always supplies the
			// transport resolver; the shared-chat fallback keeps legacy records
			// executable without inspecting transport-specific locator encodings.
			sessionCtx.Kind = KindChat
		}
	}
	if err := validateExecutionContext(sessionCtx.Kind, sessionCtx.OwnerJobID); err != nil {
		return SessionContext{}, fmt.Errorf("validate session execution context: %w", err)
	}
	sessionCtx.OwnerJobID = strings.TrimSpace(sessionCtx.OwnerJobID)
	return sessionCtx, nil
}

func (m *Manager) prepareSessionWorkspace(
	ctx context.Context,
	sessionID string,
	persisted *baldastate.SessionRecord,
) (branchName, workspaceDir, startupNotice string, created bool, err error) {
	workspaceDir = m.workingDir
	if !m.workspaceEnabled {
		return "", workspaceDir, "", false, nil
	}

	branchName = fmt.Sprintf("norma/balda/%s", sessionID)
	if persisted != nil {
		if persistedBranch := strings.TrimSpace(persisted.BranchName); persistedBranch != "" {
			branchName = persistedBranch
			if !git.BranchExists(ctx, m.workingDir, branchName) {
				return "", "", "", false, fmt.Errorf("persisted workspace branch %q not found", branchName)
			}
		}
	}

	canonicalPath := m.workspaces.CanonicalWorkspaceDir(sessionID)
	workspace, err := m.workspaces.EnsureWorkspace(ctx, sessionID, branchName, canonicalPath)
	if err != nil && errors.Is(err, ErrWorkspaceCollision) {
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
		return "", "", "", false, fmt.Errorf("create workspace: %w", err)
	}
	if workspace.SyncSkipped {
		startupNotice = workspaceSyncSkippedNotice
	}
	m.logger.Debug().Str("session_id", sessionID).Str("workspace", workspace.Dir).Msg("workspace created")
	return branchName, workspace.Dir, startupNotice, workspace.Created, nil
}

func (m *Manager) bindSessionRuntime(
	ctx context.Context,
	rootRuntime *BuiltRuntime,
	locator SessionLocator,
	userID, agentSessionID, workspaceDir, snapshotID, providerBindingJSON string,
) (*BuiltRuntime, error) {
	if rootRuntime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	runtimeManager := m.runtimeManager
	scopedManager, ok := runtimeManager.(SessionRuntimeManager)
	if !ok {
		if strings.TrimSpace(providerBindingJSON) != "" {
			return nil, fmt.Errorf("persisted provider binding requires a scoped runtime manager")
		}
		nonOwningRuntime := *rootRuntime
		nonOwningRuntime.Close = nil
		return &nonOwningRuntime, nil
	}

	sessionRuntime, err := scopedManager.RuntimeForSession(ctx, SessionRuntimeRequest{
		Locator:             locator,
		UserID:              userID,
		AgentSessionID:      agentSessionID,
		WorkspaceDir:        workspaceDir,
		RuntimeSnapshotID:   strings.TrimSpace(snapshotID),
		ProviderBindingJSON: strings.TrimSpace(providerBindingJSON),
	})
	if err != nil {
		return nil, err
	}
	if sessionRuntime == nil {
		return nil, fmt.Errorf("session runtime is required")
	}
	resolvedSnapshotID := strings.TrimSpace(sessionRuntime.RuntimeSnapshotID)
	if resolvedSnapshotID == "" {
		closeBuiltRuntime(sessionRuntime)
		return nil, fmt.Errorf("session runtime snapshot is required")
	}
	if requested := strings.TrimSpace(snapshotID); requested != "" && resolvedSnapshotID != requested {
		closeBuiltRuntime(sessionRuntime)
		return nil, fmt.Errorf("session runtime snapshot %q does not match persisted snapshot %q", resolvedSnapshotID, requested)
	}
	if requested := strings.TrimSpace(providerBindingJSON); requested != "" && strings.TrimSpace(sessionRuntime.ProviderBindingJSON) != requested {
		closeBuiltRuntime(sessionRuntime)
		return nil, fmt.Errorf("session provider binding does not match persisted binding")
	}
	return sessionRuntime, nil
}

func (m *Manager) restoreRuntimeSession(
	ctx context.Context,
	builder AgentBuilder,
	rootRuntime *BuiltRuntime,
	locator SessionLocator,
	userID, provider, workspaceDir string,
	sessionCtx RuntimeSessionContext,
	record baldastate.SessionRecord,
	activation **chatarchivecmd.Intent,
) (*BuiltRuntime, adksession.Session, bool, error) {
	sessionID := strings.TrimSpace(record.SessionID)
	exactRuntime, restoreErr := m.bindSessionRuntime(
		ctx, rootRuntime, locator, userID, sessionID, workspaceDir, record.RuntimeSnapshotID,
		record.ProviderBindingJSON,
	)
	if restoreErr == nil {
		var sess adksession.Session
		sess, restoreErr = builder.OpenExistingRuntimeSession(
			ctx, exactRuntime, provider, userID, sessionID, workspaceDir, sessionCtx,
		)
		if restoreErr == nil {
			return exactRuntime, sess, false, nil
		}
		closeBuiltRuntime(exactRuntime)
	}
	if replay, _ := ctx.Value(archiveReplayRestoreKey{}).(bool); replay {
		return nil, nil, false, fmt.Errorf("restore committed-final lifetime: %w", restoreErr)
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		return nil, nil, false, err
	}

	m.logger.Warn().
		Err(restoreErr).
		Str("session_id", sessionID).
		Str("runtime_snapshot_id", strings.TrimSpace(record.RuntimeSnapshotID)).
		Bool("provider_binding_present", strings.TrimSpace(record.ProviderBindingJSON) != "").
		Msg("saved session restore failed; starting fresh with current capabilities")

	currentRuntime, err := m.bindSessionRuntime(ctx, rootRuntime, locator, userID, sessionID, workspaceDir, "", "")
	if err != nil {
		return nil, nil, false, fmt.Errorf("bind current runtime after restore failure: %w", err)
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, err
	}
	if err := m.notifyBoundary(ctx, SessionBoundary{
		Locator:        locator,
		SessionID:      sessionID,
		AgentSessionID: sessionID,
		Reason:         BoundaryReasonRotation,
	}); err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, err
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, err
	}
	appName := strings.TrimSpace(currentRuntime.AppName)
	if appName == "" {
		appName = baldaRuntimeAppName
	}
	if currentRuntime.SessionSvc == nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, fmt.Errorf("session service is required")
	}
	var destruction *chatarchivecmd.Intent
	if *activation != nil && (*activation).Kind == chatarchivecmd.KindExact {
		if err := m.archive.Complete(ctx, (*activation).Token, chatarchivecmd.OutcomeAborted); err != nil {
			closeBuiltRuntime(currentRuntime)
			return nil, nil, false, err
		}
		destruction, err = m.beginArchiveDestruction(ctx, record.ArchiveID, BoundaryReasonRotation)
		if err != nil {
			closeBuiltRuntime(currentRuntime)
			return nil, nil, false, err
		}
	}
	deleteErr := currentRuntime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{AppName: appName, UserID: userID, SessionID: sessionID})
	if _, err := m.completeArchiveDeletion(ctx, destruction, currentRuntime.SessionSvc, appName, userID, deleteErr); err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, fmt.Errorf("delete failed runtime session: %w", err)
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, err
	}
	if *activation != nil {
		if (*activation).Kind == chatarchivecmd.KindAdopt {
			if err := m.archive.Complete(ctx, (*activation).Token, chatarchivecmd.OutcomeAborted); err != nil {
				closeBuiltRuntime(currentRuntime)
				return nil, nil, false, err
			}
		}
		prior := record
		if prior.ArchiveID == "" {
			prior.ArchiveID = (*activation).Session.ID
		}
		next, _, err := m.beginArchiveActivation(ctx, locator, sessionID, record.AgentName, &prior)
		if err != nil {
			closeBuiltRuntime(currentRuntime)
			return nil, nil, false, err
		}
		*activation = next
		sessionCtx.ArchiveID = next.Session.ID
	}

	sess, err := builder.CreateFreshRuntimeSession(
		ctx, currentRuntime, provider, userID, sessionID, workspaceDir, sessionCtx,
	)
	if err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, fmt.Errorf("create fresh runtime session: %w", err)
	}
	if err := m.lifecycleInterruption(ctx); err != nil {
		closeBuiltRuntime(currentRuntime)
		return nil, nil, false, err
	}
	return currentRuntime, sess, true, nil
}

func (m *Manager) beginLifecycleOperation(ctx context.Context) (func(), error) {
	m.lifecycleMu.RLock()
	if err := m.lifecycleInterruption(ctx); err != nil {
		m.lifecycleMu.RUnlock()
		return nil, err
	}
	return m.lifecycleMu.RUnlock, nil
}

func (m *Manager) lifecycleInterruption(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.stopping.Load() {
		return errManagerStopping
	}
	return nil
}

func closeBuiltRuntime(runtime *BuiltRuntime) {
	if runtime != nil && runtime.Close != nil {
		_ = runtime.Close()
		runtime.Close = nil
	}
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
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
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
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()

	m.logger.Info().
		Str("session_id", sessionID).
		Str("channel_type", locator.ChannelType).
		Str("address_key", locator.AddressKey).
		Str("boundary_reason", string(reason)).
		Msg("resetting session")

	if removed, boundaryErr := m.resetActiveSession(ctx, locator, reason); removed {
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
	if err := m.reconcileArchiveDestruction(ctx, recordLocator, record.UserID); err != nil {
		return err
	}
	if m.archive != nil && record.ArchiveID != "" {
		old, err := m.archive.Get(ctx, record.ArchiveID)
		if err != nil {
			return err
		}
		if old.Status == chatarchivecmd.StatusEnded {
			return m.lifecycleInterruption(ctx)
		}
	}
	destruction, err := m.beginArchiveDestruction(ctx, record.ArchiveID, reason)
	if err != nil {
		return err
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
	deleteErr := rootRuntime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{AppName: appName, UserID: userID, SessionID: sessionID})
	_, journalErr := m.completeArchiveDeletion(ctx, destruction, rootRuntime.SessionSvc, appName, userID, deleteErr)
	return errors.Join(boundaryErr, journalErr, m.lifecycleInterruption(ctx))
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
	if boundaryErr != nil {
		return
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
		unlock := m.lockSessionOperation(ts.sessionID)
		value, ok := ts.runtimeValue()
		if !ok {
			unlock()
			continue
		}
		if !ts.runtimeDeleted && !ts.runtimeClosed {
			if err := m.notifyBoundary(ctx, SessionBoundary{
				Locator:        ts.locator,
				SessionID:      ts.sessionID,
				AgentSessionID: value.sessionID,
				Reason:         BoundaryReasonShutdown,
			}); err != nil {
				m.logger.Warn().Err(err).Str("session_id", ts.sessionID).Msg("session shutdown boundary failed")
			}
		}
		cleanupOpts := opts
		if opts.deleteRuntimeSession {
			archiveID := value.archiveID
			intent, err := m.beginArchiveDestruction(ctx, archiveID, BoundaryReasonShutdown)
			if err != nil {
				cleanupOpts.deleteRuntimeSession = false
				m.logger.Warn().Err(err).Str("archive_id", archiveID).Msg("runtime retained because shutdown journal is unavailable")
			} else {
				deleteErr := deleteTopicRuntimeSessionValue(ctx, ts, value)
				destroyed, journalErr := m.completeArchiveDeletion(ctx, intent, value.runtime.SessionSvc, runtimeAppName(value), value.userID, deleteErr)
				ts.runtimeDeleted = destroyed
				cleanupOpts.deleteRuntimeSession = false
				if journalErr != nil {
					m.logger.Warn().Err(journalErr).Str("archive_id", archiveID).Msg("shutdown destruction remains pending")
				}
			}
		}
		if err := m.cleanupTopicSession(ctx, ts, cleanupOpts); err != nil {
			m.logger.Warn().Err(err).Str("session_id", ts.sessionID).Msg("failed to close topic session")
		}
		unlock()
	}

	m.logger.Info().Msg("all sessions stopped")
}

type sessionCleanupOptions struct {
	deleteRuntimeSession bool
	cleanupWorkspace     bool
}

func (m *Manager) resetActiveSession(ctx context.Context, locator SessionLocator, reason BoundaryReason) (bool, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	m.mu.RLock()
	ts := m.sessions[sessionID]
	m.mu.RUnlock()
	if ts == nil {
		return false, nil
	}
	if ts.pendingBoundaries == nil {
		ts.pendingBoundaries = make(map[BoundaryReason]boundaryAttempt)
	}
	attempt, ok := ts.pendingBoundaries[reason]
	value, hasRuntime := ts.runtimeValue()
	if !hasRuntime {
		return true, ErrRuntimeUnavailable
	}
	if !ok {
		occurredAt := m.currentTime()
		attempt.boundary = SessionBoundary{
			Locator:        ts.locator,
			SessionID:      ts.sessionID,
			AgentSessionID: value.sessionID,
			Reason:         reason,
			OccurredAt:     occurredAt,
			TransitionID:   m.newBoundaryID(reason, ts.sessionID, occurredAt),
		}
		ts.pendingBoundaries[reason] = attempt
	}
	destruction, err := m.beginArchiveDestruction(ctx, value.archiveID, reason)
	if err != nil {
		return true, err
	}
	var boundaryErr error
	if !attempt.published {
		boundaryErr = m.notifyBoundary(ctx, attempt.boundary)
		if boundaryErr == nil {
			attempt.published = true
			ts.pendingBoundaries[reason] = attempt
		}
	}
	deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	// Keep the active runtime available if storage cannot delete its history.
	deleteErr := deleteTopicRuntimeSessionValue(deleteCtx, ts, value)
	cancelDelete()
	destroyed, journalErr := m.completeArchiveDeletion(ctx, destruction, value.runtime.SessionSvc, runtimeAppName(value), value.userID, deleteErr)
	ts.runtimeDeleted = destroyed
	if !destroyed {
		return true, errors.Join(boundaryErr, journalErr, m.lifecycleInterruption(ctx))
	}
	m.mu.Lock()
	if m.sessions[sessionID] == ts {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancelCleanup()
	cleanupErr := m.cleanupTopicSession(cleanupCtx, ts, sessionCleanupOptions{})
	return true, errors.Join(boundaryErr, journalErr, cleanupErr, m.lifecycleInterruption(ctx))
}

func (m *Manager) lockSessionOperation(sessionID string) func() {
	m.operationMu.Lock()
	if m.operations == nil {
		m.operations = make(map[string]*sessionOperation)
	}
	op := m.operations[sessionID]
	if op == nil {
		op = &sessionOperation{}
		m.operations[sessionID] = op
	}
	op.refs++
	m.operationMu.Unlock()
	op.mu.Lock()
	return func() {
		op.mu.Unlock()
		m.operationMu.Lock()
		op.refs--
		if op.refs == 0 {
			delete(m.operations, sessionID)
		}
		m.operationMu.Unlock()
	}
}

func (m *Manager) removeActiveSession(ctx context.Context, locator SessionLocator, opts sessionCleanupOptions, reason BoundaryReason) (bool, error) {
	if opts.deleteRuntimeSession {
		m.mu.RLock()
		ts := m.sessions[strings.TrimSpace(locator.SessionID)]
		m.mu.RUnlock()
		removed, err := m.resetActiveSession(ctx, locator, reason)
		if err != nil || !removed {
			return removed, err
		}
		opts.deleteRuntimeSession = false
		return true, m.cleanupTopicSession(ctx, ts, opts)
	}
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
	value, ok := ts.runtimeValue()
	if !ok {
		return true, ErrRuntimeUnavailable
	}
	boundaryErr := m.notifyBoundary(ctx, SessionBoundary{
		Locator:        ts.locator,
		SessionID:      ts.sessionID,
		AgentSessionID: value.sessionID,
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
	m.endSessionHooks(ctx, ts)
	var firstErr error
	if opts.deleteRuntimeSession {
		firstErr = deleteTopicRuntimeSession(ctx, ts)
	}
	if opts.cleanupWorkspace && ts != nil && m.workspaceEnabled && ts.workspaceDir != "" {
		if err := m.workspaces.CleanupWorkspace(ctx, ts.workspaceDir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if ts != nil && !ts.runtimeClosed {
		if err := ts.DrainRuntime(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return firstErr
		}
		ts.runtimeClosed = true
	}
	return firstErr
}

func deleteTopicRuntimeSession(ctx context.Context, ts *TopicSession) error {
	value, ok := ts.runtimeValue()
	if !ok {
		return nil
	}
	return deleteTopicRuntimeSessionValue(ctx, ts, value)
}

func deleteTopicRuntimeSessionValue(ctx context.Context, ts *TopicSession, value runtimeSlotValue) error {
	if value.runtime.SessionSvc == nil || ts.runtimeDeleted {
		return nil
	}
	sessionID := strings.TrimSpace(value.sessionID)
	userID := strings.TrimSpace(value.userID)
	appName := baldaRuntimeAppName
	if sessionAppName := strings.TrimSpace(value.appName); sessionAppName != "" {
		appName = sessionAppName
	}
	if sessionID == "" || userID == "" {
		return nil
	}
	if err := value.runtime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("delete runtime session: %w", err)
	}
	ts.runtimeDeleted = true
	return nil
}

func (m *Manager) persistSessionRecord(ctx context.Context, ts *TopicSession, status string) error {
	if ts == nil {
		return fmt.Errorf("topic session is required")
	}
	if strings.TrimSpace(status) == "" {
		status = baldastate.SessionStatusActive
	}

	return m.sessionStore.Upsert(ctx, m.sessionRecord(ts, status))
}

func (m *Manager) sessionRecord(ts *TopicSession, status string) baldastate.SessionRecord {
	value, _ := ts.runtimeValue()
	return m.sessionRecordFromValue(ts, status, value)
}

func (m *Manager) sessionRecordFromValue(ts *TopicSession, status string, value runtimeSlotValue) baldastate.SessionRecord {
	return baldastate.SessionRecord{
		ArchiveID:           value.archiveID,
		SessionID:           ts.sessionID,
		UserID:              value.userID,
		ChannelType:         ts.locator.ChannelType,
		AddressKey:          ts.locator.AddressKey,
		AddressJSON:         ts.locator.AddressJSON,
		AgentName:           ts.agentName,
		WorkspaceDir:        ts.workspaceDir,
		BranchName:          ts.branchName,
		RuntimeSnapshotID:   value.runtime.RuntimeSnapshotID,
		ProviderBindingJSON: value.runtime.ProviderBindingJSON,
		RuntimeGeneration:   value.generation,
		EffectiveModel:      value.effectiveModel,
		RuntimeOutcome:      value.outcome,
		Status:              status,
	}
}

func (m *Manager) newAgentSessionID(sessionID string) string {
	seq := atomic.AddUint64(&m.agentSessionSeq, 1)
	return fmt.Sprintf("%s-a%d", strings.TrimSpace(sessionID), seq)
}
