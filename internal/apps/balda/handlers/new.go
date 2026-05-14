package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/auth"
	relaytelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/memory"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	"github.com/normahq/balda/internal/apps/balda/session"
	relaystate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	relaywelcome "github.com/normahq/balda/internal/apps/balda/welcome"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

type commandSessionManager interface {
	CreateSession(ctx context.Context, sessionCtx session.SessionContext, agentName string) error
	GetAgentMetadata(agentName string) session.AgentMetadata
	RelayProviderID() string
	ResetSession(ctx context.Context, locator session.SessionLocator) error
	StopSession(locator session.SessionLocator)
}

const (
	cronActionAdd    = "add"
	cronActionList   = "list"
	cronActionPause  = "pause"
	cronActionRemove = "remove"
	cronActionResume = "resume"
)

// CommandHandler handles balda commands like /topic and /close.
type CommandHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	channel           *relaytelegram.Adapter
	sessionManager    commandSessionManager
	turnDispatcher    turnQueue
	goalRunner        goalCommandRunner
	messenger         *messenger.Messenger
	userHandler       *userHandler
	memoryStore       *memory.Store
	jobStore          relaystate.ScheduledJobStore
	now               func() time.Time
}

func BuildAgentWelcomeMessage(name, sessionID, agentType, model string, mcpServers []string) string {
	return relaywelcome.BuildAgentWelcomeMessage(name, sessionID, agentType, model, mcpServers)
}

type commandHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           *relaytelegram.Adapter
	SessionManager    *session.Manager
	TurnDispatcher    *TurnDispatcher
	GoalRunner        *GoalRunner
	Messenger         *messenger.Messenger
	UserHandler       *userHandler
	MemoryStore       *memory.Store
	StateProvider     relaystate.Provider
}

// NewCommandHandler creates a new balda command handler.
func NewCommandHandler(params commandHandlerParams) *CommandHandler {
	var jobStore relaystate.ScheduledJobStore
	if params.StateProvider != nil {
		jobStore = params.StateProvider.ScheduledJobs()
	}

	return &CommandHandler{
		ownerStore:        params.OwnerStore,
		collaboratorStore: params.CollaboratorStore,
		channel:           params.Channel,
		sessionManager:    params.SessionManager,
		turnDispatcher:    params.TurnDispatcher,
		goalRunner:        params.GoalRunner,
		messenger:         params.Messenger,
		userHandler:       params.UserHandler,
		memoryStore:       params.MemoryStore,
		jobStore:          jobStore,
		now:               time.Now,
	}
}

// Register registers the handler with the registry.
func (h *CommandHandler) Register(registry tgbotkit.Registry) {
	registry.OnCommand(h.onCommand)
}

func (h *CommandHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
	commandCtx, ok := h.channel.CommandContextFromEvent(event)
	if !ok {
		return nil
	}

	switch commandCtx.Command {
	case "topic":
		return h.onTopicCommand(ctx, commandCtx)
	case "close":
		return h.onCloseCommand(ctx, commandCtx)
	case "reset":
		return h.onResetCommand(ctx, commandCtx)
	case "cancel":
		return h.onCancelCommand(ctx, commandCtx)
	case "goal":
		return h.onGoalCommand(ctx, commandCtx)
	case "memory":
		return h.onMemoryCommand(ctx, commandCtx)
	case "cron":
		return h.onCronCommand(ctx, commandCtx)
	case "user":
		// Route to UserHandler
		return h.userHandler.HandleUserCommand(ctx, commandCtx)
	default:
		return nil
	}
}

func (h *CommandHandler) onCronCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if h.jobStore == nil {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cron jobs are unavailable right now. Please try again."); err != nil {
			return err
		}
		return nil
	}
	action, fields := parseCronAction(commandCtx.Args)
	switch action {
	case cronActionAdd:
		return h.onCronAddCommand(ctx, commandCtx, fields)
	case cronActionList:
		return h.onCronListCommand(ctx, commandCtx, fields)
	case cronActionPause:
		return h.onCronSetStatusCommand(ctx, commandCtx, fields, relaystate.ScheduledJobStatusPaused, cronActionPause, "paused")
	case cronActionRemove:
		return h.onCronRemoveCommand(ctx, commandCtx, fields)
	case cronActionResume:
		return h.onCronSetStatusCommand(ctx, commandCtx, fields, relaystate.ScheduledJobStatusActive, cronActionResume, "resumed")
	default:
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /cron add <schedule> <prompt>\nUsage: /cron list\nUsage: /cron remove <job_id>\nUsage: /cron pause <job_id>\nUsage: /cron resume <job_id>"); err != nil {
			return err
		}
		return nil
	}
}

