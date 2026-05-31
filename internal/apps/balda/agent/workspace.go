package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/normahq/balda/internal/git"
	"github.com/rs/zerolog/log"
)

const defaultWorkspaceSessionsDir = "sessions"

// ErrWorkspaceCollision marks workspace path/branch collisions.
var ErrWorkspaceCollision = errors.New("workspace collision")

// WorkspaceManager manages git worktrees for balda sessions.
type WorkspaceManager struct {
	workingDir    string
	workspacesDir string
	baseBranch    string
}

// EnsureWorkspaceResult describes the mounted workspace and whether restore
// had to skip base-branch sync after an auto-sync failure.
type EnsureWorkspaceResult struct {
	Dir         string
	SyncSkipped bool
}

// NewWorkspaceManager creates a WorkspaceManager for the given working directory.
func NewWorkspaceManager(workingDir, stateDir, baseBranch string) *WorkspaceManager {
	return NewWorkspaceManagerWithSessionsDir(workingDir, stateDir, baseBranch, "")
}

// NewWorkspaceManagerWithSessionsDir creates a WorkspaceManager with a custom sessions dir name.
func NewWorkspaceManagerWithSessionsDir(workingDir, stateDir, baseBranch, sessionsDir string) *WorkspaceManager {
	dirName := strings.TrimSpace(sessionsDir)
	if dirName == "" {
		dirName = defaultWorkspaceSessionsDir
	}
	return &WorkspaceManager{
		workingDir:    workingDir,
		workspacesDir: filepath.Join(strings.TrimSpace(stateDir), dirName),
		baseBranch:    strings.TrimSpace(baseBranch),
	}
}

// CanonicalWorkspaceDir returns the canonical workspace directory path for a session key.
func (m *WorkspaceManager) CanonicalWorkspaceDir(key string) string {
	return filepath.Join(m.workspacesDir, key)
}

// ForceRemountCanonicalWorkspace forcefully clears the canonical workspace path and remounts it.
func (m *WorkspaceManager) ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (EnsureWorkspaceResult, error) {
	workspaceDir := m.CanonicalWorkspaceDir(key)
	if err := m.forceCleanupWorkspacePath(ctx, workspaceDir); err != nil {
		return EnsureWorkspaceResult{Dir: workspaceDir}, err
	}
	return m.EnsureWorkspace(ctx, key, branchName, workspaceDir)
}

// EnsureWorkspace creates or returns an existing workspace directory.
// If an existing session branch is available, balda attempts to sync it to base.
// When that sync fails, balda recreates a clean worktree on the session branch
// and continues without syncing base so restart can still succeed.
func (m *WorkspaceManager) EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (EnsureWorkspaceResult, error) {
	result := EnsureWorkspaceResult{}
	if err := os.MkdirAll(m.workspacesDir, 0o755); err != nil {
		return result, fmt.Errorf("create workspaces dir: %w", err)
	}

	workspaceDir := existingPath
	if strings.TrimSpace(workspaceDir) == "" {
		workspaceDir = filepath.Join(m.workspacesDir, key)
	}
	result.Dir = workspaceDir

	branchName = strings.TrimSpace(branchName)
	branchExists := branchName != "" && git.BranchExists(ctx, m.workingDir, branchName)

	if fi, err := os.Stat(workspaceDir); err == nil && fi.IsDir() {
		if err := validateWorkspaceBranch(ctx, workspaceDir, branchName); err != nil {
			return result, err
		}
		if branchExists {
			// Workspace already exists — import latest base when we are resuming an existing branch.
			if err := m.Import(ctx, workspaceDir); err != nil {
				return m.recreateWorkspaceWithoutSync(ctx, workspaceDir, branchName, err)
			}
		}
		return result, nil
	} else if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("stat workspace dir %q: %w", workspaceDir, err)
	}

	baseBranch := ""
	if !branchExists {
		var err error
		baseBranch, err = m.resolvedBaseBranch(ctx)
		if err != nil {
			return result, err
		}
	}

	if _, err := git.MountWorktree(ctx, m.workingDir, workspaceDir, branchName, baseBranch); err != nil {
		return result, fmt.Errorf("mount worktree: %w", err)
	}

	if !branchExists {
		return result, nil
	}
	if err := m.Import(ctx, workspaceDir); err != nil {
		return m.recreateWorkspaceWithoutSync(ctx, workspaceDir, branchName, err)
	}

	return result, nil
}

