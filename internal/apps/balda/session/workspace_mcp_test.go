package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

func TestWorkspaceMCPImport_UsesPersistedSessionMetadata(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	initGitRepo(t, ctx, workingDir)

	writeFile(t, filepath.Join(workingDir, "base.txt"), "base\n")
	runGit(t, ctx, workingDir, "add", "base.txt")
	runGit(t, ctx, workingDir, "commit", "-m", "chore: seed")

	workspaceDir := filepath.Join(t.TempDir(), "balda-workspace")
	branchName := "norma/balda/tg-1-2"
	runGit(t, ctx, workingDir, "worktree", "add", "-b", branchName, workspaceDir, "HEAD")

	writeFile(t, filepath.Join(workingDir, "main-only.txt"), "main-only\n")
	runGit(t, ctx, workingDir, "add", "main-only.txt")
	runGit(t, ctx, workingDir, "commit", "-m", "feat: main update")

	store := &fakeSessionStore{
		recordsByID: map[string]baldastate.SessionRecord{
			"tg-1-2": {
				SessionID:    "tg-1-2",
				ChannelType:  baldastate.ChannelTypeTelegram,
				AddressKey:   "1:2",
				AddressJSON:  `{"chat_id":1,"topic_id":2}`,
				AgentName:    "opencode",
				WorkspaceDir: workspaceDir,
				BranchName:   branchName,
				Status:       baldastate.SessionStatusActive,
			},
		},
	}

	manager := &Manager{
		workspaces:       baldaagent.NewWorkspaceManager(workingDir, t.TempDir(), "master"),
		workspaceEnabled: true,
		logger:           zerolog.Nop(),
		sessionStore:     store,
		sessions:         map[string]*TopicSession{},
	}

	svc := &workspaceMCPServer{manager: manager, logger: zerolog.Nop()}
	if err := svc.Import(ctx, "tg-1-2"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	if got := readFile(t, filepath.Join(workspaceDir, "main-only.txt")); got != "main-only\n" {
		t.Fatalf("workspace main-only.txt = %q, want rebased content", got)
	}
}

func TestWorkspaceMCPExport_UsesPersistedSessionMetadata(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	initGitRepo(t, ctx, workingDir)

	writeFile(t, filepath.Join(workingDir, "base.txt"), "base\n")
	runGit(t, ctx, workingDir, "add", "base.txt")
	runGit(t, ctx, workingDir, "commit", "-m", "chore: seed")

	workspaceDir := filepath.Join(t.TempDir(), "balda-workspace")
	branchName := "norma/balda/tg-3-4"
	runGit(t, ctx, workingDir, "worktree", "add", "-b", branchName, workspaceDir, "HEAD")

	writeFile(t, filepath.Join(workspaceDir, "feature.txt"), "feature\n")
	runGit(t, ctx, workspaceDir, "add", "feature.txt")
	runGit(t, ctx, workspaceDir, "commit", "-m", "feat: branch feature")

	store := &fakeSessionStore{
		recordsByID: map[string]baldastate.SessionRecord{
			"tg-3-4": {
				SessionID:    "tg-3-4",
				ChannelType:  baldastate.ChannelTypeTelegram,
				AddressKey:   "3:4",
				AddressJSON:  `{"chat_id":3,"topic_id":4}`,
				AgentName:    "opencode",
				WorkspaceDir: workspaceDir,
				BranchName:   branchName,
				Status:       baldastate.SessionStatusActive,
			},
		},
	}

	manager := &Manager{
		workspaces:       baldaagent.NewWorkspaceManager(workingDir, t.TempDir(), "master"),
		workspaceEnabled: true,
		logger:           zerolog.Nop(),
		sessionStore:     store,
		sessions:         map[string]*TopicSession{},
	}

	svc := &workspaceMCPServer{manager: manager, logger: zerolog.Nop()}
	if err := svc.Export(ctx, "tg-3-4", "feat: export persisted balda workspace"); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	if got := readFile(t, filepath.Join(workingDir, "feature.txt")); got != "feature\n" {
		t.Fatalf("main repo feature.txt = %q, want exported content", got)
	}

	logMsg := runGit(t, ctx, workingDir, "log", "-1", "--pretty=%s")
	if strings.TrimSpace(logMsg) != "feat: export persisted balda workspace" {
		t.Fatalf("HEAD commit = %q, want export commit", strings.TrimSpace(logMsg))
	}
}