func (h *CommandHandler) onCronAddCommand(
	ctx context.Context,
	commandCtx relaytelegram.CommandContext,
	fields []string,
) error {
	scheduleSpec, prompt, err := parseCronAddArgs(fields)
	if err != nil {
		if usageErr := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /cron add <schedule> <prompt>"); usageErr != nil {
			return usageErr
		}
		return nil
	}

	interval, err := scheduleInterval(scheduleSpec)
	if err != nil {
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Invalid schedule: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}

	now := h.now().UTC()
	jobID := fmt.Sprintf("cron-%s-%d", strings.ReplaceAll(commandCtx.Locator.SessionID, ":", "-"), now.UnixNano())
	nextRunAt := now.Add(interval)
	record := relaystate.ScheduledJobRecord{
		JobID:        jobID,
		SessionID:    commandCtx.Locator.SessionID,
		ChannelType:  commandCtx.Locator.ChannelType,
		AddressKey:   commandCtx.Locator.AddressKey,
		AddressJSON:  commandCtx.Locator.AddressJSON,
		Prompt:       prompt,
		ScheduleSpec: scheduleSpec,
		Timezone:     "UTC",
		Status:       relaystate.ScheduledJobStatusActive,
		MaxRetries:   3,
		NextRunAt:    nextRunAt,
	}
	if err := h.jobStore.Upsert(ctx, record); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("failed to create scheduled job")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to create cron job: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}

	return h.channel.SendPlain(
		ctx,
		commandCtx.Locator,
		fmt.Sprintf("Scheduled job created.\nID: %s\nSchedule: %s\nNext run: %s", jobID, scheduleSpec, nextRunAt.Format(time.RFC3339)),
	)
}

func (h *CommandHandler) onCronListCommand(
	ctx context.Context,
	commandCtx relaytelegram.CommandContext,
	fields []string,
) error {
	if len(fields) != 0 {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /cron list"); err != nil {
			return err
		}
		return nil
	}

	jobs, err := h.jobStore.ListByAddress(ctx, commandCtx.Locator.ChannelType, commandCtx.Locator.AddressKey)
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to list cron jobs")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to list cron jobs: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if len(jobs) == 0 {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "No cron jobs for this session."); err != nil {
			return err
		}
		return nil
	}

	lines := make([]string, 0, len(jobs)+1)
	lines = append(lines, "Cron jobs for this session:")
	for _, job := range jobs {
		lines = append(lines, fmt.Sprintf(
			"- %s | %s | %s | next: %s",
			job.JobID,
			strings.TrimSpace(job.Status),
			strings.TrimSpace(job.ScheduleSpec),
			job.NextRunAt.UTC().Format(time.RFC3339),
		))
	}

	return h.channel.SendPlain(ctx, commandCtx.Locator, strings.Join(lines, "\n"))
}

func (h *CommandHandler) onCronRemoveCommand(
	ctx context.Context,
	commandCtx relaytelegram.CommandContext,
	fields []string,
) error {
	if len(fields) != 1 {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /cron remove <job_id>"); err != nil {
			return err
		}
		return nil
	}
	jobID := strings.TrimSpace(fields[0])
	job, ok, err := h.jobStore.GetByID(ctx, jobID)
	if err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("failed to lookup cron job")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to remove cron job: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if !ok || job.ChannelType != commandCtx.Locator.ChannelType || job.AddressKey != commandCtx.Locator.AddressKey {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cron job not found for this session."); err != nil {
			return err
		}
		return nil
	}
	if err := h.jobStore.Delete(ctx, jobID); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("failed to delete cron job")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to remove cron job: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	return h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Cron job removed: %s", jobID))
}

