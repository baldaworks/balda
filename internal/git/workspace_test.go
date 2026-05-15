package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountWorktree_AllowsBranchAlreadyCheckedOutInMainWorktree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoDir := t.TempDir()
	initGitRepo(t, ctx, repoDir)

	writeFile(t, filepath.Join(repoDir, "seed.txt"), "seed\n")
	runGit(t, ctx, repoDir, "add", "seed.txt")
	runGit(t, ctx, repoDir, "commit", "-m", "chore: seed")

	branchName := "norma/balda/tg-1-0"
	runGit(t, ctx, repoDir, "checkout", "-b", branchName)

	workspaceDir := filepath.Join(t.TempDir(), "balda-workspace")
	mounted, err := MountWorktree(ctx, repoDir, workspaceDir, branchName, "HEAD")
	if err != nil {
		t.Fatalf("MountWorktree() error = %v", err)
	}
	if mounted != workspaceDir {
		t.Fatalf("MountWorktree() path = %q, want %q", mounted, workspaceDir)
	}

	if _, err := os.Stat(workspaceDir); err != nil {
		t.Fatalf("workspace dir not created: %v", err)
	}

	head := strings.TrimSpace(runGit(t, ctx, workspaceDir, "rev-parse", "--abbrev-ref", "HEAD"))
	if head != branchName {
		t.Fatalf("workspace HEAD = %q, want %q", head, branchName)
	}
}

func initGitRepo(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	runGit(t, ctx, dir, "init")
	runGit(t, ctx, dir, "config", "user.name", "Norma Test")
	runGit(t, ctx, dir, "config", "user.email", "norma-test@example.com")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func runGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
