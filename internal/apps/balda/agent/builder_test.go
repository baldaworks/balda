package agent

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/memory"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/hostedagent"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/normahq/runtime/v2/sessionstate"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestMergeMCPServerIDsWithBase(t *testing.T) {
	explicit := []string{" custom.one ", "balda", "", "custom.one", "custom.two"}
	extra := []string{"balda.extra", "custom.two", " "}
	got := mergeMCPServerIDsWithBase([]string{"balda"}, explicit, extra)
	want := []string{"balda", "custom.one", "custom.two", "balda.extra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeMCPServerIDsWithBase(%#v, %#v, %#v) = %#v, want %#v", []string{"balda"}, explicit, extra, got, want)
	}
}

func TestDedicatedRuntimeBuildRequestUsesSelectedProviderWithoutChatSettings(t *testing.T) {
	req, err := dedicatedRuntimeBuildRequest(" memory-fast ", " /tmp/session-memory ", " derive facts ")
	if err != nil {
		t.Fatalf("dedicatedRuntimeBuildRequest() error = %v", err)
	}
	if req.AgentID != "memory-fast" {
		t.Fatalf("dedicated runtime agent ID = %q, want memory-fast", req.AgentID)
	}
	if req.Name != "memory-fast-session-memory" {
		t.Fatalf("dedicated runtime name = %q, want memory-fast-session-memory", req.Name)
	}
	if req.WorkingDirectory != "/tmp/session-memory" || req.Instruction != "derive facts" {
		t.Fatalf("dedicated runtime request = %+v, want trimmed runtime fields", req)
	}
	if len(req.MCPServerIDs) != 0 {
		t.Fatalf("dedicated runtime MCP IDs = %#v, want empty", req.MCPServerIDs)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("dedicated runtime reasoning override = %q, want empty so provider setting is used", req.ReasoningEffort)
	}
}

func TestNewBuilderWithNilFactoryRejectsDedicatedRuntime(t *testing.T) {
	builder := NewBuilder(BuilderParams{})
	_, err := builder.BuildDedicatedRuntime(context.Background(), "memory", t.TempDir(), "derive facts")
	if err == nil || !strings.Contains(err.Error(), "agent builder is required") {
		t.Fatalf("BuildDedicatedRuntime() error = %v, want missing builder error", err)
	}
}

func TestBuildDedicatedRuntimeUsesSelectedProviderSettingsAndIsolation(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"chat": {
			Type:     agentconfig.AgentTypeCodexACP,
			CodexACP: &agentconfig.ACPConfig{Model: "chat-expensive", ReasoningEffort: "high"},
		},
		"memory": {
			Type:     agentconfig.AgentTypeCodexACP,
			CodexACP: &agentconfig.ACPConfig{Model: "memory-fast"},
		},
	}
	factory := &capturingDedicatedRuntimeFactory{providers: providers}
	builder := &Builder{dedicatedFactory: factory}

	runtime, err := builder.BuildDedicatedRuntime(context.Background(), "memory", t.TempDir(), "derive facts")
	if err != nil {
		t.Fatalf("BuildDedicatedRuntime() error = %v", err)
	}
	if runtime == nil || runtime.Agent == nil || runtime.SessionSvc == nil {
		t.Fatalf("BuildDedicatedRuntime() = %+v, want isolated agent and session service", runtime)
	}
	if runtime.AppName != defaultRuntimeAppName+"-session-memory" {
		t.Fatalf("dedicated runtime app name = %q, want isolated app name", runtime.AppName)
	}
	if len(factory.requests) != 1 {
		t.Fatalf("dedicated factory requests = %d, want 1", len(factory.requests))
	}
	req := factory.requests[0]
	if req.AgentID != "memory" {
		t.Fatalf("dedicated factory agent ID = %q, want memory", req.AgentID)
	}
	if len(req.MCPServerIDs) != 0 || req.ReasoningEffort != "" {
		t.Fatalf("dedicated factory request = %+v, want empty MCP/reasoning overrides", req)
	}
	resolved := factory.resolved[0]
	if resolved.Model != "memory-fast" || resolved.ReasoningEffort != "" {
		t.Fatalf("selected provider settings = model %q reasoning %q, want memory-fast/empty", resolved.Model, resolved.ReasoningEffort)
	}
}

type capturingDedicatedRuntimeFactory struct {
	providers map[string]agentconfig.Config
	requests  []agentfactory.BuildRequest
	resolved  []agentconfig.ResolvedConfig
}

func (f *capturingDedicatedRuntimeFactory) Build(_ context.Context, req agentfactory.BuildRequest) (adkagent.Agent, error) {
	f.requests = append(f.requests, req)
	cfg, ok := f.providers[req.AgentID]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", req.AgentID)
	}
	resolved, err := agentconfig.NormalizeConfig(cfg, "")
	if err != nil {
		return nil, err
	}
	f.resolved = append(f.resolved, resolved)
	return adkagent.New(adkagent.Config{Name: req.Name, Description: req.Description})
}