func (h *CommandHandler) onCronSetStatusCommand(
	ctx context.Context,
	commandCtx relaytelegram.CommandContext,
	fields []string,
	status string,
	commandName string,
	statusLabel string,
) error {
	if len(fields) != 1 {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Usage: /cron %s <job_id>", commandName)); err != nil {
			return err
		}
		return nil
	}
	jobID := strings.TrimSpace(fields[0])
	job, ok, err := h.jobStore.GetByID(ctx, jobID)
	if err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Str("status", status).Msg("failed to lookup cron job")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to update cron job: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if !ok || job.ChannelType != commandCtx.Locator.ChannelType || job.AddressKey != commandCtx.Locator.AddressKey {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cron job not found for this session."); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(job.Status) == status {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Cron job already %s: %s", statusLabel, jobID)); err != nil {
			return err
		}
		return nil
	}

	job.Status = status
	if err := h.jobStore.Upsert(ctx, job); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Str("status", status).Msg("failed to update cron job status")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to update cron job: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	return h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Cron job %s: %s", statusLabel, jobID))
}

func parseCronAction(raw string) (action string, fields []string) {
	fields = strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "", nil
	}
	return strings.ToLower(fields[0]), fields[1:]
}

func parseCronAddArgs(fields []string) (scheduleSpec string, prompt string, err error) {
	if len(fields) < 2 {
		return "", "", fmt.Errorf("insufficient arguments")
	}
	if fields[0] == "@every" {
		if len(fields) < 3 {
			return "", "", fmt.Errorf("missing prompt")
		}
		scheduleSpec = "@every " + fields[1]
		prompt = strings.TrimSpace(strings.Join(fields[2:], " "))
	} else {
		scheduleSpec = fields[0]
		prompt = strings.TrimSpace(strings.Join(fields[1:], " "))
	}
	if prompt == "" {
		return "", "", fmt.Errorf("prompt is required")
	}
	return strings.TrimSpace(scheduleSpec), prompt, nil
}

func (h *CommandHandler) onGoalCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	objective := strings.TrimSpace(commandCtx.Args)
	if objective == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /goal <objective>"); err != nil {
			return err
		}
		return nil
	}

	if h.goalRunner == nil {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Goal runs are unavailable right now. Please try again."); err != nil {
			return err
		}
		return nil
	}

	started, err := h.goalRunner.Start(ctx, commandCtx.Locator, objective, relaytelegram.UserID(commandCtx.UserID))
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to start /goal run")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to start goal run: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if !started {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "A goal run is already active for this session."); err != nil {
			return err
		}
		return nil
	}

	return nil
}

func (h *CommandHandler) onTopicCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	topicName := strings.TrimSpace(commandCtx.Args)
	if topicName == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /topic <name>"); err != nil {
			return err
		}
		return nil
	}
	relayProviderID := strings.TrimSpace(h.sessionManager.RelayProviderID())
	if relayProviderID == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "balda.provider is not configured."); err != nil {
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
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to create topic session: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.sessionManager.CreateSession(ctx, session.SessionContext{
		Locator: topicLocator,
		UserID:  relaytelegram.UserID(commandCtx.UserID),
	}, topicName); err != nil {
		log.Error().Err(err).Str("topic_name", topicName).Msg("failed to create topic session after topic creation")
		_ = h.channel.Close(ctx, topicLocator)
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to create topic session: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}

	metadata := h.sessionManager.GetAgentMetadata(relayProviderID)

	welcomeMsg := BuildAgentWelcomeMessage(topicName, topicLocator.SessionID, metadata.Type, metadata.Model, metadata.MCPServers)
	if err := h.channel.SendMarkdown(ctx, topicLocator, welcomeMsg); err != nil {
		log.Error().Err(err).Msg("failed to send welcome message")
		return err
	}

	return nil
}

func (h *CommandHandler) onMemoryCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}
	if !commandCtx.IsDM {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /memory"); err != nil {
			return err
		}
		return nil
	}
	if h.memoryStore == nil || !h.memoryStore.MemoryEnabled() {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Memory is disabled."); err != nil {
			return err
		}
		return nil
	}
	content, err := h.memoryStore.ReadMemory(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to read balda memory")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to read memory: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Memory is empty."); err != nil {
			return err
		}
		return nil
	}
	return h.sendPlainChunks(ctx, commandCtx.Locator, content)
}

