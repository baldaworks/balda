package session

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type WorkspaceService interface {
	Import(ctx context.Context, sessionID string) error
	Export(ctx context.Context, sessionID string, commitMessage string) error
}

type workspaceService struct {
	manager *Manager
	logger  zerolog.Logger
}

func NewWorkspaceService(manager *Manager) WorkspaceService {
	return &workspaceService{
		manager: manager,
		logger:  log.With().Str("component", "workspace.mcp").Logger(),
	}
}

func (s *workspaceService) Import(ctx context.Context, sessionID string) error {
	s.logger.Info().Str("session_id", sessionID).Msg("MCP: Import called")
	if !s.manager.workspaceEnabled {
		return fmt.Errorf("workspace mode is disabled")
	}

	info, err := s.manager.GetSessionInfo(ctx, sessionID)
	if err != nil {
		s.logger.Error().Err(err).Str("session_id", sessionID).Msg("MCP: Import failed — session not found")
		return err
	}

	if info.WorkspaceDir == "" {
		return fmt.Errorf("session %q has no workspace", sessionID)
	}

	if err := s.manager.workspaces.Import(ctx, info.WorkspaceDir); err != nil {
		s.logger.Error().Err(err).Str("session_id", sessionID).Msg("MCP: Import failed")
		return err
	}

	s.logger.Info().Str("session_id", sessionID).Msg("MCP: Import succeeded")
	return nil
}

func (s *workspaceService) Export(ctx context.Context, sessionID string, commitMessage string) error {
	s.logger.Info().Str("session_id", sessionID).Str("message", commitMessage).Msg("MCP: Export called")
	if !s.manager.workspaceEnabled {
		return fmt.Errorf("workspace mode is disabled")
	}

	info, err := s.manager.GetSessionInfo(ctx, sessionID)
	if err != nil {
		s.logger.Error().Err(err).Str("session_id", sessionID).Msg("MCP: Export failed — session not found")
		return err
	}

	if info.WorkspaceDir == "" {
		return fmt.Errorf("session %q has no workspace", sessionID)
	}
	if info.BranchName == "" {
		return fmt.Errorf("session %q has no workspace branch", sessionID)
	}

	if err := s.manager.workspaces.Export(ctx, info.WorkspaceDir, info.BranchName, commitMessage); err != nil {
		s.logger.Error().Err(err).Str("session_id", sessionID).Msg("MCP: Export failed")
		return err
	}

	s.logger.Info().Str("session_id", sessionID).Msg("MCP: Export succeeded")
	return nil
}
