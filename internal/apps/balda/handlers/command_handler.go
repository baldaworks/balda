package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/auth"
	"github.com/normahq/balda/internal/apps/balda/automode"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/locatorref"
	"github.com/normahq/balda/internal/apps/balda/pluginapp"
	"github.com/normahq/balda/internal/apps/balda/plugincmd"
	"github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
	"github.com/normahq/balda/internal/apps/balda/welcome"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

type commandSessionManager interface {
	CreateSession(ctx context.Context, sessionCtx session.SessionContext, agentName string) error
	GetAgentMetadata(agentName string) session.AgentMetadata
	GetSessionInfo(ctx context.Context, sessionID string) (session.TopicSessionInfo, error)
	RuntimeStateValue(ctx context.Context, locator session.SessionLocator, key string) (any, bool, error)
	UpdateRuntimeState(ctx context.Context, locator session.SessionLocator, state map[string]any) error
	BaldaProviderID() string
	ResetSession(ctx context.Context, locator session.SessionLocator) error
	TakeStartupNotice(sessionID string) string
}

type sessionWorkCanceller interface {
	CancelWork(ctx context.Context, locator session.SessionLocator, actor string, reason string) error
}

type goalJobService interface {
	HasActiveGoalJob(ctx context.Context, sessionID string) (bool, error)
}

const (
	commandStart    = "start"
	commandHelp     = "help"
	commandTopic    = "topic"
	commandLocator  = "locator"
	commandCancel   = "cancel"
	commandGoal     = "goalkeeper"
	commandUser     = "user"
	commandUsage    = "usage"
	commandAuto     = "auto"
	commandReset    = "reset"
	commandRestart  = "restart"
	commandClose    = "close"
	commandPlugins  = plugincmd.CommandPlugin
	chatTypePrivate = "private"

	userActionAdd    = "add"
	userActionInvite = "invite"
	userActionList   = "list"
	userActionRemove = "remove"
)

// CommandHandler handles Balda chat commands such as /topic, /goalkeeper, /reset,
// /restart, /locator, /close, /cancel, and /user.
type CommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           TelegramChannel
	sessionManager    commandSessionManager
	workCanceller     sessionWorkCanceller
	actorDispatcher   actortransport.Dispatcher
	goalJobs          goalJobService
	goalMaxIterations int
	autoMaxTurns      int
	userHandler       *userHandler
	plugins           *pluginapp.Service
}

type commandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           TelegramChannel
	SessionManager    *session.Manager
	WorkCanceller     appports.SessionWorkCanceller `optional:"true"`
	Dispatcher        actortransport.Dispatcher
	GoalJobs          *baldajobs.JobLifecycleService `optional:"true"`
	MaxIterations     int                            `name:"balda_goal_max_iterations"`
	AutoMaxTurns      int                            `name:"balda_automode_max_turns"`
	UserHandler       *userHandler
	Plugins           *pluginapp.Service `optional:"true"`
}

// Register registers the handler with the registry.
func (h *CommandHandler) Register(registry TelegramRegistry) {
	registry.OnCommand(h.onCommand)
}

func (h *CommandHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
	commandCtx, ok := h.channel.CommandContextFromEvent(event)
	if !ok {
		return nil
	}

	switch commandCtx.Command {
	case commandHelp:
		return h.onHelpCommand(ctx, commandCtx)
	case commandTopic:
		return h.onTopicCommand(ctx, commandCtx)
	case commandReset, commandRestart:
		return h.onResetCommand(ctx, commandCtx)
	case commandLocator:
		return h.onLocatorCommand(ctx, commandCtx)
	case commandClose:
		return h.onCloseCommand(ctx, commandCtx)
	case commandCancel:
		return h.onCancelCommand(ctx, commandCtx)
	case commandGoal:
		return h.onGoalCommand(ctx, commandCtx)
	case commandUsage:
		return h.onUsageCommand(ctx, commandCtx)
	case commandAuto:
		return h.onAutoCommand(ctx, commandCtx)
	case commandUser:
		// Route to UserHandler
		return h.userHandler.HandleUserCommand(ctx, commandCtx)
	case commandPlugins:
		return h.onPluginsCommand(ctx, commandCtx)
	default:
		return nil
	}
}