func (h *CommandHandler) sendPlainChunks(ctx context.Context, locator session.SessionLocator, text string) error {
	const maxPlainChunkRunes = 3500
	runes := []rune(text)
	for len(runes) > 0 {
		n := len(runes)
		if n > maxPlainChunkRunes {
			n = maxPlainChunkRunes
		}
		chunk := strings.TrimSpace(string(runes[:n]))
		if chunk != "" {
			if err := h.channel.SendPlain(ctx, locator, chunk); err != nil {
				return err
			}
		}
		runes = runes[n:]
	}
	return nil
}

func (h *CommandHandler) onCloseCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if !commandCtx.IsDM {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "This command is only available in direct messages."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /close"); err != nil {
			return err
		}
		return nil
	}

	if commandCtx.TopicID > 0 {
		if h.turnDispatcher != nil {
			_, _, _ = h.turnDispatcher.CancelSession(commandCtx.Locator, true)
		}
		if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
			log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset session during /close")
			if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to reset this session before close: %v", err)); sendErr != nil {
				return sendErr
			}
			return nil
		}
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Closing this topic and resetting session history."); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to send /close confirmation")
		}
		if err := h.channel.Close(ctx, commandCtx.Locator); err != nil {
			log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Int("topic_id", commandCtx.TopicID).Msg("failed to close topic")
		}
		return nil
	}

	if h.turnDispatcher != nil {
		_, _, _ = h.turnDispatcher.CancelSession(commandCtx.Locator, true)
	}
	if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset owner session during /close")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to reset this session: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Session history reset. The balda provider session will be recreated on your next message."); err != nil {
		log.Warn().Err(err).Int64("chat_id", commandCtx.ChatID).Msg("failed to send /close owner session confirmation")
	}
	return nil
}

func (h *CommandHandler) onResetCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /reset"); err != nil {
			return err
		}
		return nil
	}

	if h.turnDispatcher != nil {
		_, _, _ = h.turnDispatcher.CancelSession(commandCtx.Locator, true)
	}
	if err := h.sessionManager.ResetSession(ctx, commandCtx.Locator); err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to reset session")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to reset this session: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Session history reset. Send a new message to start fresh in this chat."); err != nil {
		return err
	}
	return nil
}

func (h *CommandHandler) onCancelCommand(ctx context.Context, commandCtx relaytelegram.CommandContext) error {
	if !h.canUseSessionCommand(ctx, commandCtx.UserID) {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Only the bot owner or collaborators can use this command."); err != nil {
			return err
		}
		return nil
	}

	if strings.TrimSpace(commandCtx.Args) != "" {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Usage: /cancel"); err != nil {
			return err
		}
		return nil
	}

	if h.turnDispatcher == nil {
		if h.goalRunner == nil {
			if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Cancel is unavailable right now. Please try again."); err != nil {
				return err
			}
			return nil
		}
		if h.goalRunner.Cancel(commandCtx.Locator) {
			if err := h.channel.SendPlain(ctx, commandCtx.Locator, "Canceled active goal run."); err != nil {
				return err
			}
			return nil
		}
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "No running or queued turns for this session."); err != nil {
			return err
		}
		return nil
	}

	hadInFlight, dropped, err := h.turnDispatcher.CancelSession(commandCtx.Locator, true)
	if err != nil {
		log.Warn().Err(err).Str("session_id", commandCtx.Locator.SessionID).Msg("failed to cancel session turns")
		if sendErr := h.channel.SendPlain(ctx, commandCtx.Locator, fmt.Sprintf("Failed to cancel current turn: %v", err)); sendErr != nil {
			return sendErr
		}
		return nil
	}

	goalCanceled := false
	if h.goalRunner != nil {
		goalCanceled = h.goalRunner.Cancel(commandCtx.Locator)
	}

	if !hadInFlight && dropped == 0 && !goalCanceled {
		if err := h.channel.SendPlain(ctx, commandCtx.Locator, "No running or queued turns for this session."); err != nil {
			return err
		}
		return nil
	}

	response := "Canceled current turn."
	if !hadInFlight {
		response = "No running turn to cancel."
	}
	if dropped > 0 {
		response = fmt.Sprintf("%s Dropped %d queued message(s).", response, dropped)
	}
	if goalCanceled {
		response = fmt.Sprintf("%s Canceled active goal run.", response)
	}
	if err := h.channel.SendPlain(ctx, commandCtx.Locator, response); err != nil {
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
