package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"

	"github.com/normahq/norma/pkg/goalkeeper"
	"github.com/normahq/norma/pkg/runtime/agentfactory"
	adkagent "google.golang.org/adk/agent"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	goalWorkerName           = "GoalkeeperWorker"
	goalValidatorName        = "GoalkeeperValidator"
	goalWorkerOutputStateKey = "goalkeeper_worker_output"
)

// GoalBuildConfig configures Balda's /goal work-validation agent.
type GoalBuildConfig struct {
	ProviderID          string
	SessionID           string
	BranchName          string
	WorkspaceDir        string
	MaxIterations       uint
	BundledMCPServerIDs []string
	ExtraMCPServerIDs   []string
}

// BuildGoalWorkflow builds Norma's goal work-validation loop using
// Balda's configured provider for both child agents.
func (b *Builder) BuildGoalWorkflow(ctx context.Context, cfg GoalBuildConfig) (adkagent.Agent, error) {
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

	worker, err := b.buildGoalChildAgent(ctx, goalChildAgentConfig{
		ProviderID:        providerID,
		Name:              goalWorkerName,
		Description:       "Goalkeeper worker agent",
		SessionID:         sessionID,
		SessionBranch:     sessionBranch,
		WorkspaceDir:      workspaceDir,
		RepoBranchAtStart: repoBranchAtStart,
		RoleInstruction:   goalWorkerInstruction(),
		OutputKey:         goalWorkerOutputStateKey,
		MCPServerIDs:      mcpServerIDs,
	})
	if err != nil {
		return nil, err
	}

	rawValidator, err := b.buildGoalChildAgent(ctx, goalChildAgentConfig{
		ProviderID:        providerID,
		Name:              goalValidatorName,
		Description:       "Goalkeeper validator agent",
		SessionID:         sessionID,
		SessionBranch:     sessionBranch,
		WorkspaceDir:      workspaceDir,
		RepoBranchAtStart: repoBranchAtStart,
		RoleInstruction:   goalValidatorInstruction(),
		MCPServerIDs:      mcpServerIDs,
	})
	if err != nil {
		_ = closeRuntimeAgent(worker)
		return nil, err
	}
	validator, err := wrapGoalValidatorWithWorkerOutput(rawValidator, goalWorkerOutputStateKey)
	if err != nil {
		_ = closeRuntimeAgent(worker)
		_ = closeRuntimeAgent(rawValidator)
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
	return &closableGoalWorkflow{Agent: workflow, closers: goalChildClosers(worker, validator)}, nil
}

type goalChildAgentConfig struct {
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

func (b *Builder) buildGoalChildAgent(ctx context.Context, cfg goalChildAgentConfig) (adkagent.Agent, error) {
	req := b.goalChildBuildRequest(cfg)
	ag, err := b.factory.Build(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("creating %s from provider %q: %w", cfg.Name, cfg.ProviderID, err)
	}
	return ag, nil
}

func (b *Builder) goalChildBuildRequest(cfg goalChildAgentConfig) agentfactory.BuildRequest {
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
		Instruction:      joinGoalInstructions(baseInstruction, cfg.RoleInstruction),
		OutputKey:        strings.TrimSpace(cfg.OutputKey),
		MCPServerIDs:     cfg.MCPServerIDs,
	}
}

func joinGoalInstructions(baseInstruction, roleInstruction string) string {
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

func goalWorkerInstruction() string {
	return strings.Join([]string{
		"You are the Goalkeeper worker agent.",
		"You receive one user goal as plain text.",
		"Use the available goal and context.",
		"Do the requested work in the current working directory.",
		"Prefer direct execution over clarification when execution is possible.",
		"Ask clarifying questions only when execution is blocked by missing critical information.",
		"Return a concise plain-text summary of what changed and what evidence supports it.",
		"Run only lightweight sanity checks directly relevant to the work unless the goal asks for broader verification.",
	}, "\n")
}

func goalValidatorInstruction() string {
	return strings.Join([]string{
		"You are the Goalkeeper validator agent.",
		"Validate the prior worker result against the original user goal using the shared ADK session context.",
		"Inspect the current working directory as needed.",
		"Do not intentionally mutate files or continue the worker's implementation work.",
		"Start with exactly `verdict: pass` or `verdict: fail`.",
		"`verdict: pass` means the goal was reached.",
		"`verdict: fail` means the goal was not reached.",
		"Then provide brief evidence and a concise final summary.",
	}, "\n")
}

func wrapGoalValidatorWithWorkerOutput(inner adkagent.Agent, workerOutputStateKey string) (adkagent.Agent, error) {
	key := strings.TrimSpace(workerOutputStateKey)
	if key == "" {
		return nil, fmt.Errorf("worker output state key is required")
	}

	base, err := adkagent.New(adkagent.Config{
		Name:        inner.Name(),
		Description: inner.Description(),
		SubAgents:   inner.SubAgents(),
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				prompt := buildGoalValidatorPrompt(ctx.UserContent(), sessionStateString(ctx, key))
				wrappedCtx := goalkeeperUserContentContext{
					InvocationContext: ctx,
					userContent:       genai.NewContentFromText(prompt, genai.RoleUser),
				}
				for ev, err := range inner.Run(wrappedCtx) {
					if !yield(ev, err) {
						return
					}
					if err != nil {
						return
					}
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}
	closer, ok := inner.(io.Closer)
	if !ok {
		return base, nil
	}
	return goalValidatorWrapper{Agent: base, closer: closer}, nil
}

type goalValidatorWrapper struct {
	adkagent.Agent
	closer io.Closer
}

func (w goalValidatorWrapper) Close() error {
	if w.closer == nil {
		return nil
	}
	return w.closer.Close()
}

func sessionStateString(ctx adkagent.InvocationContext, key string) string {
	if ctx == nil || ctx.Session() == nil {
		return ""
	}
	value, err := ctx.Session().State().Get(key)
	if err != nil {
		if errors.Is(err, adksession.ErrStateKeyNotExist) {
			return ""
		}
		return ""
	}
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func buildGoalValidatorPrompt(userContent *genai.Content, workerOutput string) string {
	goal := visibleContentText(userContent)
	workerOutput = strings.TrimSpace(workerOutput)
	if goal == "" {
		goal = "Goal:"
	}
	if workerOutput == "" {
		return goal + "\n\nWorker result:\n(none)"
	}
	return goal + "\n\nWorker result:\n" + workerOutput
}

func visibleContentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var parts []string
	for _, part := range content.Parts {
		if part == nil || part.Thought {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

type goalkeeperUserContentContext struct {
	adkagent.InvocationContext
	userContent *genai.Content
}

func (c goalkeeperUserContentContext) UserContent() *genai.Content {
	return c.userContent
}

type closableGoalWorkflow struct {
	adkagent.Agent
	closers []io.Closer
	once    sync.Once
	err     error
}

func (w *closableGoalWorkflow) Close() error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		errs := make([]error, 0, len(w.closers)+1)
		if err := closeRuntimeAgent(w.Agent); err != nil {
			errs = append(errs, err)
		}
		for _, closer := range w.closers {
			if closer == nil {
				continue
			}
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		w.err = errors.Join(errs...)
	})
	return w.err
}

func goalChildClosers(agents ...adkagent.Agent) []io.Closer {
	closers := make([]io.Closer, 0, len(agents))
	for _, ag := range agents {
		closer, ok := ag.(io.Closer)
		if ok {
			closers = append(closers, closer)
		}
	}
	return closers
}