func (h *CommandHandler) onHelpCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if strings.TrimSpace(commandCtx.Args) != "" {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /help")
	}
	return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, renderHelpMessage(h.canUseSessionCommand(ctx, commandCtx.UserID), h.ownerStore != nil && h.ownerStore.IsOwner(commandCtx.UserID)))
}

func renderHelpMessage(canUseSessionCommands bool, isOwner bool) string {
	var lines []string
	lines = append(lines, "# Available commands")
	lines = append(lines, "", "## Onboarding", "", "- `/start` — connect or restore access")

	if canUseSessionCommands {
		lines = append(lines, "", "## Sessions", "")
		lines = append(lines, "- `/topic <name>` — create a new DM topic session")
		lines = append(lines, "- `/reset` — reset current session and start again")
		lines = append(lines, "- `/restart` — alias of `/reset`")
		lines = append(lines, "- `/close` — close topic or clear current DM session")
		lines = append(lines, "- `/cancel` — request cancel for the current turn")
		lines = append(lines, "- `/locator` — show current session locator")
		lines = append(lines, "- `/usage` — show last provider usage for this session")
		lines = append(lines, "", "## Automation", "")
		lines = append(lines, "- `/goalkeeper <objective>` — start a goal run")
		lines = append(lines, "- `/goalkeeper clear` — clear active goal run")
		lines = append(lines, "- `/auto` — show auto mode status")
		lines = append(lines, "- `/auto on` — enable auto mode")
		lines = append(lines, "- `/auto off` — disable auto mode")
	}

	if isOwner {
		lines = append(lines, "", plugincmd.HelpMarkdown())
		lines = append(lines, "", "## Admin", "")
		lines = append(lines, "- `/user add` — create collaborator invite")
		lines = append(lines, "- `/user list` — list collaborators and invites")
		lines = append(lines, "- `/user remove <user_id>` — remove collaborator")
	}

	return strings.Join(lines, "\n")
}

func (h *CommandHandler) onPluginsCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if h.ownerStore == nil || !h.ownerStore.IsOwner(commandCtx.UserID) {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner can use this command.")
	}
	args := strings.TrimSpace(commandCtx.Args)
	if args == "" {
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsageMarkdown())
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsageMarkdown())
	}
	switch fields[0] {
	case plugincmd.CommandPluginsList:
		return h.sendPluginsList(ctx, commandCtx, fields[1:])
	case plugincmd.CommandPluginsShow, plugincmd.CommandPluginsInstall, plugincmd.CommandPluginsRemove:
		return h.sendPluginsAction(ctx, commandCtx, fields[0], fields[1:])
	case plugincmd.CommandPluginsMarketplace:
		if len(fields) < 2 {
			return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsageMarkdown())
		}
		switch fields[1] {
		case plugincmd.CommandMarketplaceAdd, plugincmd.CommandMarketplaceList, plugincmd.CommandMarketplaceShow, plugincmd.CommandMarketplaceUpgrade, plugincmd.CommandMarketplaceRemove:
			return h.sendMarketplaceAction(ctx, commandCtx, fields[1], fields[2:])
		default:
			return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsageMarkdown())
		}
	default:
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsageMarkdown())
	}
}

func (h *CommandHandler) sendPluginsList(ctx context.Context, commandCtx TelegramCommandContext, rest []string) error {
	available := len(rest) == 1 && (rest[0] == "--available" || rest[0] == "available")
	if len(rest) > 1 || (len(rest) == 1 && !available) {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
	}
	if h.plugins == nil {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin service is unavailable.")
	}
	if available {
		plugins, err := h.plugins.ListAvailable(ctx)
		if err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not list available plugins.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderAvailablePluginsMarkdown(plugins))
	}
	plugins, err := h.plugins.ListInstalled(ctx)
	if err != nil {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not list installed plugins.")
	}
	return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderInstalledPluginsMarkdown(plugins))
}

