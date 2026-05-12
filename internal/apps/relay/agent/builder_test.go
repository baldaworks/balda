package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/normahq/relay/internal/apps/relay/memory"
	"github.com/normahq/runtime/agentconfig"
	"github.com/normahq/runtime/agentfactory"
	runtimeconfig "github.com/normahq/runtime/appconfig"
	"github.com/normahq/runtime/mcpregistry"
	"github.com/normahq/runtime/sessionstate"
	adksession "google.golang.org/adk/session"
)

func TestBundledMCPServerIDs(t *testing.T) {
	tests := []struct {
		name             string
		workspaceEnabled bool
		want             []string
	}{
		{
			name:             "workspace_disabled",
			workspaceEnabled: false,
			want:             []string{"relay"},
		},
		{
			name:             "workspace_enabled",
			workspaceEnabled: true,
			want:             []string{"relay"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bundledMCPServerIDs(tt.workspaceEnabled); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("bundledMCPServerIDs(%v) = %#v, want %#v", tt.workspaceEnabled, got, tt.want)
			}
		})
	}
}

func TestMergeMCPServerIDs(t *testing.T) {
	explicit := []string{" custom.one ", "relay", "", "custom.one", "custom.two"}
	extra := []string{"relay.extra", "custom.two", " "}
	got := mergeMCPServerIDs(explicit, extra, true)
	want := []string{"relay", "custom.one", "custom.two", "relay.extra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeMCPServerIDs(%#v, %#v, true) = %#v, want %#v", explicit, extra, got, want)
	}
}

func TestBuildRelayInstruction_IncludesGlobalAndAgentInstruction(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		normaCfg: runtimeconfig.RuntimeConfig{
			Providers: map[string]agentconfig.Config{
				"alpha": {
					SystemInstructions: "norma instruction",
				},
			},
		},
		relayGlobalInstruction: "relay instruction",
	}

	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippet := "Global instruction:\nrelay instruction\n\nInstruction:\nnorma instruction"
	if !strings.Contains(got, wantSnippet) {
		t.Fatalf("buildRelayInstruction() missing snippet %q in output:\n%s", wantSnippet, got)
	}
}

func TestProviderIDs(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		normaCfg: runtimeconfig.RuntimeConfig{
			Providers: map[string]agentconfig.Config{
				"alpha": {},
				"":      {},
			},
		},
	}
	rawProviderID := " beta "
	builder.normaCfg.Providers[rawProviderID] = agentconfig.Config{}

	got := builder.ProviderIDs()
	want := []string{"alpha", "beta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProviderIDs() = %#v, want %#v", got, want)
	}
}

func TestBuildRelayInstruction_OmitsInstructionSectionsWhenEmpty(t *testing.T) {
	t.Parallel()

	builder := &Builder{}
	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"main",
	)

	if strings.Contains(got, "Global instruction:") || strings.Contains(got, "Instruction:") {
		t.Fatalf("buildRelayInstruction() unexpectedly contained instruction block:\n%s", got)
	}
}

func TestBuildRelayInstruction_IncludesMemoryPlaceholdersWhenEnabled(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		memoryStore: memory.NewStore(t.TempDir(), true, true),
	}
	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"main",
	)

	for _, snippet := range []string{
		"SOUL.md session-start instructions:\n{relay_soul?}",
		"MEMORY.md session-start facts:\n{relay_memory?}",
	} {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildRelayInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
}

func TestBuildRelayInstruction_IncludesGitWorkspaceContext(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		workspaceEnabled:    true,
		workspaceBaseBranch: "main",
		workingDir:          "/repo",
	}

	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"develop",
	)

	wantSnippets := []string{
		"Workspace settings:",
		"This session belongs to channel type: telegram.",
		"Mode: git-worktree",
		"Path: /tmp/work",
		"Config path: /repo/.config/relay/config.yaml",
		"Base branch: main",
		"Session branch: norma/relay/tg-1-2",
		"Main repo branch at start: develop",
		"Git workspace guidance:",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildRelayInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
	if strings.Contains(got, "ask one short clarifying question") {
		t.Fatalf("buildRelayInstruction() unexpectedly included clarification mandate:\n%s", got)
	}
}

func TestBuildRelayInstruction_IncludesDirectModeSettingsWhenWorkspaceDisabled(t *testing.T) {
	t.Parallel()

	builder := &Builder{workspaceEnabled: false, workingDir: "/repo"}
	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippets := []string{
		"Workspace settings:",
		"This session belongs to channel type: telegram.",
		"Mode: direct",
		"Path: /tmp/work",
		"Config path: /repo/.config/relay/config.yaml",
		"Base branch: n/a",
		"Git workspace tooling: disabled",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildRelayInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}

	if strings.Contains(got, "Git workspace guidance:") {
		t.Fatalf("buildRelayInstruction() unexpectedly included git guidance in direct mode:\n%s", got)
	}
	if strings.Contains(got, "Available namespaces:") {
		t.Fatalf("buildRelayInstruction() unexpectedly included generic MCP namespace docs:\n%s", got)
	}
}

