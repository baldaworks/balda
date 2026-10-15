package sessionturnapp

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

// HookRunner is the turn executor's consuming port for managed hook decisions.
type HookRunner interface {
	Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error)
}

// SetHooks installs the immutable hook execution port before runtime starts.
func (s *TurnExecutionService) SetHooks(hooks HookRunner) { s.hooks = hooks }

func (s *TurnExecutionService) submitHooks(ctx context.Context, req ExecutionRequest) (string, error) {
	if s.hooks == nil {
		return "", nil
	}
	result, err := s.hooks.Run(ctx, hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: req.SessionID, AgentSessionID: req.AgentSessionID, CWD: req.WorkspaceDir, Source: req.TurnSource, Prompt: req.Text})
	if err != nil {
		return "", fmt.Errorf("prompt submission hook: %w", err)
	}
	return result.AdditionalContext, nil
}

func (s *TurnExecutionService) stopHooks(ctx context.Context, req ExecutionRequest, response, status string) error {
	if s.hooks == nil {
		return nil
	}
	_, err := s.hooks.Run(ctx, hookcmd.Event{Name: hookcmd.Stop, SessionID: req.SessionID, AgentSessionID: req.AgentSessionID, CWD: req.WorkspaceDir, Source: status, Prompt: req.Text, Response: response})
	if err != nil {
		return fmt.Errorf("turn completion hook: %w", err)
	}
	return nil
}
