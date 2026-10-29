package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/agentcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/shutdown"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// ErrProviderUnavailable identifies provider-dependent work that cannot run
// with the current provider selection. Callers should present a bounded error
// and leave the base application running.
var ErrProviderUnavailable = agentcmd.ErrProviderUnavailable

// SessionRuntimeRequest identifies the authenticated session that owns a
// scoped provider runtime and its internal MCP capability.
type SessionRuntimeRequest struct {
	Locator        deliverycmd.Locator
	UserID         string
	AgentSessionID string
	LineageID      string
	WorkspaceDir   string
	// RuntimeSnapshotID requests an exact persisted capability snapshot.
	RuntimeSnapshotID string
	// ProviderBindingJSON requests the exact persisted provider graph.
	// An empty value resolves the current committed graph.
	ProviderBindingJSON string
	// ModelOverride optionally replaces only the exact recipe model used to
	// construct this Product.
	ModelOverride *string
	// ExpectedRecipeModel is used only to adopt an already-promoted recovery
	// candidate without rebuilding or contacting a provider.
	ExpectedRecipeModel string
	// RecoveryFence allows this exact candidate to adopt a previously installed
	// cold-recovery child-lookup fence.
	RecoveryFence bool
	// ProviderConfigs carries the already bound graph to capability projection.
	ProviderConfigs map[string]agentconfig.Config
}

// SessionCapabilityBinding is one immutable catalog projection owned by a session runtime.
type SessionCapabilityBinding struct {
	SnapshotID           runtimecatalogcmd.SnapshotID
	Skills               SkillMetadataProjection
	MCPServerIDs         []string
	ProviderMCPServerIDs map[string][]string
	Close                func() error
}

var (
	// ErrSessionBindingCleanup is the bounded classification for failure to
	// release a session-scoped MCP binding.
	ErrSessionBindingCleanup = errors.New("session binding cleanup failed")
	// ErrSessionCapabilityCleanup is the bounded classification for failure to
	// release a session capability snapshot.
	ErrSessionCapabilityCleanup = errors.New("session capability cleanup failed")
)

// SessionCapabilityBinder selects commands, skills, and MCP from one catalog snapshot.
type SessionCapabilityBinder interface {
	BindSessionCapabilities(ctx context.Context, providerID string, request SessionRuntimeRequest) (SessionCapabilityBinding, error)
}

// ScopedMCPServer is an authenticated per-session MCP endpoint registration.
type ScopedMCPServer struct {
	ID      string
	Config  agentconfig.MCPServerConfig
	Release func() error
}

// SessionMCPBinder creates a capability that injects trusted session headers
// before an internal MCP request reaches bundled tools.
type SessionMCPBinder interface {
	BindSession(ctx context.Context, request SessionRuntimeRequest) (ScopedMCPServer, error)
}

type sessionMCPBinderStatus interface {
	SessionMCPEnabled(capabilities SessionCapabilityBinding) bool
}

// RuntimeManager owns the app-scoped runtime and optional per-session runtimes.
type RuntimeManager struct {
	builder           *Builder
	providerID        string
	workingDir        string
	workspaceEnabled  bool
	baldaMCPServerIDs []string
	goalWorkspaces    *WorkspaceManager
	sessionMCPBinder  SessionMCPBinder
	capabilityBinder  SessionCapabilityBinder
	providerService   *providers.Service
	mcpRegistry       *mcpregistry.MapRegistry
	logger            zerolog.Logger

	mu                    sync.RWMutex
	runtime               *BuiltRuntime
	runtimeBuilder        *Builder
	runtimeMCPServerIDs   []string
	scopedRuntimes        map[string]*BuiltRuntime
	sessionBases          map[string]childRuntimeBase
	knownSessionBases     map[string]struct{}
	baseTransitions       map[string]RuntimeBaseToken
	baseCandidates        map[RuntimeBaseToken]*UnpublishedSessionRuntimeCandidate
	replacementCandidates map[string]*BuiltRuntime
	scopedSequence        uint64
	baseSequence          uint64
}

// RuntimeManagerParams wires RuntimeManager dependencies.
type RuntimeManagerParams struct {
	fx.In

	Builder           *Builder
	BaldaProviderID   string `name:"balda_provider"`
	WorkingDir        string
	StateDir          string                  `name:"balda_state_dir"`
	WorkspaceEnabled  bool                    `name:"balda_workspace_enabled"`
	WorkspaceBaseRef  string                  `name:"balda_workspace_base_branch"`
	BaldaMCPServerIDs []string                `name:"balda_mcp_servers"`
	SessionMCPBinder  SessionMCPBinder        `optional:"true"`
	CapabilityBinder  SessionCapabilityBinder `optional:"true"`
	ProviderService   *providers.Service      `optional:"true"`
	MCPRegistry       *mcpregistry.MapRegistry
	Logger            zerolog.Logger
}

const (
	GoalExportStatusExported    = "exported"
	GoalExportStatusFailed      = "export_failed"
	GoalExportStatusNotExported = "not_exported"
	GoalExportReasonDisabled    = "workspace_disabled"
)

// GoalRunConfig configures a per-run /goalkeeper worker-validator execution context.
type GoalRunConfig struct {
	SourceSessionID string
	JobID           string
	UserID          string
	MaxIterations   uint
}

// GoalExportResult describes the export/finalization outcome for a passing
// /goalkeeper run.
type GoalExportResult struct {
	Status        string
	CommitMessage string
	Reason        string
	Error         string
}

// GoalRun owns the per-run /goalkeeper worker-validator runner and agents.
type GoalRun struct {
	Agent              adkagent.Agent
	Runner             *runner.Runner
	SessionID          string
	WorkspaceDir       string
	BranchName         string
	FinalizeFn         func(context.Context, string, string, string) (GoalExportResult, error)
	CleanupResourcesFn func(context.Context) error
}

type childRuntimeBase struct {
	runtime           *BuiltRuntime
	builder           *Builder
	providerID        string
	workingDir        string
	extraMCPServerIDs []string
	token             RuntimeBaseToken
}

type scopedRuntimeOwner struct {
	mu         sync.Mutex
	current    *BuiltRuntime
	closed     bool
	outerOnce  sync.Once
	outerErr   error
	outerClose func(*BuiltRuntime) error
}

func (o *scopedRuntimeOwner) closeOuter(runtime *BuiltRuntime) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.current != runtime {
		return nil
	}
	o.closed = true
	o.outerOnce.Do(func() {
		if o.outerClose != nil {
			o.outerErr = o.outerClose(runtime)
		}
	})
	return o.outerErr
}

// RuntimeBaseToken is an opaque process-local identity for one published
// child-runtime base. It is intentionally not durable.
type RuntimeBaseToken uint64

const recoveryRuntimeBaseFence RuntimeBaseToken = ^RuntimeBaseToken(0)