// Import syncs a workspace branch onto the configured base branch.
func (m *WorkspaceManager) Import(ctx context.Context, workspaceDir string) error {
	statusOut, err := git.GitRunCmdOutput(ctx, workspaceDir, "git", "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("read workspace status: %w", err)
	}

	status := strings.TrimSpace(statusOut)
	if status != "" {
		changedEntries := strings.Count(status, "\n") + 1
		log.Warn().
			Str("workspace", workspaceDir).
			Int("changed_entries", changedEntries).
			Msg("discarding dirty workspace changes before import")

		if err := git.GitRunCmdErr(ctx, workspaceDir, "git", "reset", "--hard"); err != nil {
			return fmt.Errorf("reset dirty workspace before import: %w", err)
		}
		if err := git.GitRunCmdErr(ctx, workspaceDir, "git", "clean", "-fd"); err != nil {
			return fmt.Errorf("clean dirty workspace before import: %w", err)
		}
	}

	baseRef, err := m.resolvedBaseBranch(ctx)
	if err != nil {
		return err
	}

	if err := git.GitRunCmdErr(ctx, workspaceDir, "git", "rebase", baseRef); err != nil {
		// Abort rebase on failure so workspace stays clean
		_ = git.GitRunCmdErr(ctx, workspaceDir, "git", "rebase", "--abort")
		return fmt.Errorf("rebase workspace onto %s: %w", baseRef, err)
	}
	log.Info().Str("workspace", workspaceDir).Str("base_ref", baseRef).Msg("workspace synced to base ref")
	return nil
}

func (m *WorkspaceManager) resolvedBaseBranch(ctx context.Context) (string, error) {
	if branch := strings.TrimSpace(m.baseBranch); branch != "" {
		return branch, nil
	}

	branch, err := git.CurrentBranch(ctx, m.workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve workspace base branch: %w", err)
	}
	return branch, nil
}

// Export squash-merges workspace branch into the configured base branch and commits.
func (m *WorkspaceManager) Export(ctx context.Context, workspaceDir, branchName, commitMessage string) error {
	mainRepo := m.workingDir
	baseBranch, err := m.resolvedBaseBranch(ctx)
	if err != nil {
		return err
	}
	currentBranch, err := git.CurrentBranch(ctx, mainRepo)
	if err != nil {
		return fmt.Errorf("resolve current repository branch: %w", err)
	}
	if currentBranch != baseBranch {
		return fmt.Errorf("export requires repository branch %q, current branch is %q", baseBranch, currentBranch)
	}

	// Stash local changes in main repo if dirty
	dirty := strings.TrimSpace(git.GitRunCmd(ctx, mainRepo, "git", "status", "--porcelain"))
	stashed := dirty != ""
	if stashed {
		if err := git.GitRunCmdErr(ctx, mainRepo, "git", "stash", "push", "-u", "-m", "norma pre-export"); err != nil {
			return fmt.Errorf("git stash push: %w", err)
		}
	}

	restoreStash := func() error {
		if !stashed {
			return nil
		}
		return git.GitRunCmdErr(ctx, mainRepo, "git", "stash", "pop")
	}

	beforeHash := strings.TrimSpace(git.GitRunCmd(ctx, mainRepo, "git", "rev-parse", "HEAD"))

	// Squash merge workspace branch into configured base branch.
	if err := git.GitRunCmdErr(ctx, mainRepo, "git", "merge", "--squash", branchName); err != nil {
		_ = git.GitRunCmdErr(ctx, mainRepo, "git", "reset", "--hard", beforeHash)
		_ = restoreStash()
		return fmt.Errorf("git merge --squash %s: %w", branchName, err)
	}

	// Stage and check if there are changes
	if err := git.GitRunCmdErr(ctx, mainRepo, "git", "add", "-A"); err != nil {
		_ = git.GitRunCmdErr(ctx, mainRepo, "git", "reset", "--hard", beforeHash)
		_ = restoreStash()
		return fmt.Errorf("git add: %w", err)
	}

	status := strings.TrimSpace(git.GitRunCmd(ctx, mainRepo, "git", "status", "--porcelain"))
	if status == "" {
		_ = restoreStash()
		log.Info().Str("base_branch", baseBranch).Msg("nothing to export — workspace already matches base branch")
		return nil
	}

	// Commit on configured base branch.
	if err := git.GitRunCmdErr(ctx, mainRepo, "git", "commit", "-m", commitMessage); err != nil {
		_ = git.GitRunCmdErr(ctx, mainRepo, "git", "reset", "--hard", beforeHash)
		_ = restoreStash()
		return fmt.Errorf("git commit: %w", err)
	}

	if err := restoreStash(); err != nil {
		return fmt.Errorf("git stash pop: %w", err)
	}

	afterHash := strings.TrimSpace(git.GitRunCmd(ctx, mainRepo, "git", "rev-parse", "HEAD"))
	log.Info().
		Str("branch", branchName).
		Str("base_branch", baseBranch).
		Str("before_hash", beforeHash).
		Str("after_hash", afterHash).
		Msg("workspace exported to base branch")

	return nil
}

