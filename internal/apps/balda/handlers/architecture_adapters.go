package handlers

import (
	"context"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/sessionapp"
)

type sessionWorkspaceManagerAdapter struct {
	manager *baldaagent.WorkspaceManager
}

func (a sessionWorkspaceManagerAdapter) CanonicalWorkspaceDir(key string) string {
	return sessionapp.SessionWorkspaceManagerAdapter{Manager: a.manager}.CanonicalWorkspaceDir(key)
}

func (a sessionWorkspaceManagerAdapter) ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (baldasession.EnsureWorkspaceResult, error) {
	return sessionapp.SessionWorkspaceManagerAdapter{Manager: a.manager}.ForceRemountCanonicalWorkspace(ctx, key, branchName)
}

func (a sessionWorkspaceManagerAdapter) EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (baldasession.EnsureWorkspaceResult, error) {
	return sessionapp.SessionWorkspaceManagerAdapter{Manager: a.manager}.EnsureWorkspace(ctx, key, branchName, existingPath)
}

func (a sessionWorkspaceManagerAdapter) Import(ctx context.Context, workspaceDir string) error {
	return sessionapp.SessionWorkspaceManagerAdapter{Manager: a.manager}.Import(ctx, workspaceDir)
}

func (a sessionWorkspaceManagerAdapter) Export(ctx context.Context, workspaceDir, branchName, commitMessage string) error {
	return sessionapp.SessionWorkspaceManagerAdapter{Manager: a.manager}.Export(ctx, workspaceDir, branchName, commitMessage)
}

func (a sessionWorkspaceManagerAdapter) CleanupWorkspace(ctx context.Context, workspaceDir string) error {
	return sessionapp.SessionWorkspaceManagerAdapter{Manager: a.manager}.CleanupWorkspace(ctx, workspaceDir)
}