// UnpublishedSessionRuntimeCandidate owns a runtime which is invisible to
// shared runtime indexes until it wins the exact base-token publication CAS.
type UnpublishedSessionRuntimeCandidate struct {
	manager         *RuntimeManager
	sessionID       string
	runtime         *BuiltRuntime
	base            childRuntimeBase
	expectedToken   RuntimeBaseToken
	targetRuntimeID string
	mu              sync.Mutex
	state           uint8
	promoted        bool
	slotPublished   bool
	recovery        bool
}

const (
	candidateUnpublished uint8 = iota
	candidatePublishing
	candidatePublished
	candidateAborted
)

func (c *UnpublishedSessionRuntimeCandidate) Runtime() *BuiltRuntime { return c.runtime }
func (c *UnpublishedSessionRuntimeCandidate) ExpectedBaseToken() RuntimeBaseToken {
	return c.expectedToken
}

// ClaimForPublication installs a short child-lookup barrier after verifying
// the incumbent base token. Repeated claims by this candidate are idempotent.
func (c *UnpublishedSessionRuntimeCandidate) ClaimForPublication() error {
	if c == nil || c.manager == nil || c.runtime == nil || c.sessionID == "" {
		return ErrProviderUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == candidatePublishing || c.state == candidatePublished {
		return nil
	}
	if c.state != candidateUnpublished {
		return ErrProviderUnavailable
	}
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	if c.manager.baseTransitions == nil {
		c.manager.baseTransitions = make(map[string]RuntimeBaseToken)
	}
	current := c.manager.sessionBases[c.sessionID].token
	transition := c.manager.baseTransitions[c.sessionID]
	transitionAllowed := transition == 0 || c.recovery && transition == recoveryRuntimeBaseFence
	if current != c.expectedToken || !transitionAllowed {
		return ErrProviderUnavailable
	}
	c.manager.baseTransitions[c.sessionID] = c.base.token
	c.state = candidatePublishing
	return nil
}

// Promote publishes only the base registration while the lookup barrier is
// still held. The caller must immediately publish the matching RuntimeSlot.
func (c *UnpublishedSessionRuntimeCandidate) Promote() error {
	if c == nil || c.manager == nil {
		return ErrProviderUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == candidatePublished {
		return nil
	}
	if c.state != candidatePublishing {
		return ErrProviderUnavailable
	}
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	if c.manager.scopedRuntimes == nil {
		c.manager.scopedRuntimes = make(map[string]*BuiltRuntime)
	}
	if c.manager.sessionBases == nil {
		c.manager.sessionBases = make(map[string]childRuntimeBase)
	}
	if c.manager.knownSessionBases == nil {
		c.manager.knownSessionBases = make(map[string]struct{})
	}
	if c.manager.baseCandidates == nil {
		c.manager.baseCandidates = make(map[RuntimeBaseToken]*UnpublishedSessionRuntimeCandidate)
	}
	if c.manager.baseTransitions[c.sessionID] == c.base.token && c.manager.sessionBases[c.sessionID].token == c.base.token {
		c.promoted = true
		return nil
	}
	if c.manager.baseTransitions[c.sessionID] != c.base.token || c.manager.sessionBases[c.sessionID].token != c.expectedToken {
		return ErrProviderUnavailable
	}
	c.manager.scopedRuntimes[c.runtime.scopedID] = c.runtime
	c.manager.sessionBases[c.sessionID] = c.base
	c.manager.knownSessionBases[c.sessionID] = struct{}{}
	c.manager.baseCandidates[c.base.token] = c
	c.promoted = true
	return nil
}

// MarkPublished records that the matching RuntimeSlot pointer is visible. The
// child-lookup barrier remains until CompletePublication proves the journal
// terminal, so every intermediate crash state stays fail-closed.
func (c *UnpublishedSessionRuntimeCandidate) MarkPublished() {
	if c == nil || c.manager == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == candidatePublished || c.slotPublished {
		return
	}
	if c.state != candidatePublishing || !c.promoted {
		return
	}
	c.slotPublished = true
}

// CompletePublication releases the child-lookup barrier only after the caller
// has proved exact base, slot, and terminal operation state.
func (c *UnpublishedSessionRuntimeCandidate) CompletePublication() error {
	if c == nil || c.manager == nil {
		return ErrProviderUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == candidatePublished {
		return nil
	}
	if c.state != candidatePublishing || !c.promoted || !c.slotPublished {
		return ErrProviderUnavailable
	}
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	if c.manager.baseTransitions[c.sessionID] != c.base.token || c.manager.sessionBases[c.sessionID].token != c.base.token {
		return ErrProviderUnavailable
	}
	delete(c.manager.baseTransitions, c.sessionID)
	delete(c.manager.baseCandidates, c.base.token)
	c.state = candidatePublished
	return nil
}

// Abort settles an unpublished candidate exactly once. A promoted candidate
// is never removed by abort; forward recovery owns it from that point.
func (c *UnpublishedSessionRuntimeCandidate) Abort(context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.state == candidatePublished || c.state == candidateAborted {
		c.mu.Unlock()
		return nil
	}
	promoted := false
	if c.manager != nil {
		c.manager.mu.Lock()
		promoted = c.manager.sessionBases[c.sessionID].token == c.base.token
		if !promoted && c.manager.baseTransitions[c.sessionID] == c.base.token {
			delete(c.manager.baseTransitions, c.sessionID)
		}
		if !promoted {
			delete(c.manager.baseCandidates, c.base.token)
		}
		c.manager.mu.Unlock()
	}
	if promoted {
		c.mu.Unlock()
		return ErrProviderUnavailable
	}
	c.state = candidateAborted
	runtime := c.runtime
	c.mu.Unlock()
	if runtime != nil && runtime.Close != nil {
		return runtime.Close()
	}
	return nil
}

// Close releases child provider agents created for the workflow.
func (r *GoalRun) Close() error {
	if r == nil {
		return nil
	}
	return closeRuntimeAgent(r.Agent)
}

// Finalize completes a passing /goalkeeper run by exporting workspace-backed runs or
// reporting an explicit no-export outcome for direct mode.
func (r *GoalRun) Finalize(
	ctx context.Context,
	objective string,
	workerOutput string,
	validatorOutput string,
) (GoalExportResult, error) {
	if r == nil || r.FinalizeFn == nil {
		return GoalExportResult{Status: GoalExportStatusNotExported, Reason: GoalExportReasonDisabled}, nil
	}
	return r.FinalizeFn(ctx, objective, workerOutput, validatorOutput)
}

// CleanupResources deletes the isolated goal runtime session and its workspace.
func (r *GoalRun) CleanupResources(ctx context.Context) error {
	if r == nil || r.CleanupResourcesFn == nil {
		return nil
	}
	return r.CleanupResourcesFn(ctx)
}

// NewRuntimeManager creates the app-scoped balda runtime owner.
func NewRuntimeManager(p RuntimeManagerParams) *RuntimeManager {
	m := &RuntimeManager{
		builder:               p.Builder,
		providerID:            strings.TrimSpace(p.BaldaProviderID),
		workingDir:            strings.TrimSpace(p.WorkingDir),
		workspaceEnabled:      p.WorkspaceEnabled,
		baldaMCPServerIDs:     append([]string(nil), p.BaldaMCPServerIDs...),
		sessionMCPBinder:      p.SessionMCPBinder,
		capabilityBinder:      p.CapabilityBinder,
		providerService:       p.ProviderService,
		mcpRegistry:           p.MCPRegistry,
		goalWorkspaces:        NewWorkspaceManagerWithSessionsDir(p.WorkingDir, p.StateDir, p.WorkspaceBaseRef, "goals"),
		logger:                p.Logger.With().Str("component", "balda.runtime_manager").Logger(),
		scopedRuntimes:        make(map[string]*BuiltRuntime),
		sessionBases:          make(map[string]childRuntimeBase),
		knownSessionBases:     make(map[string]struct{}),
		baseTransitions:       make(map[string]RuntimeBaseToken),
		baseCandidates:        make(map[RuntimeBaseToken]*UnpublishedSessionRuntimeCandidate),
		replacementCandidates: make(map[string]*BuiltRuntime),
	}

	return m
}

// Stop releases the app-scoped provider runtime.
func (m *RuntimeManager) Stop(context.Context) error {
	return m.close()
}

// ProviderID returns the configured balda provider ID.
func (m *RuntimeManager) ProviderID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerID
}

// EnsureRuntime initializes the runtime if it has not been created yet.
func (m *RuntimeManager) EnsureRuntime(ctx context.Context) error {
	return m.EnsureProviderAvailable(ctx)
}

// EnsureProviderAvailable validates the current managed-provider graph before
// reusing or lazily building the provider runtime.
func (m *RuntimeManager) EnsureProviderAvailable(ctx context.Context) error {
	m.mu.RLock()
	builder := m.builder
	providerID := strings.TrimSpace(m.providerID)
	m.mu.RUnlock()
	if builder == nil {
		return fmt.Errorf("agent builder is required")
	}
	if providerID == "" {
		return fmt.Errorf("%w: balda provider is not configured", ErrProviderUnavailable)
	}
	if _, _, _, err := m.resolveProviderGraph(ctx, builder, providerID, ""); err != nil {
		return providerUnavailableError(err)
	}
	_, err := m.Runtime(ctx)
	return err
}

// Runtime returns the cached app-scoped runtime, creating it on first use.
func (m *RuntimeManager) Runtime(ctx context.Context) (*BuiltRuntime, error) {
	m.mu.RLock()
	if m.runtime != nil {
		runtime := m.runtime
		m.mu.RUnlock()
		return runtime, nil
	}
	builder := m.builder
	providerID := strings.TrimSpace(m.providerID)
	workingDir := m.workingDir
	extraMCPServerIDs := append([]string(nil), m.baldaMCPServerIDs...)
	binder := m.capabilityBinder
	m.mu.RUnlock()

	if builder == nil {
		return nil, fmt.Errorf("agent builder is required")
	}
	if providerID == "" {
		return nil, fmt.Errorf("%w: balda provider is not configured", ErrProviderUnavailable)
	}
	builder, providerID, providerBindingJSON, err := m.resolveProviderGraph(ctx, builder, providerID, "")
	if err != nil {
		return nil, providerUnavailableError(err)
	}
	var capabilities SessionCapabilityBinding
	if binder != nil {
		capabilities, err = binder.BindSessionCapabilities(ctx, providerID, SessionRuntimeRequest{WorkspaceDir: workingDir, ProviderConfigs: maps.Clone(builder.normaCfg.Providers)})
		if err != nil {
			return nil, errors.Join(fmt.Errorf("bind provider capabilities: %w", err), closeSessionCapabilities(capabilities))
		}
		if capabilities.SnapshotID == "" || capabilities.Skills.Snapshot != capabilities.SnapshotID {
			return nil, errors.Join(fmt.Errorf("provider capability binding is incomplete"), closeSessionCapabilities(capabilities))
		}
		builder, err = builder.withProviderMCP(capabilities.ProviderMCPServerIDs)
		if err != nil {
			return nil, errors.Join(err, closeSessionCapabilities(capabilities))
		}
		if capabilities.ProviderMCPServerIDs != nil {
			extraMCPServerIDs = append([]string(nil), capabilities.MCPServerIDs...)
		} else {
			extraMCPServerIDs = pinnedRuntimeMCPServerIDs(extraMCPServerIDs, capabilities.MCPServerIDs)
		}
	}

	runtime, err := builder.BuildRuntimeWithCapabilities(
		runtimeLifecycleContext(ctx),
		providerID,
		workingDir,
		nil,
		extraMCPServerIDs,
		capabilities.Skills,
		SessionInstructionContext{SnapshotID: capabilities.SnapshotID},
	)
	if err != nil {
		m.logger.Error().Err(err).Str("agent", providerID).Msg("failed to build balda provider runtime")
		return nil, errors.Join(providerUnavailableError(err), closeSessionCapabilities(capabilities))
	}
	closeProduct := runtime.Close
	var once sync.Once
	var closeErr error
	runtime.Close = func() error {
		once.Do(func() {
			if closeProduct != nil {
				closeErr = closeProduct()
			} else {
				closeErr = closeRuntimeAgent(runtime.Agent)
			}
			closeErr = errors.Join(closeErr, closeSessionCapabilities(capabilities))
		})
		return closeErr
	}
	runtime.RuntimeSnapshotID = string(capabilities.SnapshotID)
	runtime.ProviderBindingJSON = providerBindingJSON
	runtime.ProviderID = providerID

	m.mu.Lock()
	if existing := m.runtime; existing != nil {
		m.mu.Unlock()
		if runtime != nil {
			if closeErr := runtime.Close(); closeErr != nil {
				m.logger.Warn().Err(closeErr).Str("agent", providerID).Msg("failed to close duplicate balda provider runtime")
			}
		}
		return existing, nil
	}
	m.runtime = runtime
	m.runtimeBuilder = builder
	m.runtimeMCPServerIDs = append([]string(nil), extraMCPServerIDs...)
	m.mu.Unlock()

	m.logger.Info().Str("agent", providerID).Msg("balda provider runtime ready")
	return runtime, nil
}

// RuntimeForSession builds a provider runtime whose bundled MCP URL is bound
// to one exact authenticated Balda locator. It intentionally uses a distinct
// ACP process/runtime because the provider's MCP headers are static for the
// lifetime of an ACP process and cannot be safely mutated between turns.
func (m *RuntimeManager) RuntimeForSession(ctx context.Context, request SessionRuntimeRequest) (*BuiltRuntime, error) {
	return m.buildSessionRuntime(ctx, request, true)
}

// BuildUnpublishedSessionRuntime constructs an independently owned runtime
// without exposing it through scoped runtime or child-runtime indexes.
func (m *RuntimeManager) BuildUnpublishedSessionRuntime(ctx context.Context, request SessionRuntimeRequest) (*UnpublishedSessionRuntimeCandidate, error) {
	runtime, err := m.buildSessionRuntime(ctx, request, false)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(request.Locator.SessionID)
	if sessionID == "" {
		_ = runtime.Close()
		return nil, fmt.Errorf("session id is required")
	}
	m.mu.RLock()
	expected := m.sessionBases[sessionID].token
	m.mu.RUnlock()
	base := childRuntimeBase{
		runtime: runtime, builder: runtime.baseBuilder, providerID: runtime.baseProviderID,
		workingDir: runtime.baseWorkingDir, extraMCPServerIDs: append([]string(nil), runtime.baseMCPServerIDs...),
		token: RuntimeBaseToken(runtime.RuntimeBaseToken),
	}
	return &UnpublishedSessionRuntimeCandidate{
		manager: m, sessionID: sessionID, runtime: runtime, base: base, expectedToken: expected,
		targetRuntimeID: strings.TrimSpace(request.AgentSessionID), recovery: request.RecoveryFence,
	}, nil
}

// FenceSessionRuntimeBase makes child lookup fail closed before cold recovery
// performs any candidate verification. Repeating it is idempotent.
func (m *RuntimeManager) FenceSessionRuntimeBase(sessionID string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return ErrProviderUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseTransitions == nil {
		m.baseTransitions = make(map[string]RuntimeBaseToken)
	}
	if current := m.baseTransitions[sessionID]; current != 0 && current != recoveryRuntimeBaseFence {
		return nil
	}
	m.baseTransitions[sessionID] = recoveryRuntimeBaseFence
	if m.knownSessionBases == nil {
		m.knownSessionBases = make(map[string]struct{})
	}
	m.knownSessionBases[sessionID] = struct{}{}
	return nil
}

// CompleteSessionRuntimeRecovery clears only the cold-recovery sentinel after
// the exact restored RuntimeSlot and its published base share one token.
func (m *RuntimeManager) CompleteSessionRuntimeRecovery(sessionID string, token uint64) error {
	if m == nil || strings.TrimSpace(sessionID) == "" || token == 0 {
		return ErrProviderUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseTransitions[sessionID] == 0 {
		return nil
	}
	if m.baseTransitions[sessionID] != recoveryRuntimeBaseFence || m.sessionBases[sessionID].token != RuntimeBaseToken(token) {
		return ErrProviderUnavailable
	}
	delete(m.baseTransitions, sessionID)
	return nil
}

// AdoptPromotedSessionRuntime returns the exact same-process candidate for a
// base already promoted under its still-held publication barrier.
func (m *RuntimeManager) AdoptPromotedSessionRuntime(sessionID string, incumbentToken uint64, request SessionRuntimeRequest) (*UnpublishedSessionRuntimeCandidate, bool) {
	if m == nil {
		return nil, false
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.RLock()
	transition := m.baseTransitions[sessionID]
	base := m.sessionBases[sessionID]
	candidate := m.baseCandidates[transition]
	m.mu.RUnlock()
	if sessionID == "" || transition == 0 || transition == recoveryRuntimeBaseFence || base.token != transition ||
		candidate == nil || incumbentToken != 0 && candidate.expectedToken != RuntimeBaseToken(incumbentToken) || candidate.runtime != base.runtime ||
		candidate.targetRuntimeID != strings.TrimSpace(request.AgentSessionID) ||
		candidate.runtime.RuntimeSnapshotID != strings.TrimSpace(request.RuntimeSnapshotID) ||
		candidate.runtime.ProviderBindingJSON != strings.TrimSpace(request.ProviderBindingJSON) ||
		candidate.runtime.RecipeModel != strings.TrimSpace(request.ExpectedRecipeModel) {
		return nil, false
	}
	return candidate, true
}

func (m *RuntimeManager) buildSessionRuntime(ctx context.Context, request SessionRuntimeRequest, publish bool) (*BuiltRuntime, error) {
	if m == nil {
		return nil, fmt.Errorf("balda runtime manager is required")
	}
	m.mu.RLock()
	binder := m.capabilityBinder
	providerID := m.providerID
	builder := m.builder
	m.mu.RUnlock()
	builder, providerID, providerBindingJSON, err := m.resolveProviderGraph(ctx, builder, providerID, request.ProviderBindingJSON)
	if err != nil {
		return nil, providerUnavailableError(err)
	}
	if binder == nil {
		return nil, fmt.Errorf("session capability binder is required")
	}
	if builder != nil {
		request.ProviderConfigs = maps.Clone(builder.normaCfg.Providers)
	}
	capabilities, err := binder.BindSessionCapabilities(ctx, providerID, request)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("bind session capabilities: %w", err), closeSessionCapabilities(capabilities))
	}
	snapshotID := strings.TrimSpace(string(capabilities.SnapshotID))
	if snapshotID == "" || capabilities.Skills.Snapshot != capabilities.SnapshotID {
		return nil, errors.Join(fmt.Errorf("session capability binding is incomplete"), closeSessionCapabilities(capabilities))
	}
	requestedSnapshotID := strings.TrimSpace(request.RuntimeSnapshotID)
	if requestedSnapshotID != "" && snapshotID != requestedSnapshotID {
		return nil, errors.Join(
			fmt.Errorf("session capability snapshot %q does not match requested snapshot %q", snapshotID, requestedSnapshotID),
			closeSessionCapabilities(capabilities),
		)
	}
	request.RuntimeSnapshotID = snapshotID
	return m.runtimeForSession(ctx, request, capabilities, builder, providerID, providerBindingJSON, publish)
}

func (m *RuntimeManager) runtimeForSession(ctx context.Context, request SessionRuntimeRequest, capabilities SessionCapabilityBinding,
	builder *Builder, providerID, providerBindingJSON string, publish bool,
) (*BuiltRuntime, error) {
	if m == nil {
		return nil, fmt.Errorf("balda runtime manager is required")
	}
	m.mu.RLock()
	workingDir := strings.TrimSpace(request.WorkspaceDir)
	binder := m.sessionMCPBinder
	registry := m.mcpRegistry
	hostMCPServerIDs := append([]string(nil), m.baldaMCPServerIDs...)
	if workingDir == "" {
		workingDir = m.workingDir
	}
	m.mu.RUnlock()
	bindingEnabled := binder != nil && registry != nil
	if status, ok := binder.(sessionMCPBinderStatus); ok && !status.SessionMCPEnabled(capabilities) {
		bindingEnabled = false
	}
	hasCapabilities := strings.TrimSpace(string(capabilities.SnapshotID)) != ""
	if publish && !bindingEnabled && !hasCapabilities && providerBindingJSON == "" {
		return m.Runtime(ctx)
	}
	if len(capabilities.MCPServerIDs) > 0 && registry == nil {
		return nil, errors.Join(fmt.Errorf("MCP registry is required for session capabilities"), closeSessionCapabilities(capabilities))
	}
	if builder == nil {
		return nil, errors.Join(fmt.Errorf("agent builder is required"), closeSessionCapabilities(capabilities))
	}
	if providerID == "" {
		return nil, errors.Join(
			fmt.Errorf("%w: balda provider is not configured", ErrProviderUnavailable),
			closeSessionCapabilities(capabilities),
		)
	}
	var err error
	builder, err = builder.withProviderMCP(capabilities.ProviderMCPServerIDs)
	if err != nil {
		return nil, errors.Join(providerUnavailableError(err), closeSessionCapabilities(capabilities))
	}
	if capabilities.ProviderMCPServerIDs != nil {
		// Host defaults have already been resolved to this exact snapshot.
		hostMCPServerIDs = nil
	}
	var binding ScopedMCPServer
	extraMCPServerIDs := pinnedRuntimeMCPServerIDs(hostMCPServerIDs, capabilities.MCPServerIDs)
	if bindingEnabled {
		var err error
		binding, err = binder.BindSession(ctx, request)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("bind session MCP context: %w", err),
				releaseSessionBinding(binding),
				closeSessionCapabilities(capabilities),
			)
		}
		if strings.TrimSpace(binding.ID) == "" || strings.TrimSpace(binding.Config.URL) == "" {
			return nil, errors.Join(
				fmt.Errorf("session MCP binding is incomplete"),
				releaseSessionBinding(binding),
				closeSessionCapabilities(capabilities),
			)
		}
		registry.Set(binding.ID, binding.Config)
		extraMCPServerIDs = append(extraMCPServerIDs, binding.ID)
	}
	runtime, err := builder.BuildRuntimeWithCapabilitiesAndModelOverride(
		runtimeLifecycleContext(ctx),
		providerID,
		workingDir,
		nil,
		extraMCPServerIDs,
		capabilities.Skills,
		SessionInstructionContext{
			SnapshotID:  capabilities.SnapshotID,
			SessionID:   strings.TrimSpace(request.Locator.SessionID),
			ChannelType: strings.TrimSpace(request.Locator.ChannelType),
		},
		request.ModelOverride,
	)
	if err != nil {
		if binding.ID != "" {
			registry.Delete(binding.ID)
		}
		return nil, errors.Join(
			providerUnavailableError(err),
			releaseSessionBinding(binding),
			closeSessionCapabilities(capabilities),
		)
	}
	closeProduct := runtime.Close
	m.mu.Lock()
	m.scopedSequence++
	m.baseSequence++
	scopedID := binding.ID
	if scopedID == "" {
		scopedID = fmt.Sprintf("session-runtime-%d", m.scopedSequence)
	}
	baseToken := RuntimeBaseToken(m.baseSequence)
	m.mu.Unlock()
	owner := &scopedRuntimeOwner{current: runtime}
	owner.outerClose = func(current *BuiltRuntime) error {
		if binding.ID != "" {
			registry.Delete(binding.ID)
		}
		outerErr := errors.Join(releaseSessionBinding(binding), closeSessionCapabilities(capabilities))
		m.mu.Lock()
		if registered, ok := m.scopedRuntimes[scopedID]; ok && registered == current {
			delete(m.scopedRuntimes, scopedID)
		}
		sessionID := strings.TrimSpace(request.Locator.SessionID)
		if registered, ok := m.sessionBases[sessionID]; ok && registered.runtime == current {
			delete(m.sessionBases, sessionID)
		}
		m.mu.Unlock()
		return outerErr
	}
	var once sync.Once
	var closeErr error
	closeScoped := func() error {
		once.Do(func() {
			if closeProduct != nil {
				closeErr = closeProduct()
			} else {
				closeErr = closeRuntimeAgent(runtime.Agent)
			}
			closeErr = errors.Join(closeErr, owner.closeOuter(runtime))
		})
		return closeErr
	}
	runtime.Close = closeScoped
	runtime.scopedID = scopedID
	runtime.RuntimeBaseToken = uint64(baseToken)
	runtime.baseBuilder = builder
	runtime.baseProviderID = providerID
	runtime.baseWorkingDir = workingDir
	runtime.baseMCPServerIDs = append([]string(nil), extraMCPServerIDs...)
	runtime.scopedOwner = owner
	runtime.promoteReplacement = m.replacementBasePromoter(strings.TrimSpace(request.Locator.SessionID), scopedID, baseToken, runtime)
	runtime.RuntimeSnapshotID = string(capabilities.SnapshotID)
	runtime.ProviderBindingJSON = providerBindingJSON
	runtime.ProviderID = providerID
	if !publish {
		return runtime, nil
	}
	m.mu.Lock()
	if m.scopedRuntimes == nil {
		m.scopedRuntimes = make(map[string]*BuiltRuntime)
	}
	if m.sessionBases == nil {
		m.sessionBases = make(map[string]childRuntimeBase)
	}
	if m.knownSessionBases == nil {
		m.knownSessionBases = make(map[string]struct{})
	}
	if m.baseTransitions == nil {
		m.baseTransitions = make(map[string]RuntimeBaseToken)
	}
	m.scopedRuntimes[scopedID] = runtime
	if sessionID := strings.TrimSpace(request.Locator.SessionID); sessionID != "" {
		m.sessionBases[sessionID] = childRuntimeBase{
			runtime: runtime, builder: builder, providerID: providerID, workingDir: workingDir,
			extraMCPServerIDs: append([]string(nil), extraMCPServerIDs...),
			token:             baseToken,
		}
		m.knownSessionBases[sessionID] = struct{}{}
	}
	m.mu.Unlock()
	return runtime, nil
}