func TestBuildBaldaInstruction_IncludesGlobalAndAgentInstruction(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		normaCfg: runtimeconfig.RuntimeConfig{
			Providers: map[string]agentconfig.Config{
				"alpha": {
					SystemInstructions: "norma instruction",
				},
			},
		},
		baldaGlobalInstruction: "balda instruction",
	}

	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippet := "Global instruction:\nbalda instruction\n\nInstruction:\nnorma instruction"
	if !strings.Contains(got, wantSnippet) {
		t.Fatalf("buildBaldaInstruction() missing snippet %q in output:\n%s", wantSnippet, got)
	}
}

func TestBuildBaldaInstruction_OmitsInstructionSectionsWhenEmpty(t *testing.T) {
	t.Parallel()

	builder := &Builder{}
	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	if strings.Contains(got, "Global instruction:") || strings.Contains(got, "Instruction:") {
		t.Fatalf("buildBaldaInstruction() unexpectedly contained instruction block:\n%s", got)
	}
}

func TestBuildBaldaInstruction_IncludesMemoryPlaceholdersWhenEnabled(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		memoryEnabled: true,
	}
	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	for _, snippet := range []string{
		"Memory guidance:",
		"balda.memory.remember",
		"explicitly asks you to remember/save",
		"refreshed for active sessions on their next turn",
		"Durable Balda memory facts:\n{balda_memory?}",
	} {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildBaldaInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
}

func TestBuildBaldaInstruction_ExcludesMemoryWhenDisabled(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		memoryEnabled: false,
	}
	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	for _, forbidden := range []string{
		"Memory guidance:",
		"balda.memory.remember",
		"Durable Balda memory facts:",
		"{balda_memory?}",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("buildBaldaInstruction() contained %q with memory disabled:\n%s", forbidden, got)
		}
	}
}

func TestBuildBaldaInstruction_IncludesGitWorkspaceContext(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		workspaceEnabled:    true,
		workspaceBaseBranch: "main",
		workingDir:          "/repo",
	}

	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"develop",
	)

	wantSnippets := []string{
		"You are communicating with the Balda bot owner or an authorized collaborator.",
		"Workspace settings:",
		"This session belongs to channel type: telegram.",
		"Mode: git-worktree",
		"Path: /tmp/work",
		"Config path: /repo/.config/balda/config.yaml",
		"Base branch: main",
		"Session branch: norma/balda/tg-1-2",
		"Main repo branch at start: develop",
		"Git workspace guidance:",
		"When calling `balda.workspace.import` or `balda.workspace.export`, pass `session_id` equal to the current Session ID shown above.",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildBaldaInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
	if strings.Contains(got, "ask one short clarifying question") {
		t.Fatalf("buildBaldaInstruction() unexpectedly included clarification mandate:\n%s", got)
	}
}

func TestBuildBaldaInstruction_IncludesDirectModeSettingsWhenWorkspaceDisabled(t *testing.T) {
	t.Parallel()

	builder := &Builder{workspaceEnabled: false, workingDir: "/repo"}
	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippets := []string{
		"You are communicating with the Balda bot owner or an authorized collaborator.",
		"Workspace settings:",
		"This session belongs to channel type: telegram.",
		"Mode: direct",
		"Path: /tmp/work",
		"Config path: /repo/.config/balda/config.yaml",
		"Base branch: n/a",
		"Git workspace tooling: disabled",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildBaldaInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}

	if strings.Contains(got, "Git workspace guidance:") {
		t.Fatalf("buildBaldaInstruction() unexpectedly included git guidance in direct mode:\n%s", got)
	}
	if strings.Contains(got, "Available namespaces:") {
		t.Fatalf("buildBaldaInstruction() unexpectedly included generic MCP namespace docs:\n%s", got)
	}
}

func TestBuildBaldaInstruction_LeavesFormattingToTurnComposer(t *testing.T) {
	t.Parallel()

	builder := &Builder{}
	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	for _, stale := range []string{"Telegram formatting mode:", "[application-message-format]", "Example output:"} {
		if strings.Contains(got, stale) {
			t.Fatalf("buildBaldaInstruction() contains turn-owned formatting text %q:\n%s", stale, got)
		}
	}
}