func (h *CommandHandler) sendPluginsAction(ctx context.Context, commandCtx TelegramCommandContext, action string, rest []string) error {
	if len(rest) != 1 {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
	}
	switch action {
	case plugincmd.CommandPluginsShow:
		if h.plugins == nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin service is unavailable.")
		}
		name := strings.TrimSpace(strings.SplitN(rest[0], "@", 2)[0])
		plugin, ok, err := h.plugins.GetInstalled(ctx, name)
		if err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not load plugin details.")
		}
		if ok {
			return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderInstalledPluginMarkdown(plugin))
		}
		available, found, err := h.plugins.GetAvailable(ctx, rest[0])
		if err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not load plugin details.")
		}
		if !found {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.NotImplementedMessage("/plugin show "+rest[0]))
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderAvailablePluginMarkdown(available))
	case plugincmd.CommandPluginsInstall:
		if h.plugins == nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin service is unavailable.")
		}
		if err := h.plugins.Install(ctx, rest[0]); err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not install plugin.")
		}
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin installed.")
	case plugincmd.CommandPluginsRemove:
		if h.plugins == nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin service is unavailable.")
		}
		if err := h.plugins.RemoveInstalled(ctx, rest[0]); err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not remove plugin.")
		}
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin removed.")
	default:
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.NotImplementedMessage("/plugin "+action+" "+rest[0]))
	}
}

func (h *CommandHandler) sendMarketplaceAction(ctx context.Context, commandCtx TelegramCommandContext, action string, rest []string) error {
	if h.plugins == nil {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin service is unavailable.")
	}
	switch action {
	case plugincmd.CommandMarketplaceList:
		if len(rest) != 0 {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
		}
		sources, err := h.plugins.ListMarketplaceStatuses(ctx)
		if err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not list plugin marketplaces.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderMarketplaceStatusesMarkdown(sources))
	case plugincmd.CommandMarketplaceShow:
		if len(rest) != 1 {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
		}
		status, ok, err := h.plugins.GetMarketplaceStatus(ctx, rest[0])
		if err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not load plugin marketplace.")
		}
		if !ok {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin marketplace not found.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderMarketplaceStatusMarkdown(status))
	case plugincmd.CommandMarketplaceAdd:
		if len(rest) < 1 {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
		}
		source := strings.TrimSpace(rest[0])
		if source == "" {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
		}
		if err := h.plugins.AddMarketplace(ctx, pluginapp.MarketplaceSource{
			Name:   pluginapp.InferMarketplaceName(source),
			Source: source,
		}); err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not add plugin marketplace.")
		}
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin marketplace added.")
	case plugincmd.CommandMarketplaceUpgrade:
		if len(rest) > 1 {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
		}
		name := ""
		if len(rest) == 1 {
			name = strings.TrimSpace(rest[0])
		}
		results, err := h.plugins.UpgradeMarketplaces(ctx, name)
		if err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not refresh plugin marketplaces.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.RenderMarketplaceUpgradeMarkdown(results))
	case plugincmd.CommandMarketplaceRemove:
		if len(rest) != 1 {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
		}
		if err := h.plugins.RemoveMarketplace(ctx, rest[0]); err != nil {
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not remove plugin marketplace.")
		}
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Plugin marketplace removed.")
	default:
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, plugincmd.TransportUsage())
	}
}

func (h *CommandHandler) onAutoCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command.")
	}
	arg := strings.ToLower(strings.TrimSpace(commandCtx.Args))
	switch arg {
	case "":
		status, err := loadAutoStatus(ctx, h.sessionManager, commandCtx.Locator)
		if err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to load auto mode status")
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not read auto mode status.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, automode.RenderStatusMarkdown(status))
	case autoActionOn:
		if err := dispatchAutoStateUpdate(ctx, h.actorDispatcher, commandCtx.Locator, automode.EnableStateWithMaxTurns(time.Now(), h.autoMaxTurns)); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to dispatch auto mode enable")
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not enable auto mode.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, automode.RenderStatusMarkdown(automode.NormalizeWithDefault(automode.Status{
			Enabled:  true,
			State:    automode.StateIdle,
			MaxTurns: h.autoMaxTurns,
		}, h.autoMaxTurns)))
	case autoActionOff:
		if err := dispatchAutoStateUpdate(ctx, h.actorDispatcher, commandCtx.Locator, automode.DisableState()); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to dispatch auto mode disable")
			return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not disable auto mode.")
		}
		return sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, automode.RenderStatusMarkdown(automode.DefaultStatusWithMaxTurns(h.autoMaxTurns)))
	default:
		return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /auto\n/auto on\n/auto off")
	}
}

func (h *CommandHandler) onUsageCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /usage"); err != nil {
			return err
		}
		return nil
	}
	snapshot, ok, err := loadUsageSnapshot(ctx, h.sessionManager, commandCtx.Locator)
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to load usage snapshot")
	}
	if err != nil || !ok {
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "No provider usage has been recorded for this session yet."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, renderUsageSnapshot(snapshot))
}

