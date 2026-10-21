package balda

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/internalmcp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestNativeEntrypointsRejectInvalidACPStartupBeforeSideEffects(t *testing.T) {
	for _, entrypoint := range []string{"app", "module", "preflight"} {
		t.Run(entrypoint, func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{Balda: BaldaConfig{ACPStartupTimeout: "0s", WorkingDir: dir, StateDir: "state"}}
			var err error
			switch entrypoint {
			case "app":
				err = App(cfg, runtimeconfig.RuntimeConfig{}, "", runtimeconfig.RuntimeLoadOptions{}, nil).Err()
			case "module":
				err = fx.New(Module(cfg, runtimeconfig.RuntimeConfig{}, "", runtimeconfig.RuntimeLoadOptions{}, nil), fx.NopLogger).Err()
			case "preflight":
				err = PreflightRuntime(t.Context(), cfg, runtimeconfig.RuntimeConfig{}, runtimeconfig.RuntimeLoadOptions{})
			}
			if err == nil || !strings.Contains(err.Error(), "balda.acp_startup_timeout") {
				t.Fatalf("%s error = %v; want startup timeout validation", entrypoint, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
				t.Fatalf("state directory created before validation: %v", err)
			}
		})
	}
}

func TestProductionACPStartupFactoryBudgets(t *testing.T) {
	for _, path := range []string{"direct", "scoped", "goal"} {
		t.Run(path, func(t *testing.T) {
			cfg, norma, trace := acpWiringConfig(t, "300ms", "30s")
			var direct *agentfactory.Factory
			var scoped baldaagent.ScopedRuntimeFactory
			var builder *baldaagent.Builder
			var provider state.Provider
			var bundled *internalmcp.InternalMCPManager
			app := fx.New(Module(cfg, norma, "", runtimeconfig.RuntimeLoadOptions{}, nil), fx.NopLogger,
				fx.Populate(&direct, &scoped, &builder, &provider, &bundled))
			if err := app.Err(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Stop(context.Background()) }()
			// Composition opens state, while these tests deliberately never Start ingress.
			defer func() { _ = provider.Close() }()
			request := agentfactory.BuildRequest{AgentID: "fixture", WorkingDirectory: cfg.Balda.WorkingDir}
			var built adkagent.Agent
			var err error
			switch path {
			case "direct":
				built, err = direct.Build(t.Context(), request)
			case "scoped":
				built, err = scoped.BuildScoped(t.Context(), request, nil)
			case "goal":
				if err := bundled.EnsureStarted(t.Context()); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = bundled.Stop(context.Background()) }()
				base, buildErr := builder.BuildRuntimeWithCapabilities(t.Context(), "fixture", cfg.Balda.WorkingDir,
					nil, nil, baldaagent.SkillMetadataProjection{}, baldaagent.SessionInstructionContext{SessionID: "isolated-goal"})
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				defer func() { _ = base.Agent.(io.Closer).Close() }()
				svc := session.InMemoryService()
				for _, id := range []string{"goal-worker", "goal-validator"} {
					if _, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "startup", UserID: "fixture-user", SessionID: id}); err != nil {
						t.Fatal(err)
					}
				}
				built, err = builder.BuildGoalWorkflow(t.Context(), baldaagent.GoalBuildConfig{
					BaseAgent: base.Agent, ProviderID: "fixture", SessionID: "isolated-goal",
					WorkerSessionID: "goal-worker", ValidatorSessionID: "goal-validator", WorkspaceDir: cfg.Balda.WorkingDir,
					MaxIterations: 1, AppName: "startup", SessionService: svc,
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if closer, ok := built.(io.Closer); ok {
				defer func() { _ = closer.Close() }()
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatalf("factory eagerly launched ACP: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			start := time.Now()
			err = runACPStartupTurn(t, ctx, built)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("startup error = %v; want deadline", err)
			}
			if elapsed := time.Since(start); elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
				t.Fatalf("startup duration = %s; want selected 300ms budget plus cleanup/tolerance", elapsed)
			}
		})
	}
}

func TestPreflightScopeClassifiers(t *testing.T) {
	var params struct {
		fx.In
		Contributions []sessionmemoryapp.ScopeClassifierContribution `group:"balda_session_memory_scope_classifier"`
	}
	app := fx.New(preflightScopeClassifiers(), fx.NopLogger, fx.Populate(&params))
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		locator deliverycmd.Locator
		want    deliverycmd.LocatorScopeKind
	}{
		{telegram.NewLocator(101, 0), deliverycmd.LocatorScopePersonal},
		{telegram.NewLocator(-1001, 77), deliverycmd.LocatorScopeGroup},
		{zulip.NewDMLocator(101), deliverycmd.LocatorScopePersonal},
		{zulip.NewStreamLocator(42, "ops"), deliverycmd.LocatorScopeGroup},
		{mattermost.NewDMLocator("dm-channel-1", "user-1"), deliverycmd.LocatorScopePersonal},
		{mattermost.NewChannelLocator("team-1", "channel-1", ""), deliverycmd.LocatorScopeGroup},
		{slackagent.NewConversationLocator("T123", "D456"), deliverycmd.LocatorScopePersonal},
		{slackagent.NewThreadLocator("T123", "C456", "thread-789"), deliverycmd.LocatorScopeGroup},
	} {
		found := false
		for _, contribution := range params.Contributions {
			if contribution.ChannelType != tt.locator.ChannelType {
				continue
			}
			found = true
			kind, err := contribution.Classifier(tt.locator)
			if err != nil || kind != tt.want {
				t.Fatalf("preflight %s classification = %s, %v; want %s", tt.locator.ChannelType, kind, err, tt.want)
			}
		}
		if !found {
			t.Errorf("preflight classifier missing for %s", tt.locator.ChannelType)
		}
	}
}

