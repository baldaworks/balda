package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/normahq/balda/internal/apps/balda/shutdown"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
)

// RuntimeManager owns the single app-scoped balda provider runtime.
type RuntimeManager struct {
	builder           *Builder
	providerID        string
	workingDir        string
	baldaMCPServerIDs []string
	logger            zerolog.Logger

	mu      sync.RWMutex
	runtime *BuiltRuntime
}

// RuntimeManagerParams wires RuntimeManager dependencies.
type RuntimeManagerParams struct {
	fx.In

	LC                fx.Lifecycle
	Builder           *Builder
	BaldaProviderID   string `name:"balda_provider"`
	WorkingDir        string
	BaldaMCPServerIDs []string `name:"balda_mcp_servers"`
	Logger            zerolog.Logger
}

// GoalkeeperRuntimeConfig configures a per-run Goalkeeper workflow runtime.
type GoalkeeperRuntimeConfig struct {
	SessionID     string
	BranchName    string
	WorkspaceDir  string
	MaxIterations uint
}

// TaskAgentRuntimeConfig configures one per-task role agent.
type TaskAgentRuntimeConfig struct {
	SessionID    string
	BranchName   string
	WorkspaceDir string
	Role         string
}

// GoalkeeperRuntime owns the per-run Goalkeeper workflow runner and agents.
type GoalkeeperRuntime struct {
	Agent  adkagent.Agent
	Runner *runner.Runner
}

// TaskAgentRuntime owns a short-lived task role runner and provider agent.
type TaskAgentRuntime struct {
	Agent  adkagent.Agent
	Runner *runner.Runner
}

type childRuntimeBase struct {
	runtime           *BuiltRuntime
	builder           *Builder
	providerID        string
	workingDir        string
	extraMCPServerIDs []string
}

// Close releases child provider agents created for the workflow.
func (r *GoalkeeperRuntime) Close() error {
	if r == nil {
		return nil
	}
	return closeRuntimeAgent(r.Agent)
}

// Close releases the provider agent created for one task role run.
func (r *TaskAgentRuntime) Close() error {
	if r == nil {
		return nil
	}
	return closeRuntimeAgent(r.Agent)
}

// NewRuntimeManager creates the app-scoped balda runtime owner.
func NewRuntimeManager(p RuntimeManagerParams) *RuntimeManager {
	m := &RuntimeManager{
		builder:           p.Builder,
		providerID:        strings.TrimSpace(p.BaldaProviderID),
		workingDir:        strings.TrimSpace(p.WorkingDir),
		baldaMCPServerIDs: append([]string(nil), p.BaldaMCPServerIDs...),
		logger:            p.Logger.With().Str("component", "balda.runtime_manager").Logger(),
	}

	p.LC.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return m.close()
		},
	})

	return m
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
		if closeErr := closeBuiltRuntime(runtime); closeErr != nil {
			m.logger.Warn().Err(closeErr).Str("agent", providerID).Msg("failed to close duplicate balda provider runtime")
		}
		return existing, nil
	}
	m.runtime = runtime
	m.mu.Unlock()

	m.logger.Info().Str("agent", providerID).Msg("balda provider runtime ready")
	return runtime, nil
}

// BuildGoalkeeperRuntime creates a per-run Goalkeeper workflow using the
// app-scoped session service so the workflow runs in the current Balda session.
func (m *RuntimeManager) BuildGoalkeeperRuntime(
	ctx context.Context,
	cfg GoalkeeperRuntimeConfig,
) (*GoalkeeperRuntime, error) {
	base, err := m.childRuntimeBase(ctx)
	if err != nil {
		return nil, err
	}

	workflow, err := base.builder.BuildGoalkeeperWorkflow(ctx, GoalkeeperBuildConfig{
		ProviderID:        base.providerID,
		SessionID:         cfg.SessionID,
		BranchName:        cfg.BranchName,
		WorkspaceDir:      base.workspaceDir(cfg.WorkspaceDir),
		MaxIterations:     cfg.MaxIterations,
		ExtraMCPServerIDs: base.extraMCPServerIDs,
	})
	if err != nil {
		return nil, err
	}
	r, err := base.runner(workflow, "goalkeeper")
	if err != nil {
		_ = closeRuntimeAgent(workflow)
		return nil, err
	}
	return &GoalkeeperRuntime{
		Agent:  workflow,
		Runner: r,
	}, nil
}

// BuildTaskAgentRuntime creates a short-lived role agent backed by the balda provider.
func (m *RuntimeManager) BuildTaskAgentRuntime(
	ctx context.Context,
	cfg TaskAgentRuntimeConfig,
) (*TaskAgentRuntime, error) {
	base, err := m.childRuntimeBase(ctx)
	if err != nil {
		return nil, err
	}

	ag, err := base.builder.BuildTaskRoleAgent(ctx, TaskRoleBuildConfig{
		ProviderID:        base.providerID,
		SessionID:         cfg.SessionID,
		BranchName:        cfg.BranchName,
		WorkspaceDir:      base.workspaceDir(cfg.WorkspaceDir),
		Role:              cfg.Role,
		ExtraMCPServerIDs: base.extraMCPServerIDs,
	})
	if err != nil {
		return nil, err
	}
	r, err := base.runner(ag, "task role")
	if err != nil {
		_ = closeRuntimeAgent(ag)
		return nil, err
	}
	return &TaskAgentRuntime{Agent: ag, Runner: r}, nil
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

func (b childRuntimeBase) workspaceDir(raw string) string {
	if workspaceDir := strings.TrimSpace(raw); workspaceDir != "" {
		return workspaceDir
	}
	return b.workingDir
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

func (m *RuntimeManager) close() error {
	m.mu.Lock()
	runtime := m.runtime
	m.runtime = nil
	m.mu.Unlock()
	return closeBuiltRuntime(runtime)
}

func closeBuiltRuntime(runtime *BuiltRuntime) error {
	if runtime == nil {
		return nil
	}
	return closeRuntimeAgent(runtime.Agent)
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