func (h *CommandHandler) onGoalCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	objective := strings.TrimSpace(commandCtx.Args)
	if objective == "" {
		if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage:\n/goalkeeper <objective>\n/goalkeeper clear"); err != nil {
			return err
		}
		return nil
	}
	if strings.EqualFold(objective, "clear") {
		if h.actorDispatcher == nil {
			if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Goal control is unavailable right now. Please try again."); err != nil {
				return err
			}
			return nil
		}
		if err := submitGoalClearControl(ctx, h.actorDispatcher, commandCtx.Locator, telegramref.UserID(commandCtx.UserID), "goal cleared by user", true); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish goal clear command")
			if sendErr := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not clear goal run."); sendErr != nil {
				return sendErr
			}
		}
		return nil
	}

	started, err := h.submitGoalJobWithOptions(ctx, commandCtx.Locator, commandCtx.DeliveryOptions, objective, telegramref.UserID(commandCtx.UserID))
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to start /goalkeeper run")
		if sendErr := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not start goal run."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if !started {
		if err := sendAgentReply(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "A goal run is already active for this session."); err != nil {
			return err
		}
		return nil
	}

	return nil
}

func (h *CommandHandler) onResetCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	commandName := commandCtx.Command
	if commandName == "" {
		commandName = commandReset
	}

	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, fmt.Sprintf("Usage: /%s", commandName)); err != nil {
			return err
		}
		return nil
	}

	info, infoErr := h.sessionManager.GetSessionInfo(ctx, commandCtx.Locator.SessionID)
	if infoErr != nil {
		log.Debug().Err(infoErr).Str("session_id", commandCtx.Locator.SessionID).Str("command", commandName).Msg("session info unavailable before restart")
	}
	h.cancelSessionWork(ctx, commandCtx.Locator, fmt.Sprintf("session canceled by %s command", commandName), commandName)
	if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Str("command", commandName).Msg("failed to reset session during session restart command")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not reset this session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	label := restartSessionLabel(commandCtx, info)
	userID := restartSessionUserID(commandCtx, info)
	if err := h.sessionManager.CreateSession(ctx, session.SessionContext{
		Locator: commandCtx.Locator,
		UserID:  userID,
	}, label); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Str("command", commandName).Msg("failed to recreate session during session restart command")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not restart this session."); sendErr != nil {
			return sendErr
		}
		return nil
	}

	baldaProviderID := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	metadata := h.sessionManager.GetAgentMetadata(baldaProviderID)
	welcomeName := restartWelcomeDisplayName(commandCtx, label)
	welcomeMsg := welcome.BuildAgentWelcomeMessage(welcomeName, commandCtx.Locator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, welcomeMsg); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Str("command", commandName).Msg("failed to send restart welcome")
	}
	h.sendSessionStartupNotice(ctx, commandCtx.Locator, commandCtx.Locator.SessionID)
	return nil
}

func (h *CommandHandler) cancelSessionWork(ctx context.Context, locator session.SessionLocator, reason string, commandName string) {
	if h.workCanceller == nil {
		return
	}
	if err := h.workCanceller.CancelWork(ctx, locator, "command."+commandName, reason); err != nil {
		log.Warn().Err(err).Str("session_id", locator.SessionID).Str("command", commandName).Msg("failed to synchronously cancel session work")
	}
}

func (h *CommandHandler) sendSessionStartupNotice(ctx context.Context, locator session.SessionLocator, sessionID string) {
	if h.sessionManager == nil {
		return
	}
	notice := strings.TrimSpace(h.sessionManager.TakeStartupNotice(sessionID))
	if notice == "" {
		return
	}
	if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, locator, notice); err != nil {
		log.Warn().Err(err).Str("session_id", sessionID).Msg("failed to send restart startup notice")
	}
}

func restartSessionLabel(commandCtx TelegramCommandContext, info session.TopicSessionInfo) string {
	if label := strings.TrimSpace(info.AgentName); label != "" {
		return label
	}
	if commandCtx.IsDM && commandCtx.TopicID == 0 {
		return ownerSessionLabel
	}
	return autoSessionLabel
}

func restartSessionUserID(commandCtx TelegramCommandContext, info session.TopicSessionInfo) string {
	if userID := strings.TrimSpace(info.UserID); userID != "" {
		return userID
	}
	return telegramref.UserID(commandCtx.UserID)
}