// CleanupWorkspace removes a git worktree.
func (m *WorkspaceManager) CleanupWorkspace(ctx context.Context, workspaceDir string) error {
	if workspaceDir == "" {
		return nil
	}
	if err := git.RemoveWorktree(ctx, m.workingDir, workspaceDir); err != nil {
		log.Warn().Err(err).Str("workspace", workspaceDir).Msg("failed to remove worktree")
		return err
	}
	return nil
}

func (m *WorkspaceManager) recreateWorkspaceWithoutSync(ctx context.Context, workspaceDir, branchName string, syncErr error) (EnsureWorkspaceResult, error) {
	result := EnsureWorkspaceResult{Dir: workspaceDir}
	if strings.TrimSpace(branchName) == "" {
		return result, fmt.Errorf("sync workspace to base: %w", syncErr)
	}

	log.Warn().
		Err(syncErr).
		Str("workspace", workspaceDir).
		Str("branch", branchName).
		Msg("workspace sync failed; recreating worktree without sync")

	if err := git.RemoveWorktree(ctx, m.workingDir, workspaceDir); err != nil {
		return result, fmt.Errorf("cleanup conflicted workspace: %w", err)
	}
	if _, err := git.MountWorktree(ctx, m.workingDir, workspaceDir, branchName, ""); err != nil {
		return result, fmt.Errorf("remount workspace without sync: %w", err)
	}

	result.SyncSkipped = true
	return result, nil
}

func (m *WorkspaceManager) forceCleanupWorkspacePath(ctx context.Context, workspaceDir string) error {
	trimmedDir := strings.TrimSpace(workspaceDir)
	if trimmedDir == "" {
		return nil
	}
	if _, err := os.Stat(trimmedDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat workspace dir %q: %w", trimmedDir, err)
	}

	_ = git.RemoveWorktree(ctx, m.workingDir, trimmedDir)
	if err := os.RemoveAll(trimmedDir); err != nil {
		return fmt.Errorf("remove workspace dir %q: %w", trimmedDir, err)
	}
	return nil
}

func validateWorkspaceBranch(ctx context.Context, workspaceDir, expectedBranch string) error {
	branch := strings.TrimSpace(expectedBranch)
	if branch == "" {
		return nil
	}
	if !git.Available(ctx, workspaceDir) {
		return fmt.Errorf("%w: %q exists but is not a git repository", ErrWorkspaceCollision, workspaceDir)
	}
	currentBranch, err := git.CurrentBranch(ctx, workspaceDir)
	if err != nil {
		return fmt.Errorf("%w: resolve branch for %q: %w", ErrWorkspaceCollision, workspaceDir, err)
	}
	currentBranch = strings.TrimSpace(currentBranch)
	if currentBranch == "" || currentBranch == branch {
		return nil
	}
	return fmt.Errorf(
		"%w: %q is already mounted for branch %q; expected %q",
		ErrWorkspaceCollision,
		workspaceDir,
		currentBranch,
		branch,
	)
}
