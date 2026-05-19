package agent

import (
	"strings"
	"testing"
)

func TestGoalkeeperChildBuildRequest_SetsOutputKeyAndInstructions(t *testing.T) {
	t.Parallel()

	builder := &Builder{workingDir: "/repo"}
	cfg := goalkeeperChildAgentConfig{
		ProviderID:        "provider",
		Name:              goalkeeperWorkerName,
		Description:       "Goalkeeper worker agent",
		SessionID:         "tg-1-2",
		SessionBranch:     "norma/balda/tg-1-2",
		WorkspaceDir:      "/tmp/workspace",
		RepoBranchAtStart: "main",
		RoleInstruction:   "worker role instruction",
		OutputKey:         "  app:goalkeeper_worker_output  ",
		MCPServerIDs:      []string{"balda"},
	}

	req := builder.goalkeeperChildBuildRequest(cfg)
	if req.OutputKey != goalkeeperWorkerOutputStateKey {
		t.Fatalf("req.OutputKey = %q, want %q", req.OutputKey, goalkeeperWorkerOutputStateKey)
	}
	if !strings.Contains(req.Instruction, "worker role instruction") {
		t.Fatalf("req.Instruction = %q, want role instruction", req.Instruction)
	}
	if !strings.Contains(req.Instruction, "Workspace settings:") {
		t.Fatalf("req.Instruction = %q, want Balda base instruction", req.Instruction)
	}
}

func TestGoalkeeperValidatorInstruction_ContainsWorkerOutputPlaceholder(t *testing.T) {
	t.Parallel()

	got := goalkeeperValidatorInstruction(goalkeeperWorkerOutputStateKey)
	want := "{app:goalkeeper_worker_output?}"
	if !strings.Contains(got, want) {
		t.Fatalf("goalkeeperValidatorInstruction() = %q, want %q placeholder", got, want)
	}
}
