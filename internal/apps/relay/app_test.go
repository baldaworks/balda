package relay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/normahq/relay/internal/apps/relay/paths"
	"github.com/normahq/relay/internal/git"
	"github.com/normahq/runtime/agentconfig"
	runtimeconfig "github.com/normahq/runtime/appconfig"
)

const (
	testWorkspaceBaseBranchSourceConfig = "config"
	testWorkspaceBaseBranchSourceHead   = "head"
)

var (
	_ = os.Getwd
	_ = filepath.Clean
	_ = paths.ConfigPath
)

func TestIsExpectedBotRunShutdown(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "wrapped context canceled",
			err:  fmt.Errorf("shutdown: %w", context.Canceled),
			want: true,
		},
		{
			name: "other error",
			err:  context.DeadlineExceeded,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isExpectedBotRunShutdown(tt.err); got != tt.want {
				t.Fatalf("isExpectedBotRunShutdown(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestValidateRelayMCPConfiguration_RejectsRemovedBuiltInServerReferences(t *testing.T) {
	cfg := Config{
		Relay: RelayConfig{
			MCPServers: []string{"relay.config"},
		},
	}
	normaCfg := runtimeconfig.RuntimeConfig{
		Providers: map[string]agentconfig.Config{
			"root": {MCPServers: []string{"relay.workspace"}},
		},
	}

	err := validateRelayMCPConfiguration(cfg, normaCfg, "/tmp/work/.config/relay/config.yaml")
	if err == nil {
		t.Fatal("validateRelayMCPConfiguration() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), `relay.mcp_servers[0] references removed built-in config MCP server "relay.config"; edit the relay config file directly at "/tmp/work/.config/relay/config.yaml"`) {
		t.Fatalf("unexpected relay.mcp_servers validation error: %v", err)
	}
	if !strings.Contains(err.Error(), `runtime.providers.root.mcp_servers[0] references removed built-in MCP server "relay.workspace"; use "relay"`) {
		t.Fatalf("unexpected runtime.providers validation error: %v", err)
	}
}

func TestValidateRelayMCPConfiguration_RejectsReservedCustomServerIDs(t *testing.T) {
	normaCfg := runtimeconfig.RuntimeConfig{
		Providers: map[string]agentconfig.Config{
			"root": {},
		},
		MCPServers: map[string]agentconfig.MCPServerConfig{
			"relay":          {Type: agentconfig.MCPServerTypeHTTP, URL: "http://example.com/mcp"},
			"runtime.config": {Type: agentconfig.MCPServerTypeHTTP, URL: "http://example.com/state"},
		},
	}

	err := validateRelayMCPConfiguration(Config{}, normaCfg, "/tmp/work/.config/relay/config.yaml")
	if err == nil {
		t.Fatal("validateRelayMCPConfiguration() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "runtime.mcp_servers.relay is reserved for the built-in relay MCP server") {
		t.Fatalf("missing reserved relay error: %v", err)
	}
	if !strings.Contains(err.Error(), `runtime.mcp_servers.runtime.config conflicts with removed built-in config MCP server ID "runtime.config"; edit the relay config file directly at "/tmp/work/.config/relay/config.yaml"`) {
		t.Fatalf("missing removed built-in server conflict error: %v", err)
	}
}

func TestValidateTelegramFormattingMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "default empty", in: "", want: "markdownv2"},
		{name: "trimmed markdownv2", in: "  MARKDOWNV2 ", want: "markdownv2"},
		{name: "html", in: "html", want: "html"},
		{name: "none", in: "none", want: "none"},
		{name: "invalid", in: "markdown", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := validateTelegramFormattingMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateTelegramFormattingMode(%q) error = nil, want non-nil", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateTelegramFormattingMode(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("validateTelegramFormattingMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateSessionPersistence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "default empty", in: "", want: sessionPersistenceMemory},
		{name: "trimmed memory", in: "  MEMORY ", want: sessionPersistenceMemory},
		{name: "sqlite", in: "sqlite", want: sessionPersistenceSQLite},
		{name: "invalid", in: "gorm", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := validateSessionPersistence(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateSessionPersistence(%q) error = nil, want non-nil", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSessionPersistence(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("validateSessionPersistence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveWorkspaceBaseBranch_ConfigPreferredWhenValid(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	initGitRepoForRelay(t, ctx, repoDir)

	runGitForRelay(t, ctx, repoDir, "branch", "main")

	branch, source, err := resolveWorkspaceBaseBranch(ctx, repoDir, "main", true)
	if err != nil {
		t.Fatalf("resolveWorkspaceBaseBranch returned error: %v", err)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	if source != testWorkspaceBaseBranchSourceConfig {
		t.Fatalf("source = %q, want %s", source, testWorkspaceBaseBranchSourceConfig)
	}
}

func TestResolveWorkspaceBaseBranch_FallbackToHeadWhenConfiguredMissing(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	initGitRepoForRelay(t, ctx, repoDir)
	runGitForRelay(t, ctx, repoDir, "checkout", "-b", "trunk")

	branch, source, err := resolveWorkspaceBaseBranch(ctx, repoDir, "missing-branch", true)
	if err != nil {
		t.Fatalf("resolveWorkspaceBaseBranch returned error: %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("branch = %q, want trunk", branch)
	}
	if source != testWorkspaceBaseBranchSourceHead {
		t.Fatalf("source = %q, want %s", source, testWorkspaceBaseBranchSourceHead)
	}
}

func TestResolveWorkspaceBaseBranch_EnabledRequiresResolvableBranch(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()

	if _, _, err := resolveWorkspaceBaseBranch(ctx, workingDir, "", true); err == nil {
		t.Fatal("resolveWorkspaceBaseBranch returned nil error for non-git workspace-enabled config")
	}
}

func TestResolveWorkspaceEnabledForApp_AutoDisablesWhenBaseBranchUnresolvable(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runGitForRelay(t, ctx, repoDir, "init")

	mode, enabled, err := resolveWorkspaceEnabledForApp(ctx, string(WorkspaceModeAuto), repoDir, "", git.Available)
	if err != nil {
		t.Fatalf("resolveWorkspaceEnabledForApp returned error: %v", err)
	}
	if mode != WorkspaceModeAuto {
		t.Fatalf("mode = %q, want %q", mode, WorkspaceModeAuto)
	}
	if enabled {
		t.Fatal("enabled = true, want false for unborn HEAD in auto mode")
	}
}

func TestResolveWorkspaceEnabledForApp_OnRemainsEnabledForGitRepo(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runGitForRelay(t, ctx, repoDir, "init")

	mode, enabled, err := resolveWorkspaceEnabledForApp(ctx, string(WorkspaceModeOn), repoDir, "", git.Available)
	if err != nil {
		t.Fatalf("resolveWorkspaceEnabledForApp returned error: %v", err)
	}
	if mode != WorkspaceModeOn {
		t.Fatalf("mode = %q, want %q", mode, WorkspaceModeOn)
	}
	if !enabled {
		t.Fatal("enabled = false, want true")
	}
}

func initGitRepoForRelay(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	runGitForRelay(t, ctx, dir, "init")
	runGitForRelay(t, ctx, dir, "config", "user.name", "Norma Test")
	runGitForRelay(t, ctx, dir, "config", "user.email", "norma-test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGitForRelay(t, ctx, dir, "add", "seed.txt")
	runGitForRelay(t, ctx, dir, "commit", "-m", "chore: seed")
}

func runGitForRelay(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
