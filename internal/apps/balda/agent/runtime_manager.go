package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/shutdown"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// SessionRuntimeRequest identifies the authenticated session that owns a
// scoped provider runtime and its internal MCP capability.
type SessionRuntimeRequest struct {
	Locator        deliverycmd.Locator
	UserID         string
	AgentSessionID string
	LineageID      string
	WorkspaceDir   string
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
	SessionMCPEnabled() bool
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
	mcpRegistry       *mcpregistry.MapRegistry
	logger            zerolog.Logger

	mu             sync.RWMutex
	runtime        *BuiltRuntime
	scopedRuntimes map[string]*BuiltRuntime
}

// RuntimeManagerParams wires RuntimeManager dependencies.
type RuntimeManagerParams struct {
	fx.In

	Builder           *Builder
	BaldaProviderID   string `name:"balda_provider"`
	WorkingDir        string
	StateDir          string           `name:"balda_state_dir"`
	WorkspaceEnabled  bool             `name:"balda_workspace_enabled"`
	WorkspaceBaseRef  string           `name:"balda_workspace_base_branch"`
	BaldaMCPServerIDs []string         `name:"balda_mcp_servers"`
	SessionMCPBinder  SessionMCPBinder `optional:"true"`
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
		builder:           p.Builder,
		providerID:        strings.TrimSpace(p.BaldaProviderID),
		workingDir:        strings.TrimSpace(p.WorkingDir),
		workspaceEnabled:  p.WorkspaceEnabled,
		baldaMCPServerIDs: append([]string(nil), p.BaldaMCPServerIDs...),
		sessionMCPBinder:  p.SessionMCPBinder,
		mcpRegistry:       p.MCPRegistry,
		goalWorkspaces:    NewWorkspaceManagerWithSessionsDir(p.WorkingDir, p.StateDir, p.WorkspaceBaseRef, "goals"),
		logger:            p.Logger.With().Str("component", "balda.runtime_manager").Logger(),
		scopedRuntimes:    make(map[string]*BuiltRuntime),
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
	m.mu.RUnlock()

	if builder == nil {
		return nil, fmt.Errorf("agent builder is required")
	}
	if providerID == "" {
		return nil, fmt.Errorf("balda provider is not configured")
	}

	runtime, err := builder.BuildRuntimeWithMCPServerIDs(
		ctx,
		providerID,
		workingDir,
		nil,
		extraMCPServerIDs,
	)
	if err != nil {
		m.logger.Error().Err(err).Str("agent", providerID).Msg("failed to build balda provider runtime")
		return nil, err
	}

	m.mu.Lock()
	if existing := m.runtime; existing != nil {
		m.mu.Unlock()
		if runtime != nil {
			if closeErr := closeRuntimeAgent(runtime.Agent); closeErr != nil {
				m.logger.Warn().Err(closeErr).Str("agent", providerID).Msg("failed to close duplicate balda provider runtime")
			}
		}
		return existing, nil
	}
	m.runtime = runtime
	m.mu.Unlock()

	m.logger.Info().Str("agent", providerID).Msg("balda provider runtime ready")
	return runtime, nil
}