func TestACPPreflightStaysLazy(t *testing.T) {
	cfg, norma, trace := acpWiringConfig(t, "300ms", "30s")
	if err := PreflightRuntime(t.Context(), cfg, norma, runtimeconfig.RuntimeLoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("preflight eagerly launched configured ACP backend: %v", err)
	}
}

func TestPreflightDoesNotResolveSelectedAgentProvider(t *testing.T) {
	for _, providerID := range []string{"", "unavailable-managed"} {
		name := "empty"
		if providerID != "" {
			name = "unavailable-managed"
		}
		t.Run(name, func(t *testing.T) {
			cfg, _, _ := acpWiringConfig(t, "300ms", "30s")
			cfg.Balda.Provider = providerID
			if err := PreflightRuntime(t.Context(), cfg, runtimeconfig.RuntimeConfig{}, runtimeconfig.RuntimeLoadOptions{}); err != nil {
				t.Fatalf("PreflightRuntime() error = %v", err)
			}
		})
	}
}

func TestACPPreflightPreservesConversationLifetimes(t *testing.T) {
	cfg, norma, _ := acpWiringConfig(t, "300ms", "30s")
	if err := os.MkdirAll(cfg.Balda.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(cfg.Balda.StateDir, "state.db")
	provider, err := state.NewSQLiteProvider(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := chatarchivecmd.Session{ID: "33c9805d-c079-4f04-a52b-77f0ce015d47", StableID: "conversation",
		RuntimeID: "memory-runtime", Persistence: chatarchivecmd.PersistenceMemory, BootID: "existing-boot",
		Location:  chatarchivecmd.Location{Channel: "telegram", AddressKey: "101:0", Locator: "telegram:101:0"},
		StartedAt: now, ObservedAt: now}
	intent, err := provider.ChatArchive().Begin(t.Context(), chatarchivecmd.Intent{Token: "seed", Kind: chatarchivecmd.KindFresh, Session: record, At: now})
	if err != nil {
		t.Fatal(err)
	}
	before, err := provider.ChatArchive().Activate(t.Context(), intent.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if err := PreflightRuntime(t.Context(), cfg, norma, runtimeconfig.RuntimeLoadOptions{}); err != nil {
		t.Fatal(err)
	}
	provider, err = state.NewSQLiteProvider(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	after, err := provider.ChatArchive().Get(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("preflight changed existing conversation lifetime: before=%+v after=%+v", before, after)
	}
}

func acpWiringConfig(t *testing.T, budget, delay string) (Config, runtimeconfig.RuntimeConfig, string) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	defaults, err := os.ReadFile("../../../cmd/balda/balda.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	configDir := filepath.Join(dir, ".config", "balda")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("runtime:\n  providers:\n    fixture:\n      type: generic_acp\n      generic_acp:\n        cmd: [fixture]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeconfig.LoadConfigDocument(runtimeconfig.RuntimeLoadOptions{WorkingDir: dir},
		runtimeconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaults, UseDotConfigAppDir: true}, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Balda.ACPStartupTimeout = budget
	cfg.Balda.Provider = "fixture"
	cfg.Balda.WorkingDir, cfg.Balda.StateDir = dir, filepath.Join(dir, "state")
	cfg.Balda.Workspace.Mode = "off"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "acp.jsonl")
	norma := runtimeconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{
		"fixture": {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{executable, "-test.run=^TestBaldaACPStartupChild$", "--", delay, trace}}},
	}}
	return cfg, norma, trace
}

func runACPStartupTurn(t *testing.T, ctx context.Context, built adkagent.Agent) error {
	t.Helper()
	svc := session.InMemoryService()
	run, err := runner.New(runner.Config{AppName: "startup", Agent: built, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "startup", UserID: "fixture-user"})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range run.Run(ctx, "fixture-user", created.Session.ID(), genai.NewContentFromText("hello", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			return err
		}
	}
	return nil
}

func TestBaldaACPStartupChild(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		t.Skip("subprocess fixture")
	}
	delay, err := time.ParseDuration(os.Args[len(os.Args)-2])
	if err != nil {
		os.Exit(2)
	}
	trace, err := os.OpenFile(os.Args[len(os.Args)-1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			_ = trace.Close()
			os.Exit(0)
		}
		_ = json.NewEncoder(trace).Encode(request)
		var result any
		switch request.Method {
		case "initialize":
			time.Sleep(delay)
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			result = map[string]any{"sessionId": "fixture-session"}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
		default:
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
}
