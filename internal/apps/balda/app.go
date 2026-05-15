package balda

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ipfans/fxlogger"
	relayagent "github.com/normahq/balda/internal/apps/balda/agent"
	"github.com/normahq/balda/internal/apps/balda/auth"
	"github.com/normahq/balda/internal/apps/balda/handlers"
	"github.com/normahq/balda/internal/apps/balda/memory"
	"github.com/normahq/balda/internal/apps/balda/paths"
	"github.com/normahq/balda/internal/apps/balda/shutdown"
	relaystate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/telegramfmt"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"github.com/normahq/balda/internal/apps/sessionmcp"
	"github.com/normahq/balda/internal/git"
	"github.com/normahq/runtime/agentconfig"
	"github.com/normahq/runtime/agentfactory"
	runtimeconfig "github.com/normahq/runtime/appconfig"
	"github.com/normahq/runtime/mcpregistry"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime"
	"github.com/tgbotkit/runtime/updatepoller"
	"go.uber.org/fx"
	adksession "google.golang.org/adk/session"
)

type workspaceBaseBranchParams struct {
	fx.In

	WorkspaceEnabled bool `name:"relay_workspace_enabled"`
}

const bundledRelayMCPServerID = "balda"

const (
	sessionPersistenceMemory = "memory"
	sessionPersistenceSQLite = "sqlite"
)

// App creates a new fx.App for the Balda bot with the provided configuration.
func App(
	cfg Config,
	normaCfg runtimeconfig.RuntimeConfig,
	ownerToken string,
	runtimeLoadOpts runtimeconfig.RuntimeLoadOptions,
	defaultsYAML []byte,
) *fx.App {
	return fx.New(
		fx.WithLogger(
			fxlogger.WithZerolog(
				log.Logger.With().Str("component", "balda").Logger(),
			),
		),
		Module(cfg, normaCfg, ownerToken, runtimeLoadOpts, defaultsYAML),
	)
}