// RuntimeForSession builds a provider runtime whose bundled MCP URL is bound
// to one exact authenticated Balda locator. It intentionally uses a distinct
// ACP process/runtime because the provider's MCP headers are static for the
// lifetime of an ACP process and cannot be safely mutated between turns.
func (m *RuntimeManager) RuntimeForSession(ctx context.Context, request SessionRuntimeRequest) (*BuiltRuntime, error) {
	if m == nil {
		return nil, fmt.Errorf("balda runtime manager is required")
	}
	m.mu.RLock()
	builder := m.builder
	providerID := strings.TrimSpace(m.providerID)
	workingDir := strings.TrimSpace(request.WorkspaceDir)
	binder := m.sessionMCPBinder
	registry := m.mcpRegistry
	if workingDir == "" {
		workingDir = m.workingDir
	}
	m.mu.RUnlock()
	if binder == nil || registry == nil {
		return m.Runtime(ctx)
	}
	if status, ok := binder.(sessionMCPBinderStatus); ok && !status.SessionMCPEnabled() {
		return m.Runtime(ctx)
	}
	if builder == nil {
		return nil, fmt.Errorf("agent builder is required")
	}
	if providerID == "" {
		return nil, fmt.Errorf("balda provider is not configured")
	}
	binding, err := binder.BindSession(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("bind session MCP context: %w", err)
	}
	if strings.TrimSpace(binding.ID) == "" || strings.TrimSpace(binding.Config.URL) == "" {
		if binding.Release != nil {
			_ = binding.Release()
		}
		return nil, fmt.Errorf("session MCP binding is incomplete")
	}
	registry.Set(binding.ID, binding.Config)
	runtime, err := builder.BuildRuntimeWithMCPServerIDs(ctx, providerID, workingDir, nil, []string{binding.ID})
	if err != nil {
		registry.Delete(binding.ID)
		if binding.Release != nil {
			_ = binding.Release()
		}
		return nil, err
	}
	var once sync.Once
	closeScoped := func() error {
		var closeErr error
		once.Do(func() {
			registry.Delete(binding.ID)
			if binding.Release != nil {
				closeErr = errors.Join(closeErr, binding.Release())
			}
			closeErr = errors.Join(closeErr, closeRuntimeAgent(runtime.Agent))
			m.mu.Lock()
			delete(m.scopedRuntimes, binding.ID)
			m.mu.Unlock()
		})
		return closeErr
	}
	runtime.Close = closeScoped
	m.mu.Lock()
	if m.scopedRuntimes == nil {
		m.scopedRuntimes = make(map[string]*BuiltRuntime)
	}
	m.scopedRuntimes[binding.ID] = runtime
	m.mu.Unlock()
	return runtime, nil
}

// PrepareGoalRun creates an isolated GoalKeeper execution context using the
// app-scoped provider runtime. Workspace-enabled runs get a per-task worktree;
// direct-mode runs use the app working directory and skip export.
func (m *RuntimeManager) PrepareGoalRun(
	ctx context.Context,
	cfg GoalRunConfig,
) (*GoalRun, error) {
	base, err := m.childRuntimeBase(ctx)
	if err != nil {
		return nil, err
	}
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
	r, err := base.runner(workflow, "goal")
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

func (m *RuntimeManager) childRuntimeBase(ctx context.Context) (childRuntimeBase, error) {
	runtime, err := m.Runtime(ctx)
	if err != nil {
		return childRuntimeBase{}, err
	}

	m.mu.RLock()
	base := childRuntimeBase{
		runtime:           runtime,
		builder:           m.builder,
		providerID:        strings.TrimSpace(m.providerID),
		workingDir:        strings.TrimSpace(m.workingDir),
		extraMCPServerIDs: append([]string(nil), m.baldaMCPServerIDs...),
	}
	m.mu.RUnlock()

	if base.builder == nil {
		return childRuntimeBase{}, fmt.Errorf("agent builder is required")
	}
	if base.providerID == "" {
		return childRuntimeBase{}, fmt.Errorf("balda provider is not configured")
	}
	return base, nil
}

func (b childRuntimeBase) runner(agent adkagent.Agent, label string) (*runner.Runner, error) {
	r, err := runner.New(runner.Config{
		AppName:        b.runtime.AppName,
		Agent:          agent,
		SessionService: b.runtime.SessionSvc,
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

	r, err := b.runner(agent, "goal commit message")
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
	scoped := make([]*BuiltRuntime, 0, len(m.scopedRuntimes))
	for _, item := range m.scopedRuntimes {
		scoped = append(scoped, item)
	}
	m.scopedRuntimes = make(map[string]*BuiltRuntime)
	m.runtime = nil
	m.mu.Unlock()
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
		errs = append(errs, closeRuntimeAgent(runtime.Agent))
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