func TestBuildBaldaInstruction_IncludesDirectConversationPolicyWithoutGoalSteering(t *testing.T) {
	t.Parallel()

	builder := &Builder{}
	got := builder.buildBaldaInstruction(
		"tg-1-2",
		"telegram",
		"alpha",
		"norma/balda/tg-1-2",
		"/tmp/work",
		"main",
	)

	wantSnippets := []string{
		"Treat ordinary user messages as ordinary conversation and reply to their actual content.",
		"Use the user's language by default unless they clearly ask for another language.",
		"Do not redirect the user to a command when a direct answer is appropriate.",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildBaldaInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
}

func TestBuildRootRuntimeInstruction_UsesPerSessionPlaceholders(t *testing.T) {
	t.Parallel()

	builder := &Builder{
		workspaceEnabled:    true,
		workspaceBaseBranch: "main",
		workingDir:          "/repo",
	}

	got := builder.buildRootRuntimeInstruction("alpha", "/tmp/work")

	for _, snippet := range []string{
		"ID: {balda_session_id}",
		"Path: {cwd}",
		"Session branch: {balda_session_branch}",
		"Main repo branch at start: {balda_repo_branch_at_start}",
	} {
		if !strings.Contains(got, snippet) {
			t.Fatalf("buildRootRuntimeInstruction() missing snippet %q in output:\n%s", snippet, got)
		}
	}
	for _, forbidden := range []string{"balda-runtime", "norma/balda/balda-runtime"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("buildRootRuntimeInstruction() unexpectedly contained %q:\n%s", forbidden, got)
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
		AppName:    "norma-balda",
	}

	workspaceDir := t.TempDir()
	sess, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", workspaceDir, RuntimeSessionContext{
		BaldaSessionID: "tg-1-2",
		SessionBranch:  "norma/balda/tg-1-2",
	})
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
	gotBaldaSessionID, err := sess.State().Get(BaldaSessionIDStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", BaldaSessionIDStateKey, err)
	}
	if gotBaldaSessionID != "tg-1-2" {
		t.Fatalf("session state %q = %v, want %q", BaldaSessionIDStateKey, gotBaldaSessionID, "tg-1-2")
	}
	gotSessionBranch, err := sess.State().Get(BaldaSessionBranchStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", BaldaSessionBranchStateKey, err)
	}
	if gotSessionBranch != "norma/balda/tg-1-2" {
		t.Fatalf("session state %q = %v, want %q", BaldaSessionBranchStateKey, gotSessionBranch, "norma/balda/tg-1-2")
	}
	gotRepoBranch, err := sess.State().Get(BaldaRepoBranchAtStartStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", BaldaRepoBranchAtStartStateKey, err)
	}
	if gotRepoBranch != workspaceBranchNA {
		t.Fatalf("session state %q = %v, want %q", BaldaRepoBranchAtStartStateKey, gotRepoBranch, workspaceBranchNA)
	}
}

func TestCreateRuntimeSession_IncludesMemorySnapshotState(t *testing.T) {
	t.Parallel()

	sess := createRuntimeSessionWithMemory(t, true)
	gotMemory, err := sess.State().Get(memory.MemoryStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", memory.MemoryStateKey, err)
	}
	if gotMemory != "remember this" {
		t.Fatalf("session state %q = %v, want remember this", memory.MemoryStateKey, gotMemory)
	}
}

func TestCreateRuntimeSession_MemoryDisabledLeavesSnapshotEmpty(t *testing.T) {
	t.Parallel()

	sess := createRuntimeSessionWithMemory(t, false)
	gotMemory, err := sess.State().Get(memory.MemoryStateKey)
	if err != nil {
		t.Fatalf("session state get %q error = %v", memory.MemoryStateKey, err)
	}
	if gotMemory != "" {
		t.Fatalf("session state %q = %v, want empty", memory.MemoryStateKey, gotMemory)
	}
}

func createRuntimeSessionWithMemory(t *testing.T, memoryEnabled bool) adksession.Session {
	t.Helper()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, memory.MemoryFileName), []byte("remember this\n"), 0o600); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	providers := map[string]agentconfig.Config{
		"alpha": {Type: "llm"},
	}
	builder := &Builder{
		factory:       agentfactory.New(providers, mcpregistry.New(nil)),
		normaCfg:      runtimeconfig.RuntimeConfig{Providers: providers},
		memoryEnabled: memoryEnabled,
		memorySnapshotReader: builderMemorySnapshotReader{
			store: memory.NewStore(newBuilderMemoryKV(), stateDir, memoryEnabled),
		},
	}
	runtime := &BuiltRuntime{
		SessionSvc: adksession.InMemoryService(),
		AppName:    "norma-balda",
	}

	sess, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", t.TempDir(), RuntimeSessionContext{
		BaldaSessionID: "tg-1-2",
	})
	if err != nil {
		t.Fatalf("CreateRuntimeSession() error = %v", err)
	}
	return sess
}

type builderMemoryKV struct {
	mu     sync.Mutex
	values map[string]any
}

func newBuilderMemoryKV() *builderMemoryKV {
	return &builderMemoryKV{values: make(map[string]any)}
}

type builderMemorySnapshotReader struct {
	store *memory.Store
}