func TestBuildRelayInstruction_IncludesFormattingGuidance_DefaultMarkdownV2(t *testing.T) {
	t.Parallel()

	builder := &Builder{}
	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippets := []string{
		"Telegram formatting mode: `markdownv2`.",
		"Write normal Markdown or plain text. Relay converts it to Telegram MarkdownV2; use Markdown blank lines or lists for structure, and do not pre-escape Telegram MarkdownV2 reserved characters.",
		"Example output: **Build:** success. Run `relay start`.",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildRelayInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
	if strings.Contains(got, "core.telegram.org/bots/api#formatting-options") {
		t.Fatalf("buildRelayInstruction() unexpectedly contains docs URL:\n%s", got)
	}
}

func TestBuildRelayInstruction_IncludesFormattingGuidance_HTML(t *testing.T) {
	t.Parallel()

	builder := &Builder{telegramFormattingMode: "html"}
	got := builder.buildRelayInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/relay/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippets := []string{
		"Telegram formatting mode: `html`.",
		"Use Telegram HTML parse mode. Supported tags: b/strong, i/em, u/ins, s/strike/del, tg-spoiler or span class=\"tg-spoiler\", a href, code, pre with nested code class=\"language-...\", blockquote expandable, tg-emoji emoji-id, tg-time unix/format. Relay escapes unsafe raw <, >, & while preserving supported Telegram HTML tags.",
		"Example output: <b>Build:</b> success. Run <code>relay start</code>.",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildRelayInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
}

func TestCreateRuntimeSession_IncludesCanonicalCWDState(t *testing.T) {
	t.Parallel()

	providers := map[string]agentconfig.Config{
		"alpha": {Type: "llm"},
	}
	builder := &Builder{
		factory:  agentfactory.New(providers, mcpregistry.New(nil)),
		normaCfg: runtimeconfig.RuntimeConfig{Providers: providers},
	}
	runtime := &BuiltRuntime{
		SessionSvc: adksession.InMemoryService(),
		AppName:    "norma-relay",
	}

	workspaceDir := t.TempDir()
	sess, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", workspaceDir)
	if err != nil {
		t.Fatalf("CreateRuntimeSession() error = %v", err)
	}
	gotCWD, err := sess.State().Get(sessionstate.CWDKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", sessionstate.CWDKey, err)
	}
	if gotCWD != workspaceDir {
		t.Fatalf("session state %q = %v, want %q", sessionstate.CWDKey, gotCWD, workspaceDir)
	}
}

func TestCreateRuntimeSession_IncludesMemorySnapshotState(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, memory.MemoryFileName), []byte("remember this\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, memory.SoulFileName), []byte("be precise\n"), 0o600); err != nil {
		t.Fatalf("write soul: %v", err)
	}
	providers := map[string]agentconfig.Config{
		"alpha": {Type: "llm"},
	}
	builder := &Builder{
		factory:     agentfactory.New(providers, mcpregistry.New(nil)),
		normaCfg:    runtimeconfig.RuntimeConfig{Providers: providers},
		memoryStore: memory.NewStore(stateDir, true, true),
	}
	runtime := &BuiltRuntime{
		SessionSvc: adksession.InMemoryService(),
		AppName:    "norma-relay",
	}

	sess, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", t.TempDir())
	if err != nil {
		t.Fatalf("CreateRuntimeSession() error = %v", err)
	}
	gotMemory, err := sess.State().Get(memory.MemoryStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", memory.MemoryStateKey, err)
	}
	if gotMemory != "remember this" {
		t.Fatalf("session state %q = %v, want remember this", memory.MemoryStateKey, gotMemory)
	}
	gotSoul, err := sess.State().Get(memory.SoulStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", memory.SoulStateKey, err)
	}
	if gotSoul != "be precise" {
		t.Fatalf("session state %q = %v, want be precise", memory.SoulStateKey, gotSoul)
	}
}

func TestCreateRuntimeSession_InvalidCWD_FailsBeforeCreate(t *testing.T) {
	t.Parallel()

	providers := map[string]agentconfig.Config{
		"alpha": {Type: agentconfig.AgentTypeGenericACP},
	}
	builder := &Builder{
		factory:  agentfactory.New(providers, mcpregistry.New(nil)),
		normaCfg: runtimeconfig.RuntimeConfig{Providers: providers},
	}
	runtime := &BuiltRuntime{
		SessionSvc: adksession.InMemoryService(),
		AppName:    "norma-relay",
	}

	_, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", t.TempDir()+"/missing")
	if err == nil {
		t.Fatal("CreateRuntimeSession() error = nil, want invalid cwd error")
	}
	if !strings.Contains(err.Error(), "stat session cwd") {
		t.Fatalf("CreateRuntimeSession() error = %q, want stat session cwd", err)
	}
}

func TestCurrentRepoBranch_Fallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		builder Builder
		want    string
	}{
		{
			name:    "workspace_disabled",
			builder: Builder{workspaceEnabled: false},
			want:    "n/a",
		},
		{
			name:    "missing_working_dir",
			builder: Builder{workspaceEnabled: true},
			want:    "unknown",
		},
		{
			name:    "non_git_working_dir",
			builder: Builder{workspaceEnabled: true, workingDir: t.TempDir()},
			want:    "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.builder.currentRepoBranch(context.Background()); got != tt.want {
				t.Fatalf("currentRepoBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}
