package hooknativehost

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/normahq/runtime/v2/agentconfig"
)

type allowRunner struct{}

func (allowRunner) Run(context.Context, hookcmd.Event) (hookcmd.Result, error) {
	return hookcmd.Result{}, nil
}

func TestWrapLaunchAndMetadata(t *testing.T) {
	service, err := hookprovider.New(allowRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	binding, err := service.Bind(t.Context(), hookprovider.BindingScope{SessionID: "session", Provider: "provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binding.Release() }()
	if err := WriteMetadata(binding, "claude"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".adapter", ".owner-proof"} {
		info, err := os.Stat(binding.Reference() + suffix)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("metadata %s: %v, %v", suffix, info, err)
		}
	}
	config := agentconfig.Config{Type: agentconfig.AgentTypeClaudeCodeACP, ClaudeCodeACP: &agentconfig.ACPConfig{Cmd: []string{"claude-acp"}}}
	resolved := agentconfig.ResolvedConfig{Command: []string{"claude-acp", "serve"}, Model: "model", Mode: "mode"}
	got := WrapLaunch(config, "balda", "claude", binding, resolved)
	want := []string{"balda", "agent-hooks", "proxy", "claude", binding.Reference(), binding.OwnerProof(), "--", "claude-acp", "serve"}
	if !slices.Equal(got.GenericACP.Cmd, want) {
		t.Fatalf("command=%q, want %q", got.GenericACP.Cmd, want)
	}
	if got.GenericACP.Model != "model" || got.GenericACP.Mode != "mode" {
		t.Fatalf("settings=%+v", got.GenericACP)
	}
	if config.Type != agentconfig.AgentTypeClaudeCodeACP || config.ClaudeCodeACP == nil {
		t.Fatal("caller config mutated")
	}
}