// Module returns the fx.Module for the Balda bot, initialized with the provided configurations.
func Module(
	cfg Config,
	normaCfg runtimeconfig.RuntimeConfig,
	ownerToken string,
	runtimeLoadOpts runtimeconfig.RuntimeLoadOptions,
	defaultsYAML []byte,
) fx.Option {
	// Convert balda config to tgbotkit config.
	tgbotkitCfg := tgbotkit.Config{
		Token: cfg.Relay.Telegram.Token,
		Webhook: tgbotkit.WebhookConfig{
			Enabled:    cfg.Relay.Telegram.Webhook.Enabled,
			ListenAddr: cfg.Relay.Telegram.Webhook.ListenAddr,
			Path:       cfg.Relay.Telegram.Webhook.Path,
			URL:        cfg.Relay.Telegram.Webhook.URL,
			AuthToken:  cfg.Relay.Telegram.Webhook.AuthToken,
		},
	}

	logger := log.Logger.With().Str("component", "balda").Logger()
	workingDir, err := paths.ResolveWorkingDir(cfg.Relay.WorkingDir)
	if err != nil {
		return fx.Module("balda", fx.Error(fmt.Errorf("resolve balda working_dir: %w", err)))
	}
	configPath := paths.ConfigPath(workingDir)
	if err := validateRelayMCPConfiguration(cfg, normaCfg, configPath); err != nil {
		return fx.Module("balda", fx.Error(err))
	}
	formattingMode, err := validateTelegramFormattingMode(cfg.Relay.Telegram.FormattingMode)
	if err != nil {
		return fx.Module("balda", fx.Error(err))
	}
	stateDir, err := paths.ResolveStateDir(workingDir, cfg.Relay.StateDir)
	if err != nil {
		return fx.Module("balda", fx.Error(err))
	}
	sessionPersistence, err := validateSessionPersistence(cfg.Relay.Sessions.Persistence)
	if err != nil {
		return fx.Module("balda", fx.Error(err))
	}
	jobSchedulerConfig := buildJobSchedulerConfig(cfg.Relay)
	inboundWebhookConfig := buildInboundWebhookConfig(cfg.Relay)

	// Start with global MCP servers.
	mcpServers := make(map[string]agentconfig.MCPServerConfig, len(normaCfg.MCPServers))
	for k, v := range normaCfg.MCPServers {
		mcpServers[k] = v
	}
	mcpReg := mcpregistry.New(mcpServers)

	return fx.Module("balda",
		fx.Supply(
			tgbotkitCfg,
			logger,
			normaCfg,
			workingDir,
			mcpReg,
			jobSchedulerConfig,
			inboundWebhookConfig,
		),
		fx.Provide(
			fx.Annotate(
				func() string { return stateDir },
				fx.ResultTags(`name:"relay_state_dir"`),
			),
			func() *memory.Store {
				return memory.NewStore(stateDir, cfg.Relay.Memory.Enabled)
			},
		),
		fx.Provide(
			func(lc fx.Lifecycle) (relaystate.Provider, error) {
				if err := os.MkdirAll(stateDir, 0o755); err != nil {
					return nil, fmt.Errorf("create balda state dir: %w", err)
				}
				dbPath := filepath.Join(stateDir, "balda.db")
				provider, err := relaystate.NewSQLiteProvider(context.Background(), dbPath)
				if err != nil {
					return nil, fmt.Errorf("open balda state provider: %w", err)
				}
				lc.Append(fx.Hook{
					OnStop: func(ctx context.Context) error {
						return provider.Close()
					},
				})
				return provider, nil
			},
			func(provider relaystate.Provider) updatepoller.OffsetStore {
				return provider.PollingOffsetStore()
			},
			func(provider relaystate.Provider) sessionmcp.Store {
				return provider.SessionMCPKV()
			},
		),
		fx.Provide(
			fx.Annotate(
				func(provider relaystate.Provider) adksession.Service {
					if sessionPersistence == sessionPersistenceSQLite {
						return provider.ADKSessions()
					}
					return adksession.InMemoryService()
				},
				fx.ResultTags(`name:"relay_adk_session_service"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() bool {
					return sessionPersistence == sessionPersistenceSQLite
				},
				fx.ResultTags(`name:"relay_sessions_persistent"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() (bool, error) {
					mode, enabled, err := resolveWorkspaceEnabledForApp(
						context.Background(),
						cfg.Relay.Workspace.Mode,
						workingDir,
						cfg.Relay.Workspace.BaseBranch,
						git.Available,
					)
					if err != nil {
						return false, err
					}
					warnLegacyWorkspaceDir(logger, workingDir, stateDir, enabled)
					logger.Info().
						Str("workspace_mode", string(mode)).
						Bool("workspace_enabled", enabled).
						Str("working_dir", workingDir).
						Str("state_dir", stateDir).
						Msg("balda workspace mode resolved")
					return enabled, nil
				},
				fx.ResultTags(`name:"relay_workspace_enabled"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func(p workspaceBaseBranchParams) (string, error) {
					baseBranch, source, err := resolveWorkspaceBaseBranch(
						context.Background(),
						workingDir,
						cfg.Relay.Workspace.BaseBranch,
						p.WorkspaceEnabled,
					)
					if err != nil {
						return "", err
					}
					logger.Info().
						Str("workspace_base_branch", baseBranch).
						Str("workspace_base_branch_source", source).
						Bool("workspace_enabled", p.WorkspaceEnabled).
						Msg("balda workspace base branch resolved")
					return baseBranch, nil
				},
				fx.ResultTags(`name:"relay_workspace_base_branch"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() []string { return append([]string(nil), cfg.Relay.MCPServers...) },
				fx.ResultTags(`name:"relay_mcp_servers"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() []string { return sortedMCPServerIDs(normaCfg.MCPServers) },
				fx.ResultTags(`name:"relay_runtime_mcp_server_ids"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() string {
					return strings.TrimSpace(cfg.Relay.GlobalInstruction)
				},
				fx.ResultTags(`name:"relay_global_instruction"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() string {
					return formattingMode
				},
				fx.ResultTags(`name:"relay_telegram_formatting_mode"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() bool {
					return cfg.Relay.Telegram.PlanUpdates
				},
				fx.ResultTags(`name:"relay_telegram_plan_updates"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() int {
					return cfg.Relay.Goal.MaxIterations
				},
				fx.ResultTags(`name:"relay_goal_max_iterations"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() string { return strings.TrimSpace(ownerToken) },
				fx.ResultTags(`name:"relay_auth_token"`),
			),
		),
		fx.Provide(
			fx.Annotate(
				func() string {
					return cfg.Relay.Provider
				},
				fx.ResultTags(`name:"relay_provider"`),
			),
		),
		fx.Provide(func(provider relaystate.Provider) (*auth.OwnerStore, error) {
			return auth.NewOwnerStore(provider.AppKV())
		}),
		fx.Provide(func(provider relaystate.Provider) (*auth.InviteStore, error) {
			return auth.NewInviteStore(provider.AppKV())
		}),
		fx.Provide(func(provider relaystate.Provider) *auth.CollaboratorStore {
			// Wrap the state.CollaboratorStore interface in *auth.CollaboratorStore
			// The wrapper delegates to the underlying store implementation
			return auth.NewCollaboratorStore(provider.Collaborators())
		}),
		fx.Provide(func(reg *mcpregistry.MapRegistry) *agentfactory.Factory {
			return agentfactory.New(
				normaCfg.Providers,
				reg,
				agentfactory.WithPermissionHandler(relayagent.DefaultPermissionHandler),
			)
		}),
		tgbotkit.Module,
		handlers.Module,
		fx.Provide(
			handlers.NewInternalMCPManager,
		),
		// Start Balda provider runtime and Telegram runtime only after bundled internal MCP is started.
		fx.Invoke(func(lc fx.Lifecycle, bot *runtime.Bot, runtimeManager *relayagent.RuntimeManager, mcpManager *handlers.InternalMCPManager) {
			runCtx, cancel := context.WithCancel(context.Background())
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					if err := mcpManager.EnsureStarted(ctx); err != nil {
						return fmt.Errorf("start bundled internal MCP servers: %w", err)
					}
					if err := runtimeManager.EnsureRuntime(ctx); err != nil {
						return fmt.Errorf("start Balda provider runtime: %w", err)
					}
					go func() {
						if err := bot.Run(runCtx); err != nil {
							if isExpectedBotRunShutdown(err) {
								bot.Logger().Debugf("bot run stopped during shutdown: %v", err)
								return
							}
							bot.Logger().Errorf("bot run failed: %v", err)
						}
					}()
					return nil
				},
				OnStop: func(ctx context.Context) error {
					cancel()
					return nil
				},
			})
		}),
	)
}

var removedBuiltInRelayMCPServerIDs = map[string]string{
	"runtime.state":     bundledRelayMCPServerID,
	"runtime.workspace": bundledRelayMCPServerID,
	"runtime.relay":     bundledRelayMCPServerID,
	"balda.state":       bundledRelayMCPServerID,
	"balda.workspace":   bundledRelayMCPServerID,
}

var removedConfigMCPServerIDs = map[string]struct{}{
	"runtime.config": {},
	"balda.config":   {},
}

func validateRelayMCPConfiguration(cfg Config, normaCfg runtimeconfig.RuntimeConfig, configPath string) error {
	errs := make([]string, 0)

	for id := range normaCfg.MCPServers {
		switch id {
		case bundledRelayMCPServerID:
			errs = append(errs, `runtime.mcp_servers.balda is reserved for the built-in balda MCP server`)
		default:
			if _, ok := removedConfigMCPServerIDs[id]; ok {
				errs = append(errs, fmt.Sprintf("runtime.mcp_servers.%s conflicts with removed built-in config MCP server ID %q; edit the balda config file directly at %q", id, id, configPath))
			} else if replacement, ok := removedBuiltInRelayMCPServerIDs[id]; ok {
				errs = append(errs, fmt.Sprintf("runtime.mcp_servers.%s conflicts with removed built-in MCP server ID %q; rename the custom server and use %q for the built-in balda MCP server", id, id, replacement))
			}
		}
	}

	for i, id := range cfg.Relay.MCPServers {
		trimmed := strings.TrimSpace(id)
		if _, ok := removedConfigMCPServerIDs[trimmed]; ok {
			errs = append(errs, fmt.Sprintf("balda.mcp_servers[%d] references removed built-in config MCP server %q; edit the balda config file directly at %q", i, id, configPath))
		} else if replacement, ok := removedBuiltInRelayMCPServerIDs[trimmed]; ok {
			errs = append(errs, fmt.Sprintf("balda.mcp_servers[%d] references removed built-in MCP server %q; use %q", i, id, replacement))
		}
	}

	for agentName, agentCfg := range normaCfg.Providers {
		for i, id := range agentCfg.MCPServers {
			trimmed := strings.TrimSpace(id)
			if _, ok := removedConfigMCPServerIDs[trimmed]; ok {
				errs = append(errs, fmt.Errorf("runtime.providers.%s.mcp_servers[%d] references removed built-in config MCP server %q; edit the balda config file directly at %q", agentName, i, id, configPath).Error())
			} else if replacement, ok := removedBuiltInRelayMCPServerIDs[trimmed]; ok {
				errs = append(errs, fmt.Errorf("runtime.providers.%s.mcp_servers[%d] references removed built-in MCP server %q; use %q", agentName, i, id, replacement).Error())
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("invalid balda MCP configuration: %s", strings.Join(errs, "; "))
}

func isExpectedBotRunShutdown(err error) bool {
	return shutdown.IsExpected(err)
}

func warnLegacyWorkspaceDir(logger zerolog.Logger, workingDir, stateDir string, workspaceEnabled bool) {
	if !workspaceEnabled {
		return
	}

	legacyDir := filepath.Join(workingDir, ".norma", "balda-sessions")
	newDir := filepath.Join(stateDir, "balda-sessions")
	if filepath.Clean(legacyDir) == filepath.Clean(newDir) {
		return
	}

	fi, err := os.Stat(legacyDir)
	if err != nil {
		return
	}
	if !fi.IsDir() {
		return
	}

	logger.Warn().
		Str("legacy_workspace_dir", legacyDir).
		Str("workspace_dir", newDir).
		Msg("legacy balda workspace directory detected and ignored")
}

func resolveWorkspaceBaseBranch(
	ctx context.Context,
	workingDir string,
	configuredBranch string,
	workspaceEnabled bool,
) (branch string, source string, err error) {
	configured := strings.TrimSpace(configuredBranch)
	if !workspaceEnabled {
		if configured == "" {
			return "", "disabled", nil
		}
		return configured, "config", nil
	}

	if configured != "" {
		ref := "refs/heads/" + configured
		if err := git.GitRunCmdErr(ctx, workingDir, "git", "show-ref", "--verify", "--quiet", ref); err == nil {
			return configured, "config", nil
		}
	}

	headBranch, err := git.CurrentBranch(ctx, workingDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve balda.workspace.base_branch: %w", err)
	}
	return headBranch, "head", nil
}

func resolveWorkspaceEnabledForApp(
	ctx context.Context,
	modeRaw string,
	workingDir string,
	configuredBaseBranch string,
	isGitRepo func(context.Context, string) bool,
) (WorkspaceMode, bool, error) {
	mode, enabled, err := ResolveWorkspaceEnabled(ctx, modeRaw, workingDir, isGitRepo)
	if err != nil {
		return "", false, err
	}

	// In auto mode, keep startup safe: if base branch can't be resolved yet
	// (for example unborn HEAD), run without git workspaces.
	if mode == WorkspaceModeAuto && enabled {
		if _, _, err := resolveWorkspaceBaseBranch(ctx, workingDir, configuredBaseBranch, true); err != nil {
			return mode, false, nil
		}
	}

	return mode, enabled, nil
}

func sortedMCPServerIDs(servers map[string]agentconfig.MCPServerConfig) []string {
	if len(servers) == 0 {
		return nil
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		ids = append(ids, trimmed)
	}
	sort.Strings(ids)
	return ids
}

func validateTelegramFormattingMode(raw string) (string, error) {
	mode, err := telegramfmt.ValidateMode(raw)
	if err != nil {
		return "", err
	}
	return mode, nil
}

func validateSessionPersistence(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		mode = sessionPersistenceSQLite
	}
	switch mode {
	case sessionPersistenceMemory, sessionPersistenceSQLite:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid balda.sessions.persistence %q: supported values are %q and %q", raw, sessionPersistenceMemory, sessionPersistenceSQLite)
	}
}

func buildJobSchedulerConfig(cfg RelayConfig) handlers.JobSchedulerConfig {
	locatorAliases := make(map[string]handlers.JobLocatorAlias, len(cfg.Locators))
	for alias, locator := range cfg.Locators {
		trimmedAlias := strings.TrimSpace(alias)
		locatorAliases[trimmedAlias] = handlers.JobLocatorAlias{
			ChannelType: strings.TrimSpace(locator.ChannelType),
			AddressKey:  strings.TrimSpace(locator.AddressKey),
			AddressJSON: strings.TrimSpace(locator.AddressJSON),
			SessionID:   strings.TrimSpace(locator.SessionID),
		}
	}

	jobs := make([]handlers.ConfiguredScheduledJob, 0, len(cfg.Scheduler.Jobs))
	for _, job := range cfg.Scheduler.Jobs {
		jobs = append(jobs, handlers.ConfiguredScheduledJob{
			ID:     strings.TrimSpace(job.ID),
			Alias:  strings.TrimSpace(job.Alias),
			Cron:   strings.TrimSpace(job.Cron),
			Prompt: strings.TrimSpace(job.Prompt),
		})
	}

	return handlers.JobSchedulerConfig{
		LocatorAliases: locatorAliases,
		Jobs:           jobs,
	}
}

func buildInboundWebhookConfig(cfg RelayConfig) handlers.InboundWebhookConfig {
	locatorAliases := make(map[string]handlers.WebhookLocatorAlias, len(cfg.Locators))
	for alias, locator := range cfg.Locators {
		trimmedAlias := strings.TrimSpace(alias)
		locatorAliases[trimmedAlias] = handlers.WebhookLocatorAlias{
			ChannelType: strings.TrimSpace(locator.ChannelType),
			AddressKey:  strings.TrimSpace(locator.AddressKey),
			AddressJSON: strings.TrimSpace(locator.AddressJSON),
			SessionID:   strings.TrimSpace(locator.SessionID),
		}
	}

	routes := make(map[string]handlers.InboundWebhookRouteConfig, len(cfg.InboundWebhooks.Routes))
	for routeName, route := range cfg.InboundWebhooks.Routes {
		routes[strings.TrimSpace(routeName)] = handlers.InboundWebhookRouteConfig{
			Path:           strings.TrimSpace(route.Path),
			ReportTo:       strings.TrimSpace(route.ReportTo),
			PromptTemplate: strings.TrimSpace(route.PromptTemplate),
		}
	}

	return handlers.InboundWebhookConfig{
		Enabled:        cfg.InboundWebhooks.Enabled,
		ListenAddr:     strings.TrimSpace(cfg.InboundWebhooks.ListenAddr),
		LocatorAliases: locatorAliases,
		Routes:         routes,
	}
}
