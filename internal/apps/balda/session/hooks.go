package session

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

// HookRunner is the lifecycle package's consuming port for managed hooks.
type HookRunner interface {
	Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error)
}

func (m *Manager) startSessionHooks(ctx context.Context, ts *TopicSession, provider, source string) error {
	if m.hooks == nil {
		return nil
	}
	cwd, err := filepath.Abs(ts.workspaceDir)
	if err != nil {
		return fmt.Errorf("session hook workspace: %w", err)
	}
	_, err = m.hooks.Run(ctx, hookcmd.Event{Name: hookcmd.SessionStart, SessionID: ts.sessionID, AgentSessionID: ts.agentSessionID, CWD: cwd, Provider: provider, Source: source})
	if err != nil {
		return fmt.Errorf("session start hook: %w", err)
	}
	ts.hooksStarted = true
	return nil
}

func (m *Manager) endSessionHooks(ctx context.Context, ts *TopicSession) {
	if m.hooks == nil || ts == nil || !ts.hooksStarted || ts.hooksEnded {
		return
	}
	ts.hooksEnded = true
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	cwd, err := filepath.Abs(ts.workspaceDir)
	if err == nil {
		_, err = m.hooks.Run(endCtx, hookcmd.Event{Name: hookcmd.SessionEnd, SessionID: ts.sessionID, AgentSessionID: ts.agentSessionID, CWD: cwd, Provider: m.getProviderName(), Source: "close"})
	}
	if err != nil {
		m.logger.Warn().Str("session_id", ts.sessionID).Msg("session end hook failed; continuing cleanup")
	}
}