func restartWelcomeDisplayName(commandCtx TelegramCommandContext, label string) string {
	if !commandCtx.IsDM {
		return ownerSessionLabel
	}
	return label
}

func (h *CommandHandler) onTopicCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	topicName := strings.TrimSpace(commandCtx.Args)
	if topicName == "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /topic <name>"); err != nil {
			return err
		}
		return nil
	}
	baldaProviderID := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if baldaProviderID == "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Balda is not ready right now."); err != nil {
			return err
		}
		return nil
	}

	log.Info().
		Int64("user_id", commandCtx.UserID).
		Int64("chat_id", commandCtx.ChatID).
		Str("topic_name", topicName).
		Msg("creating topic session")

	topicLocator, err := h.channel.CreateTopicLocator(ctx, commandCtx.ChatID, fmt.Sprintf("Balda: %s", topicName))
	if err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not create topic session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.sessionManager.CreateSession(ctx, session.SessionContext{
		Locator: topicLocator,
		UserID:  telegramref.UserID(commandCtx.UserID),
	}, topicName); err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic session after topic creation")
		_ = h.channel.Close(ctx, topicLocator)
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not create topic session."); sendErr != nil {
			return sendErr
		}
		return nil
	}

	metadata := h.sessionManager.GetAgentMetadata(baldaProviderID)

	welcomeMsg := welcome.BuildAgentWelcomeMessage(topicName, topicLocator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := sendMarkdown(ctx, h.actorDispatcher, commandHandlerActorAddress, topicLocator, welcomeMsg); err != nil {
		log.Error().Err(err).Msg("failed to send welcome message")
		return err
	}

	return nil
}

func (h *CommandHandler) onLocatorCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /locator"); err != nil {
			return err
		}
		return nil
	}

	ref := locatorref.Format(commandCtx.Locator)
	message := fmt.Sprintf("Transport: %s\nLocator: %s\n\nUse in scheduler/webhook config:\ntarget: locator\nkey: %s", commandCtx.Locator.ChannelType, ref, ref)
	return sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, message)
}

func (h *CommandHandler) onCloseCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /close"); err != nil {
			return err
		}
		return nil
	}

	if commandCtx.TopicID > 0 {
		if err := submitSessionCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, telegramref.UserID(commandCtx.UserID), "session canceled by close command", false); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish /close cancel control command")
		}
		if err := resetSessionWithReason(ctx, h.sessionManager, commandCtx.Locator, session.BoundaryReasonClose); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset session during /close")
			if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not close this topic."); sendErr != nil {
				return sendErr
			}
			return nil
		}
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Closing this topic and resetting session history."); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to send /close confirmation")
		}
		if err := h.channel.Close(ctx, commandCtx.Locator); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to close topic")
		}
		return nil
	}

	if err := submitSessionCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, telegramref.UserID(commandCtx.UserID), "session canceled by close command", false); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish /close cancel control command")
	}
	if err := resetSessionWithReason(ctx, h.sessionManager, commandCtx.Locator, session.BoundaryReasonClose); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset main dm session during /close")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not reset this session."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Session history reset."); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Msg("failed to send /close main dm session confirmation")
	}
	return nil
}

func (h *CommandHandler) onCancelCommand(ctx context.Context, commandCtx TelegramCommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Usage: /cancel"); err != nil {
			return err
		}
		return nil
	}

	if h.actorDispatcher == nil {
		if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Cancel is unavailable right now. Please try again."); err != nil {
			return err
		}
		return nil
	}
	if err := submitSessionTurnCancelControl(ctx, h.actorDispatcher, commandCtx.Locator, telegramref.UserID(commandCtx.UserID), "session turn canceled by user", true); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to publish cancel command")
		if sendErr := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Could not request cancel."); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := sendPlain(ctx, h.actorDispatcher, commandHandlerActorAddress, commandCtx.Locator, "Cancel requested."); err != nil {
		return err
	}
	return nil
}

func (h *CommandHandler) canUseSessionCommand(ctx context.Context, userID int64) bool {
	if h.ownerStore != nil && h.ownerStore.IsOwner(userID) {
		return true
	}
	if h.collaboratorStore == nil {
		return false
	}
	_, found, err := h.collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	if err != nil {
		log.Warn().Err(err).Int64("user_id", userID).Msg("failed to check collaborator access")
		return false
	}
	return found
}
