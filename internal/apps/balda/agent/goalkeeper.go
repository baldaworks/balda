package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/goalkeeper"
	"github.com/normahq/runtime/agentfactory"
	adkagent "google.golang.org/adk/agent"
)

const (
	goalkeeperWorkerName           = "GoalkeeperWorker"
	goalkeeperValidatorName        = "GoalkeeperValidator"
	goalkeeperWorkerOutputStateKey = "app:goalkeeper_worker_output"
)

// GoalkeeperBuildConfig configures the Balda Goalkeeper workflow agent.
type GoalkeeperBuildConfig struct {
	ProviderID          string
	SessionID           string
	BranchName          string
	WorkspaceDir        string
	MaxIterations       uint
	BundledMCPServerIDs []string
	ExtraMCPServerIDs   []string
}

// BuildGoalkeeperWorkflow builds the copied Goalkeeper worker -> validator loop
// using Balda's configured provider for both child agents.
func (b *Builder) BuildGoalkeeperWorkflow(ctx context.Context, cfg GoalkeeperBuildConfig) (adkagent.Agent, error) {
	if b == nil || b.factory == nil {
		return nil, fmt.Errorf("agent builder is required")
	}
	providerID := strings.TrimSpace(cfg.ProviderID)
	if providerID == "" {
		return nil, fmt.Errorf("balda provider is not configured")
	}
	sessionID := strings.TrimSpace(cfg.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	workspaceDir := strings.TrimSpace(cfg.WorkspaceDir)
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	if cfg.MaxIterations == 0 {
		return nil, fmt.Errorf("max iterations must be greater than zero")
	}

	repoBranchAtStart := b.currentRepoBranch(ctx)
	sessionBranch := strings.TrimSpace(cfg.BranchName)
	if sessionBranch == "" {
		sessionBranch = fmt.Sprintf("norma/balda/%s", sessionID)
	}
	mcpServerIDs := b.buildAgentMCPServerIDs(providerID, cfg.BundledMCPServerIDs, cfg.ExtraMCPServerIDs)

	worker, err := b.buildGoalkeeperChildAgent(ctx, goalkeeperChildAgentConfig{
		ProviderID:        providerID,
		Name:              goalkeeperWorkerName,
		Description:       "Goalkeeper worker agent",
		SessionID:         sessionID,
		SessionBranch:     sessionBranch,
		WorkspaceDir:      workspaceDir,
		RepoBranchAtStart: repoBranchAtStart,
		RoleInstruction:   goalkeeperWorkerInstruction(),
		OutputKey:         goalkeeperWorkerOutputStateKey,
		MCPServerIDs:      mcpServerIDs,
	})
	if err != nil {
		return nil, err
	}

	validator, err := b.buildGoalkeeperChildAgent(ctx, goalkeeperChildAgentConfig{
		ProviderID:        providerID,
		Name:              goalkeeperValidatorName,
		Description:       "Goalkeeper validator agent",
		SessionID:         sessionID,
		SessionBranch:     sessionBranch,
		WorkspaceDir:      workspaceDir,
		RepoBranchAtStart: repoBranchAtStart,
		RoleInstruction:   goalkeeperValidatorInstruction(goalkeeperWorkerOutputStateKey),
		MCPServerIDs:      mcpServerIDs,
	})
	if err != nil {
		_ = closeRuntimeAgent(worker)
		return nil, err
	}

	workflow, err := goalkeeper.New(goalkeeper.NewOptions(
		worker,
		validator,
		goalkeeper.WithMaxIterations(cfg.MaxIterations),
	))
	if err != nil {
		_ = closeRuntimeAgent(worker)
		_ = closeRuntimeAgent(validator)
		return nil, err
	}
	return workflow, nil
}

type goalkeeperChildAgentConfig struct {
	ProviderID        string
	Name              string
	Description       string
	SessionID         string
	SessionBranch     string
	WorkspaceDir      string
	RepoBranchAtStart string
	RoleInstruction   string
	OutputKey         string
	MCPServerIDs      []string
}

func (b *Builder) buildGoalkeeperChildAgent(ctx context.Context, cfg goalkeeperChildAgentConfig) (adkagent.Agent, error) {
	req := b.goalkeeperChildBuildRequest(cfg)
	ag, err := b.factory.Build(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("creating %s from provider %q: %w", cfg.Name, cfg.ProviderID, err)
	}
	return ag, nil
}

func (b *Builder) goalkeeperChildBuildRequest(cfg goalkeeperChildAgentConfig) agentfactory.BuildRequest {
	baseInstruction := b.buildBaldaInstruction(
		cfg.SessionID,
		"telegram",
		cfg.ProviderID,
		cfg.SessionBranch,
		cfg.WorkspaceDir,
		cfg.RepoBranchAtStart,
	)
	return agentfactory.BuildRequest{
		AgentID:          cfg.ProviderID,
		Name:             cfg.Name,
		Description:      cfg.Description,
		WorkingDirectory: cfg.WorkspaceDir,
		Instruction:      joinGoalkeeperInstructions(baseInstruction, cfg.RoleInstruction),
		OutputKey:        strings.TrimSpace(cfg.OutputKey),
		MCPServerIDs:     cfg.MCPServerIDs,
	}
}

func joinGoalkeeperInstructions(baseInstruction, roleInstruction string) string {
	parts := []string{
		strings.TrimSpace(baseInstruction),
		strings.TrimSpace(roleInstruction),
	}
	var out []string
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "\n\n")
}

func goalkeeperWorkerInstruction() string {
	return strings.Join([]string{
		"You are the Goalkeeper worker agent.",
		"You receive one user goal as plain text.",
		"Use the available goal and context.",
		"Do the requested work in the current working directory.",
		"Return a concise plain-text summary of what changed and what evidence supports it.",
		"Run only lightweight sanity checks directly relevant to the work unless the goal asks for broader verification.",
	}, "\n")
}

func goalkeeperValidatorInstruction(workerOutputStateKey string) string {
	return strings.Join([]string{
		"You are the Goalkeeper validator agent.",
		"Validate the prior worker result against the original user goal using the shared ADK session context.",
		fmt.Sprintf("The worker final visible result is available in session state as `{%s?}`.", strings.TrimSpace(workerOutputStateKey)),
		"Inspect the current working directory as needed.",
		"Do not intentionally mutate files or continue the worker's implementation work.",
		"Start with exactly `verdict: pass` or `verdict: fail`.",
		"`verdict: pass` means the goal was reached.",
		"`verdict: fail` means the goal was not reached.",
		"Then provide brief evidence and a concise final summary.",
	}, "\n")
}
