package session

import (
	"context"
	"errors"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
)

type testWorkspaceManager struct {
	inner *baldaagent.WorkspaceManager
}

func newTestWorkspaceManager(workingDir, stateDir, baseBranch string) WorkspaceManager {
	return testWorkspaceManager{inner: baldaagent.NewWorkspaceManagerWithSessionsDir(workingDir, stateDir, baseBranch, "")}
}

func (m testWorkspaceManager) CanonicalWorkspaceDir(key string) string {
	return m.inner.CanonicalWorkspaceDir(key)
}

func (m testWorkspaceManager) ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (EnsureWorkspaceResult, error) {
	result, err := m.inner.ForceRemountCanonicalWorkspace(ctx, key, branchName)
	return EnsureWorkspaceResult{Dir: result.Dir, SyncSkipped: result.SyncSkipped}, translateTestWorkspaceError(err)
}

func (m testWorkspaceManager) EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (EnsureWorkspaceResult, error) {
	result, err := m.inner.EnsureWorkspace(ctx, key, branchName, existingPath)
	return EnsureWorkspaceResult{Dir: result.Dir, SyncSkipped: result.SyncSkipped}, translateTestWorkspaceError(err)
}

func (m testWorkspaceManager) Import(ctx context.Context, workspaceDir string) error {
	return m.inner.Import(ctx, workspaceDir)
}

func (m testWorkspaceManager) Export(ctx context.Context, workspaceDir, branchName, commitMessage string) error {
	return m.inner.Export(ctx, workspaceDir, branchName, commitMessage)
}

func (m testWorkspaceManager) CleanupWorkspace(ctx context.Context, workspaceDir string) error {
	return m.inner.CleanupWorkspace(ctx, workspaceDir)
}

func translateTestWorkspaceError(err error) error {
	if errors.Is(err, baldaagent.ErrWorkspaceCollision) {
		return errors.Join(ErrWorkspaceCollision, err)
	}
	return err
}