func (r builderMemorySnapshotReader) Snapshot(ctx context.Context) (MemorySnapshot, error) {
	snapshot, err := r.store.Snapshot(ctx)
	if err != nil {
		return MemorySnapshot{}, err
	}
	return MemorySnapshot{
		Content: snapshot.Content,
		Version: snapshot.Version,
	}, nil
}

func (s *builderMemoryKV) GetJSON(_ context.Context, key string) (any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[strings.TrimSpace(key)]
	return value, ok, nil
}

func (s *builderMemoryKV) SetJSON(_ context.Context, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[strings.TrimSpace(key)] = value
	return nil
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
		AppName:    "norma-balda",
	}

	_, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", t.TempDir()+"/missing", RuntimeSessionContext{
		BaldaSessionID: "tg-1-2",
	})
	if err == nil {
		t.Fatal("CreateRuntimeSession() error = nil, want invalid cwd error")
	}
	if !strings.Contains(err.Error(), "stat session cwd") {
		t.Fatalf("CreateRuntimeSession() error = %q, want stat session cwd", err)
	}
}

func TestCreateRuntimeSession_RequiresBaldaSessionIDState(t *testing.T) {
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
		AppName:    "norma-balda",
	}

	_, err := builder.CreateRuntimeSession(context.Background(), runtime, "alpha", "user-1", "s-1", t.TempDir(), RuntimeSessionContext{})
	if err == nil {
		t.Fatal("CreateRuntimeSession() error = nil, want missing balda session id error")
	}
	if !strings.Contains(err.Error(), "balda session id is required") {
		t.Fatalf("CreateRuntimeSession() error = %q, want missing balda session id", err)
	}
}

func TestHostedAgentIncludesPersistentSessionHistory(t *testing.T) {
	t.Parallel()

	capture := &historyCaptureModel{}
	ag, err := hostedagent.New(hostedagent.Config{
		Name:        "hosted-history",
		Description: "Hosted session history test agent",
		Model:       capture,
	})
	if err != nil {
		t.Fatalf("hostedagent.New() error = %v", err)
	}
	sessionSvc := adksession.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:        "norma-balda",
		Agent:          ag,
		SessionService: sessionSvc,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionSvc.Create(context.Background(), &adksession.CreateRequest{
		AppName:   "norma-balda",
		UserID:    "tg-201",
		SessionID: "tg-201-0",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	runHostedHistoryTurn(t, r, created.Session.ID(), "remember alpha")
	runHostedHistoryTurn(t, r, created.Session.ID(), "what should you remember?")

	if got := len(capture.requests); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
	secondTexts := requestTexts(capture.requests[1])
	for _, want := range []string{"remember alpha", "acknowledged 1", "what should you remember?"} {
		if !containsString(secondTexts, want) {
			t.Fatalf("second request texts = %#v, want %q", secondTexts, want)
		}
	}
}

func runHostedHistoryTurn(t *testing.T, r *runner.Runner, sessionID string, text string) {
	t.Helper()

	for _, err := range r.Run(context.Background(), "tg-201", sessionID, genai.NewContentFromText(text, genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			t.Fatalf("runner.Run(%q) error = %v", text, err)
		}
	}
}

type historyCaptureModel struct {
	requests []*model.LLMRequest
}

func (*historyCaptureModel) Name() string {
	return "history-capture"
}

func (m *historyCaptureModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.requests = append(m.requests, req)
	reply := genai.NewContentFromText("acknowledged "+strconv.Itoa(len(m.requests)), genai.RoleModel)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: reply}, nil)
	}
}

func requestTexts(req *model.LLMRequest) []string {
	var texts []string
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.Text == "" {
				continue
			}
			texts = append(texts, part.Text)
		}
	}
	return texts
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCurrentRepoBranch_Fallbacks(t *testing.T) {
	t.Run("workspace_disabled", func(t *testing.T) {
		t.Parallel()

		builder := Builder{workspaceEnabled: false}
		if got := builder.currentRepoBranch(context.Background()); got != "n/a" {
			t.Fatalf("currentRepoBranch() = %q, want %q", got, "n/a")
		}
	})

	t.Run("missing_working_dir", func(t *testing.T) {
		t.Parallel()

		builder := Builder{workspaceEnabled: true}
		if got := builder.currentRepoBranch(context.Background()); got != "unknown" {
			t.Fatalf("currentRepoBranch() = %q, want %q", got, "unknown")
		}
	})

	t.Run("non_git_working_dir", func(t *testing.T) {
		workingDir := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(workingDir))

		builder := Builder{workspaceEnabled: true, workingDir: workingDir}
		if got := builder.currentRepoBranch(context.Background()); got != "unknown" {
			t.Fatalf("currentRepoBranch() = %q, want %q", got, "unknown")
		}
	})
}