func (m *RuntimeManager) replacementBasePromoter(sessionID, scopedID string, token RuntimeBaseToken, expected *BuiltRuntime) func(*BuiltRuntime) error {
	return func(candidate *BuiltRuntime) error {
		if m == nil || candidate == nil || sessionID == "" || token == 0 {
			return ErrProviderUnavailable
		}
		candidate.replacementMu.Lock()
		defer candidate.replacementMu.Unlock()
		if candidate.replacementClosed {
			return ErrProviderUnavailable
		}
		if candidate.replacementPromoted {
			return nil
		}
		owner := expected.scopedOwner
		if owner == nil {
			return ErrProviderUnavailable
		}
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.closed || owner.current != expected {
			return ErrProviderUnavailable
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		base, ok := m.sessionBases[sessionID]
		if !ok || base.token != token || base.runtime != expected || m.baseTransitions[sessionID] != 0 {
			return ErrProviderUnavailable
		}
		if m.replacementCandidates == nil {
			m.replacementCandidates = make(map[string]*BuiltRuntime)
		}
		m.baseTransitions[sessionID] = token
		m.replacementCandidates[sessionID] = candidate
		owner.current = candidate
		candidate.scopedID = scopedID
		candidate.scopedOwner = owner
		candidate.RuntimeBaseToken = uint64(token)
		candidate.baseBuilder = base.builder
		candidate.baseProviderID = base.providerID
		candidate.baseWorkingDir = base.workingDir
		candidate.baseMCPServerIDs = append([]string(nil), base.extraMCPServerIDs...)
		candidate.promoteReplacement = m.replacementBasePromoter(sessionID, scopedID, token, candidate)
		candidate.completeReplacement = m.replacementBaseCompleter(sessionID, token)
		base.runtime = candidate
		m.sessionBases[sessionID] = base
		m.scopedRuntimes[scopedID] = candidate
		candidate.replacementPromoted = true
		return nil
	}
}

func (m *RuntimeManager) replacementBaseCompleter(sessionID string, token RuntimeBaseToken) func(*BuiltRuntime) error {
	return func(candidate *BuiltRuntime) error {
		if m == nil || candidate == nil || sessionID == "" || token == 0 {
			return ErrProviderUnavailable
		}
		candidate.replacementMu.Lock()
		defer candidate.replacementMu.Unlock()
		if candidate.replacementCompleted {
			return nil
		}
		if !candidate.replacementPromoted || candidate.replacementClosed {
			return ErrProviderUnavailable
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		base, ok := m.sessionBases[sessionID]
		if !ok || base.token != token || base.runtime != candidate ||
			m.baseTransitions[sessionID] != token || m.replacementCandidates[sessionID] != candidate {
			return ErrProviderUnavailable
		}
		delete(m.baseTransitions, sessionID)
		delete(m.replacementCandidates, sessionID)
		candidate.replacementCompleted = true
		return nil
	}
}

// AdoptPromotedModelReplacement returns an exact hosted candidate protected by
// the child-lookup barrier after its base transfer but before publication proof.
func (m *RuntimeManager) AdoptPromotedModelReplacement(
	sessionID string,
	incumbentToken uint64,
	runtimeSnapshotID string,
	providerBindingJSON string,
	targetModel string,
) (*BuiltRuntime, bool) {
	if m == nil || incumbentToken == 0 {
		return nil, false
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.RLock()
	transition := m.baseTransitions[sessionID]
	base := m.sessionBases[sessionID]
	candidate := m.replacementCandidates[sessionID]
	m.mu.RUnlock()
	if sessionID == "" || transition != RuntimeBaseToken(incumbentToken) || base.token != transition ||
		candidate == nil || base.runtime != candidate || candidate.RuntimeBaseToken != incumbentToken ||
		candidate.RuntimeSnapshotID != strings.TrimSpace(runtimeSnapshotID) ||
		candidate.ProviderBindingJSON != strings.TrimSpace(providerBindingJSON) ||
		candidate.ConfirmedModel != strings.TrimSpace(targetModel) {
		return nil, false
	}
	return candidate, true
}

func (m *RuntimeManager) resolveProviderGraph(ctx context.Context, builder *Builder, rootID, bindingJSON string) (*Builder, string, string, error) {
	rootID = strings.TrimSpace(rootID)
	bindingJSON = strings.TrimSpace(bindingJSON)
	m.mu.RLock()
	service := m.providerService
	m.mu.RUnlock()
	if service == nil {
		if bindingJSON != "" {
			return nil, "", "", fmt.Errorf("restore provider binding: %w", providers.ErrUnavailable)
		}
		return builder, rootID, "", nil
	}
	var (
		compiled providers.Compiled
		err      error
	)
	if bindingJSON == "" {
		compiled, err = service.CompileCurrent(ctx, rootID)
	} else {
		var binding providers.Binding
		if json.Unmarshal([]byte(bindingJSON), &binding) != nil {
			return nil, "", "", fmt.Errorf("decode provider binding: %w", providers.ErrUnavailable)
		}
		compiled, err = service.Restore(ctx, binding)
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve provider graph: %w", err)
	}
	boundBuilder, err := builder.withProviders(compiled.Configs)
	if err != nil {
		return nil, "", "", err
	}
	encoded, err := json.Marshal(compiled.Binding)
	if err != nil {
		return nil, "", "", fmt.Errorf("encode provider binding: %w", err)
	}
	return boundBuilder, compiled.Binding.RootProviderID, string(encoded), nil
}

func providerUnavailableError(err error) error {
	if err == nil || errors.Is(err, ErrProviderUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
}

// BuildDedicatedRuntime resolves the current graph for an isolated internal provider.
func (m *RuntimeManager) BuildDedicatedRuntime(ctx context.Context, providerID, workspaceDir, instruction string) (*BuiltRuntime, error) {
	m.mu.RLock()
	builder := m.builder
	m.mu.RUnlock()
	bound, resolvedID, _, err := m.resolveProviderGraph(ctx, builder, providerID, "")
	if err != nil {
		return nil, providerUnavailableError(err)
	}
	runtime, err := bound.BuildDedicatedRuntime(ctx, resolvedID, workspaceDir, instruction)
	if err != nil {
		return nil, providerUnavailableError(err)
	}
	return runtime, nil
}

// runtimeLifecycleContext preserves request values without tying an owned
// provider subprocess to the request's cancellation. RuntimeManager closes
// app-scoped and session-scoped runtimes explicitly.
func runtimeLifecycleContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func releaseSessionBinding(binding ScopedMCPServer) error {
	if binding.Release == nil {
		return nil
	}
	if err := binding.Release(); err != nil {
		return ErrSessionBindingCleanup
	}
	return nil
}

func closeSessionCapabilities(capabilities SessionCapabilityBinding) error {
	if capabilities.Close == nil {
		return nil
	}
	if err := capabilities.Close(); err != nil {
		return ErrSessionCapabilityCleanup
	}
	return nil
}

func pinnedRuntimeMCPServerIDs(hostConfigured, snapshotPinned []string) []string {
	return mergeMCPServerIDsWithBase(hostConfigured, nil, snapshotPinned)
}

// PrepareGoalRun creates an isolated GoalKeeper execution context using the
// app-scoped provider runtime. Workspace-enabled runs get a per-task worktree;
// direct-mode runs use the app working directory and skip export.
func (m *RuntimeManager) PrepareGoalRun(
	ctx context.Context,
	cfg GoalRunConfig,
) (*GoalRun, error) {
	userID := strings.TrimSpace(cfg.UserID)
	if userID == "" {
		return nil, fmt.Errorf("goal user id is required")
	}
	jobID := strings.TrimSpace(cfg.JobID)
	if jobID == "" {
		return nil, fmt.Errorf("goal job id is required")
	}
	sourceSessionID := strings.TrimSpace(cfg.SourceSessionID)
	if sourceSessionID == "" {
		return nil, fmt.Errorf("source session id is required")
	}
	base, err := m.childRuntimeBase(ctx, sourceSessionID)
	if err != nil {
		return nil, err
	}
	goalSessionID := jobID
	workerSessionID := goalSessionID + "-worker"
	validatorSessionID := goalSessionID + "-validator"
	branchName := ""
	workspaceDir := base.workingDir
	cleanupWorkspace := false
	exportWorkspace := func(context.Context, string, string, string, string) (GoalExportResult, error) {
		return GoalExportResult{Status: GoalExportStatusNotExported, Reason: GoalExportReasonDisabled}, nil
	}
	if m.workspaceEnabled {
		branchName = goalWorkspaceBranchName(jobID)
		workspace, err := m.goalWorkspaces.EnsureWorkspace(
			ctx,
			jobID,
			branchName,
			m.goalWorkspaces.CanonicalWorkspaceDir(jobID),
		)
		if err != nil {
			if errors.Is(err, ErrWorkspaceCollision) {
				workspace, err = m.goalWorkspaces.ForceRemountCanonicalWorkspace(ctx, jobID, branchName)
			}
			if err != nil {
				return nil, fmt.Errorf("create goal workspace: %w", err)
			}
		}
		workspaceDir = workspace.Dir
		cleanupWorkspace = true
		exportWorkspace = func(
			ctx context.Context,
			objective string,
			workerOutput string,
			validatorOutput string,
			workspaceDir string,
		) (GoalExportResult, error) {
			commitMessage, commitErr := base.buildGoalCommitMessage(
				ctx,
				userID,
				sourceSessionID,
				goalSessionID,
				branchName,
				workspaceDir,
				objective,
				workerOutput,
				validatorOutput,
			)
			if commitErr != nil {
				m.logger.Warn().Err(commitErr).Str("job_id", jobID).Msg("failed to generate goal export commit message; using fallback")
			}
			commitMessage = normalizeGoalCommitMessage(objective, commitMessage)
			if err := m.goalWorkspaces.Export(ctx, workspaceDir, branchName, commitMessage); err != nil {
				return GoalExportResult{
					Status:        GoalExportStatusFailed,
					CommitMessage: commitMessage,
					Error:         err.Error(),
				}, err
			}
			return GoalExportResult{
				Status:        GoalExportStatusExported,
				CommitMessage: commitMessage,
			}, nil
		}
	}
	if _, err := base.builder.CreateRuntimeSession(
		ctx,
		base.runtime,
		base.providerID,
		userID,
		goalSessionID,
		workspaceDir,
		RuntimeSessionContext{
			BaldaSessionID: goalSessionID,
			SessionBranch:  branchName,
		},
	); err != nil {
		if cleanupWorkspace {
			_ = m.goalWorkspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return nil, fmt.Errorf("create goal runtime session: %w", err)
	}
	if _, err := base.builder.CreateRuntimeSession(
		ctx,
		base.runtime,
		base.providerID,
		userID,
		workerSessionID,
		workspaceDir,
		RuntimeSessionContext{
			BaldaSessionID: goalSessionID,
			SessionBranch:  branchName,
		},
	); err != nil {
		_ = base.deleteRuntimeSession(ctx, userID, goalSessionID)
		if cleanupWorkspace {
			_ = m.goalWorkspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return nil, fmt.Errorf("create goal worker runtime session: %w", err)
	}
	if _, err := base.builder.CreateRuntimeSession(
		ctx,
		base.runtime,
		base.providerID,
		userID,
		validatorSessionID,
		workspaceDir,
		RuntimeSessionContext{
			BaldaSessionID: goalSessionID,
			SessionBranch:  branchName,
		},
	); err != nil {
		_ = base.deleteRuntimeSession(ctx, userID, workerSessionID)
		_ = base.deleteRuntimeSession(ctx, userID, goalSessionID)
		if cleanupWorkspace {
			_ = m.goalWorkspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return nil, fmt.Errorf("create goal validator runtime session: %w", err)
	}

	workflow, err := base.builder.BuildGoalWorkflow(ctx, GoalBuildConfig{
		BaseAgent:          base.runtime.Agent,
		ProviderID:         base.providerID,
		SessionID:          sourceSessionID,
		WorkerSessionID:    workerSessionID,
		ValidatorSessionID: validatorSessionID,
		BranchName:         branchName,
		WorkspaceDir:       workspaceDir,
		MaxIterations:      cfg.MaxIterations,
		AppName:            base.runtime.AppName,
		SessionService:     base.runtime.SessionSvc,
		ExtraMCPServerIDs:  base.extraMCPServerIDs,
	})
	if err != nil {
		_ = base.deleteRuntimeSession(ctx, userID, validatorSessionID)
		_ = base.deleteRuntimeSession(ctx, userID, workerSessionID)
		_ = base.deleteRuntimeSession(ctx, userID, goalSessionID)
		if cleanupWorkspace {
			_ = m.goalWorkspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return nil, err
	}
	r, err := base.runner(workflow, "goal", workspaceDir)
	if err != nil {
		_ = closeRuntimeAgent(workflow)
		_ = base.deleteRuntimeSession(ctx, userID, validatorSessionID)
		_ = base.deleteRuntimeSession(ctx, userID, workerSessionID)
		_ = base.deleteRuntimeSession(ctx, userID, goalSessionID)
		if cleanupWorkspace {
			_ = m.goalWorkspaces.CleanupWorkspace(ctx, workspaceDir)
		}
		return nil, err
	}
	return &GoalRun{
		Agent:        workflow,
		Runner:       r,
		SessionID:    goalSessionID,
		WorkspaceDir: workspaceDir,
		BranchName:   branchName,
		FinalizeFn: func(
			ctx context.Context,
			objective string,
			workerOutput string,
			validatorOutput string,
		) (GoalExportResult, error) {
			return exportWorkspace(ctx, objective, workerOutput, validatorOutput, workspaceDir)
		},
		CleanupResourcesFn: func(ctx context.Context) error {
			sessionErr := base.deleteRuntimeSession(ctx, userID, goalSessionID)
			workerSessionErr := base.deleteRuntimeSession(ctx, userID, workerSessionID)
			validatorSessionErr := base.deleteRuntimeSession(ctx, userID, validatorSessionID)
			var workspaceErr error
			if cleanupWorkspace {
				workspaceErr = m.goalWorkspaces.CleanupWorkspace(ctx, workspaceDir)
			}
			return errors.Join(sessionErr, workerSessionErr, validatorSessionErr, workspaceErr)
		},
	}, nil
}

func (m *RuntimeManager) childRuntimeBase(ctx context.Context, sourceSessionID string) (childRuntimeBase, error) {
	m.mu.RLock()
	sessionID := strings.TrimSpace(sourceSessionID)
	bound, ok := m.sessionBases[sessionID]
	_, known := m.knownSessionBases[sessionID]
	transitioning := m.baseTransitions[sessionID] != 0
	m.mu.RUnlock()
	if transitioning {
		return childRuntimeBase{}, ErrProviderUnavailable
	}
	if ok && bound.runtime != nil && bound.builder != nil {
		return bound, nil
	}
	if known {
		return childRuntimeBase{}, ErrProviderUnavailable
	}
	runtime, err := m.Runtime(ctx)
	if err != nil {
		return childRuntimeBase{}, err
	}

	m.mu.RLock()
	base := childRuntimeBase{
		runtime:           runtime,
		builder:           m.runtimeBuilder,
		providerID:        strings.TrimSpace(m.providerID),
		workingDir:        strings.TrimSpace(m.workingDir),
		extraMCPServerIDs: append([]string(nil), m.runtimeMCPServerIDs...),
	}
	m.mu.RUnlock()

	if base.builder == nil {
		return childRuntimeBase{}, fmt.Errorf("agent builder is required")
	}
	if base.providerID == "" {
		return childRuntimeBase{}, fmt.Errorf("%w: balda provider is not configured", ErrProviderUnavailable)
	}
	return base, nil
}

func (b childRuntimeBase) runner(agent adkagent.Agent, label, workingDir string) (*runner.Runner, error) {
	r, err := b.builder.newRunner(runner.Config{
		AppName:        b.runtime.AppName,
		Agent:          agent,
		SessionService: b.runtime.SessionSvc,
	}, ToolHookScope{
		Enabled:  true,
		Provider: b.providerID,
		CWD:      strings.TrimSpace(workingDir),
	})
	if err != nil {
		return nil, fmt.Errorf("creating %s runner: %w", label, err)
	}
	return r, nil
}

func (b childRuntimeBase) deleteRuntimeSession(ctx context.Context, userID, sessionID string) error {
	if b.runtime == nil || b.runtime.SessionSvc == nil {
		return nil
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	appName := strings.TrimSpace(b.runtime.AppName)
	if appName == "" {
		appName = defaultRuntimeAppName
	}
	if err := b.runtime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{
		AppName:   appName,
		UserID:    strings.TrimSpace(userID),
		SessionID: strings.TrimSpace(sessionID),
	}); err != nil {
		return fmt.Errorf("delete goal runtime session: %w", err)
	}
	return nil
}

func (b childRuntimeBase) buildGoalCommitMessage(
	ctx context.Context,
	userID string,
	sourceSessionID string,
	goalSessionID string,
	branchName string,
	workspaceDir string,
	objective string,
	workerOutput string,
	validatorOutput string,
) (string, error) {
	agent, err := buildGoalCommitAgent(b.runtime.Agent)
	if err != nil {
		return "", err
	}

	r, err := b.runner(agent, "goal commit message", workspaceDir)
	if err != nil {
		return "", err
	}
	commitSessionID := goalSessionID + "-commit"
	if _, err := b.builder.CreateRuntimeSession(
		ctx,
		b.runtime,
		b.providerID,
		userID,
		commitSessionID,
		workspaceDir,
		RuntimeSessionContext{
			BaldaSessionID: goalSessionID,
			SessionBranch:  branchName,
		},
	); err != nil {
		return "", fmt.Errorf("create goal commit session: %w", err)
	}
	defer func() { _ = b.deleteRuntimeSession(ctx, userID, commitSessionID) }()

	prompt := genai.NewContentFromText(strings.TrimSpace(strings.Join([]string{
		"Goal objective:",
		strings.TrimSpace(objective),
		"",
		"Worker summary:",
		strings.TrimSpace(workerOutput),
		"",
		"Validator summary:",
		strings.TrimSpace(validatorOutput),
	}, "\n")), genai.RoleUser)
	var output string
	for ev, err := range r.Run(ctx, userID, commitSessionID, prompt, adkagent.RunConfig{}) {
		if err != nil {
			return output, fmt.Errorf("run goal commit generator: %w", err)
		}
		if text := visibleGoalEventText(ev); text != "" {
			output = text
		}
	}
	return output, nil
}

func goalWorkspaceBranchName(jobID string) string {
	return "norma/balda/goal/" + strings.TrimSpace(jobID)
}

func (m *RuntimeManager) close() error {
	m.mu.Lock()
	runtime := m.runtime
	pendingReplacements := make([]*BuiltRuntime, 0, len(m.replacementCandidates))
	for _, candidate := range m.replacementCandidates {
		if candidate != nil {
			pendingReplacements = append(pendingReplacements, candidate)
		}
	}
	scoped := make([]*BuiltRuntime, 0, len(m.scopedRuntimes))
	for _, item := range m.scopedRuntimes {
		scoped = append(scoped, item)
	}
	m.scopedRuntimes = make(map[string]*BuiltRuntime)
	m.sessionBases = make(map[string]childRuntimeBase)
	m.knownSessionBases = make(map[string]struct{})
	m.baseTransitions = make(map[string]RuntimeBaseToken)
	m.baseCandidates = make(map[RuntimeBaseToken]*UnpublishedSessionRuntimeCandidate)
	m.replacementCandidates = make(map[string]*BuiltRuntime)
	m.replacementCandidates = make(map[string]*BuiltRuntime)
	m.runtime = nil
	m.runtimeBuilder = nil
	m.runtimeMCPServerIDs = nil
	m.mu.Unlock()
	// Manager shutdown owns every unpublished/promoted candidate after the maps
	// are detached. Allow their normal aggregate close path to settle exactly
	// once even when publication proof never arrived.
	for _, candidate := range pendingReplacements {
		candidate.replacementMu.Lock()
		candidate.replacementCompleted = true
		candidate.replacementMu.Unlock()
	}
	var errs []error
	for _, item := range scoped {
		if item == nil {
			continue
		}
		if item.Close != nil {
			errs = append(errs, item.Close())
		} else {
			errs = append(errs, closeRuntimeAgent(item.Agent))
		}
	}
	if runtime != nil {
		if runtime.Close != nil {
			errs = append(errs, runtime.Close())
		} else {
			errs = append(errs, closeRuntimeAgent(runtime.Agent))
		}
	}
	return errors.Join(errs...)
}

func closeRuntimeAgent(agent any) error {
	if agent == nil {
		return nil
	}
	errs := make([]error, 0)
	if ag, ok := agent.(adkagent.Agent); ok {
		for _, sub := range ag.SubAgents() {
			if err := closeRuntimeAgent(sub); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if closer, ok := agent.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			if !shutdown.IsExpected(err) {
				errs = append(errs, fmt.Errorf("close balda runtime agent: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}
