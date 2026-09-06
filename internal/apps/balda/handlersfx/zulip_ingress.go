package handlersfx

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/usageview"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var zulipHandlerActorAddress = actorlayer.ActorAddress{Target: "channel", Key: "zulip"}

const (
	zulipSessionIDPrefix          = "zulip"
	zulipDirectMessageOnlyText    = "This command is only available in direct messages."
	zulipNotReadyText             = "Balda is not ready right now."
	zulipResetNotReadyText        = "Balda is not ready right now. Please try again."
	zulipAccessDeniedText         = "Only the bot owner or collaborators can use this bot."
	zulipCancelUsageText          = "Usage: /cancel"
	zulipLocatorUsageText         = "Usage: /locator"
	ownerAlreadyRegisteredMessage = "You are already registered as the bot owner."

	commandStart    = "start"
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

	userActionAdd    = "add"
	userActionInvite = "invite"
	userActionList   = "list"
	userActionRemove = "remove"

	defaultGoalMaxIterations = 25
)

type zulipInboundHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	InviteStore       *auth.InviteStore
	CollaboratorStore *auth.CollaboratorStore
	ChannelAuth       *auth.ChannelAuthService
	SessionManager    *baldasession.Manager
	Dispatcher        actortransport.Dispatcher
	GoalJobs          *baldajobs.JobLifecycleService `optional:"true"`
	MemoryStore       *memory.Store
	AuthToken         string `name:"balda_auth_token"`
	BaldaProviderID   string `name:"balda_provider"`
	MaxIterations     int    `name:"balda_goal_max_iterations"`
	AutoMaxTurns      int    `name:"balda_automode_max_turns"`
	Logger            zerolog.Logger
}

type zulipInboundHandler struct {
	ownerStore        *auth.OwnerStore
	inviteStore       *auth.InviteStore
	collaboratorStore *auth.CollaboratorStore
	channelAuth       *auth.ChannelAuthService
	sessionManager    *baldasession.Manager
	actorDispatcher   actortransport.Dispatcher
	goalJobs          *baldajobs.JobLifecycleService
	memoryStore       *memory.Store
	authToken         string
	baldaProviderName string
	goalMaxIterations int
	autoMaxTurns      int
	logger            zerolog.Logger

	mu      sync.RWMutex
	ownerID int64
	now     func() time.Time
}

func newZulipInboundHandler(params zulipInboundHandlerParams) zulip.InboundProcessor {
	maxIters := params.MaxIterations
	if maxIters <= 0 {
		maxIters = defaultGoalMaxIterations
	}
	h := &zulipInboundHandler{
		ownerStore:        params.OwnerStore,
		inviteStore:       params.InviteStore,
		collaboratorStore: params.CollaboratorStore,
		channelAuth:       params.ChannelAuth,
		sessionManager:    params.SessionManager,
		actorDispatcher:   params.Dispatcher,
		goalJobs:          params.GoalJobs,
		memoryStore:       params.MemoryStore,
		authToken:         strings.TrimSpace(params.AuthToken),
		baldaProviderName: strings.TrimSpace(params.BaldaProviderID),
		goalMaxIterations: maxIters,
		autoMaxTurns:      automode.NormalizeMaxTurns(params.AutoMaxTurns),
		logger:            params.Logger.With().Str("component", "balda.handler.zulip").Logger(),
		now:               time.Now,
	}
	h.initOwnerFromStore()
	return h
}

func (h *zulipInboundHandler) initOwnerFromStore() {
	if h.ownerStore == nil {
		h.logger.Warn().Msg("zulip handler owner store is unavailable")
		return
	}
	ownerID, ok := initZulipOwnerID(h.ownerStore)
	if !ok {
		return
	}
	h.mu.Lock()
	h.ownerID = ownerID
	h.mu.Unlock()
	h.logger.Info().Int64("owner_id", ownerID).Msg("zulip handler owner initialized")
}

func (h *zulipInboundHandler) getOwnerID() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ownerID
}

func (h *zulipInboundHandler) setOwnerID(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ownerID = id
}

func (h *zulipInboundHandler) canAccessCollaboratorScope(ctx context.Context, userID int64) bool {
	ok, err := canAccessZulipCollaboratorScope(ctx, h.ownerStore, h.collaboratorStore, userID)
	if err != nil {
		h.logger.Warn().Err(err).Int64("user_id", userID).Msg("failed to verify collaborator scope access")
		return false
	}
	return ok
}

func (h *zulipInboundHandler) HandleCommand(ctx context.Context, cmd zulip.InboundCommand) error {
	transportUserID := int64(cmd.SenderID)
	if cmd.Command != commandStart && !h.canAccessCollaboratorScope(ctx, transportUserID) {
		_ = h.sendPlain(ctx, cmd.Locator, zulipAccessDeniedText)
		return nil
	}

	switch cmd.Command {
	case commandStart:
		h.handleStartCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Args, cmd.Direct)
	case commandReset, commandRestart:
		h.handleResetCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Command, cmd.Args, cmd.Direct)
	case commandCancel:
		h.handleCancelCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Args)
	case commandLocator:
		h.handleLocatorCommand(ctx, cmd.Locator, cmd.Args)
	case commandTopic:
		h.handleTopicCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Args, cmd.Direct)
	case commandGoal:
		h.handleGoalCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Args)
	case commandUsage:
		h.handleUsageCommand(ctx, cmd.Locator, cmd.Args)
	case commandAuto:
		h.handleAutoCommand(ctx, cmd.Locator, cmd.Args)
	case commandClose:
		h.handleCloseCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Args, cmd.Direct)
	case commandUser:
		h.handleUserCommand(ctx, cmd.Locator, cmd.SenderID, cmd.Args)
	default:
		_ = h.sendPlain(ctx, cmd.Locator, fmt.Sprintf("Unknown command: /%s", cmd.Command))
	}
	return nil
}

func (h *zulipInboundHandler) ProcessInbound(ctx context.Context, msg zulip.InboundMessage) (turncmd.InboundSettlement, error) {
	if h.getOwnerID() == 0 {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, nil
	}
	if strings.TrimSpace(msg.Text) == "" {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, nil
	}
	if msg.Direct {
		if token, ok := firstFieldToken(msg.Text); ok {
			h.handleOwnerBindToken(ctx, msg.Locator, msg.SenderID, token)
			return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, nil
		}
	}

	reqAt := msg.ReceivedAt
	if reqAt.IsZero() {
		if h.now != nil {
			reqAt = h.now()
		} else {
			reqAt = time.Now()
		}
	}

	inbound := zulip.NormalizeInbound(msg.Locator, msg.MessageID, zulipUserID(msg.SenderID), msg.Text, msg.Direct, reqAt)
	service, err := ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(h.authorizeZulipInbound),
		ingressapp.SessionPreparerFunc(h.prepareZulipSession),
		h.actorDispatcher,
		h.logger,
	)
	if err != nil {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}, err
	}
	result, err := service.Process(ctx, inbound)
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		if baldaexecution.IsCommandQueueFull(err) {
			_ = h.sendPlain(ctx, msg.Locator, "Session command queue is full. Please wait or use /cancel.")
			return result.Settlement, err
		}
		h.logger.Error().Err(err).Str("session_id", msg.Locator.SessionID).Msg("zulip: failed to enqueue turn")
		if result.Settlement.Reason != ingressapp.ReasonSessionRejected {
			_ = h.sendPlain(ctx, msg.Locator, "Failed to publish your message for processing. Please try again.")
		}
	}
	return result.Settlement, err
}

func (h *zulipInboundHandler) authorizeZulipInbound(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.Authorization, error) {
	userID, err := parseZulipUserID(inbound.UserID)
	if err != nil {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	allowed := h.canAccessCollaboratorScope(ctx, int64(userID))
	return ingressapp.Authorization{Allowed: allowed, Reason: ingressapp.ReasonUnauthorized}, nil
}

func (h *zulipInboundHandler) prepareZulipSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	ts, err := h.getOrCreateSession(ctx, inboundLocator(inbound), inbound.UserID, h.baldaProviderName, inbound.Direct)
	if err != nil {
		return ingressapp.SessionPreparation{}, err
	}
	return buildZulipSessionPreparation(ts, inbound.UserID), nil
}

func (h *zulipInboundHandler) getOrCreateSession(
	ctx context.Context,
	locator deliverycmd.Locator,
	transportUserID string,
	providerName string,
	isDM bool,
) (*baldasession.TopicSession, error) {
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, "Balda is not ready right now. Please try again.")
		return nil, fmt.Errorf("session manager is unavailable")
	}
	if strings.TrimSpace(providerName) == "" {
		_ = h.sendPlain(ctx, locator, "Balda is not ready right now. Please try again.")
		return nil, fmt.Errorf("no provider configured")
	}
	ts, welcomed, err := ensureZulipSession(
		ctx,
		h.sessionManager,
		locator,
		transportUserID,
		providerName,
		isDM,
		ownerSessionLabel,
		autoSessionLabel,
	)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("zulip: failed to restore session")
		_ = h.sendPlain(ctx, locator, "Could not restore this session. Please try again.")
		return nil, err
	}
	if welcomed {
		welcomeMsg := buildZulipSessionWelcome(h.sessionManager, providerName, isDM, ts.GetSessionID(), ownerSessionLabel, autoSessionLabel)
		_ = h.sendPlain(ctx, locator, welcomeMsg)
	}
	return ts, nil
}

func (h *zulipInboundHandler) handleAutoCommand(ctx context.Context, locator deliverycmd.Locator, args string) {
	_ = h.sendPlain(ctx, locator, PlainAutoCommandReply(ctx, h.sessionManager, h.actorDispatcher, locator, args, "Usage: /auto [on|off]", h.now(), h.autoMaxTurns))
}

func (h *zulipInboundHandler) handleStartCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, args string, isDM bool) {
	if !isDM {
		_ = h.sendPlain(ctx, locator, zulipDirectMessageOnlyText)
		return
	}
	if strings.TrimSpace(args) == "" {
		ownerID := h.getOwnerID()
		if ownerID != 0 {
			if h.ownerStore != nil && h.ownerStore.IsOwnerSubject(auth.ZulipSubject(senderID)) {
				msg := ownerAlreadyRegisteredMessage
				if bundle, ok := ownerBindTokenBundleMessage(ctx, h.channelAuth, fmt.Sprintf("%d", senderID)); ok {
					msg = startOwnerAlreadyRegisteredSelfMessage(bundle)
				}
				_ = h.sendPlain(ctx, locator, msg)
				return
			}
			_ = h.sendPlain(ctx, locator, "Bot owner is already registered.")
			return
		}
		_ = h.sendPlain(ctx, locator, startWelcomeMessage())
		return
	}

	startArgs, ok := parseZulipStartArgs(args)
	if !ok {
		_ = h.sendPlain(ctx, locator, startInvalidFormatMessage())
		return
	}
	switch startArgs.Mode {
	case "owner":
		h.handleStartOwnerCommand(ctx, locator, senderID, startArgs.Token)
	case "invite":
		h.handleStartInviteCommand(ctx, locator, senderID, startArgs.Token)
	case "channel_token":
		h.handleStartChannelTokenCommand(ctx, locator, senderID, startArgs.Token)
	default:
		_ = h.sendPlain(ctx, locator, startInvalidFormatMessage())
	}
}

func (h *zulipInboundHandler) handleStartOwnerCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, token string) {
	if h.ownerStore == nil {
		h.logger.Error().Msg("owner store is unavailable")
		_ = h.sendPlain(ctx, locator, "Could not register owner. Ask the operator to check Balda storage configuration.")
		return
	}
	if h.ownerStore.HasOwner() {
		if h.ownerStore.IsOwnerSubject(auth.ZulipSubject(senderID)) {
			msg := ownerAlreadyRegisteredMessage
			if bundle, ok := ownerBindTokenBundleMessage(ctx, h.channelAuth, fmt.Sprintf("%d", senderID)); ok {
				msg = startOwnerAlreadyRegisteredSelfMessage(bundle)
			}
			_ = h.sendPlain(ctx, locator, msg)
			return
		}
		_ = h.sendPlain(ctx, locator, "Bot owner is already registered.")
		return
	}
	if h.authToken == "" {
		h.logger.Error().Msg("auth token is not configured")
		_ = h.sendPlain(ctx, locator, "Invalid authentication token. Please try again.")
		return
	}
	registered, err := registerZulipOwner(h.ownerStore, senderID, h.authToken, token)
	if err != nil {
		h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("failed to register owner")
		_ = h.sendPlain(ctx, locator, "Invalid authentication token. Please try again.")
		return
	}
	if !registered {
		_ = h.sendPlain(ctx, locator, "Bot owner is already registered.")
		return
	}
	h.setOwnerID(int64(senderID))
	ts, _, err := h.bootstrapOwnerSession(ctx, int64(senderID))
	if err != nil {
		h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("failed to bootstrap owner session")
	}
	successMsg := "You are now registered as the bot owner."
	if ts != nil {
		welcomeMsg := buildZulipSessionWelcome(h.sessionManager, h.baldaProviderName, true, ts.GetSessionID(), ownerSessionLabel, autoSessionLabel)
		successMsg = fmt.Sprintf("%s\n\n%s", successMsg, welcomeMsg)
	}
	if bundle, ok := ownerBindTokenBundleMessage(ctx, h.channelAuth, fmt.Sprintf("%d", senderID)); ok {
		successMsg = fmt.Sprintf("%s\n\n%s", successMsg, bundle)
	}
	_ = h.sendPlain(ctx, locator, successMsg)
}

func (h *zulipInboundHandler) handleStartInviteCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, token string) {
	if h.ownerStore == nil {
		_ = h.sendPlain(ctx, locator, "Failed to process invite. Ask the operator to check Balda storage configuration.")
		return
	}
	msg, err := consumeZulipInvite(ctx, h.ownerStore, h.inviteStore, h.collaboratorStore, senderID, token)
	if err != nil {
		h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("failed to process invite")
		_ = h.sendPlain(ctx, locator, "Failed to process invite. Ask the operator to check Balda storage configuration.")
		return
	}
	if msg == "Welcome! You are now a bot collaborator." {
		if _, _, err := h.ensureSession(ctx, locator, zulipUserID(senderID), true); err != nil {
			h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("failed to bootstrap collaborator session")
		}
	}
	_ = h.sendPlain(ctx, locator, msg)
}

func (h *zulipInboundHandler) handleStartChannelTokenCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, token string) {
	if h.channelAuth == nil {
		_ = h.sendPlain(ctx, locator, "Token authentication is unavailable right now.")
		return
	}
	bound, err := consumeZulipOwnerBindToken(ctx, h.channelAuth, senderID, token)
	if err != nil {
		h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("failed to process owner bind token")
		_ = h.sendPlain(ctx, locator, "Failed to process token. Please try again.")
		return
	}
	if !bound {
		_ = h.sendPlain(ctx, locator, "This token is invalid or has expired.")
		return
	}
	h.initOwnerFromStore()
	if _, _, err := h.bootstrapOwnerSession(ctx, int64(senderID)); err != nil {
		h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("failed to bootstrap owner session after bind")
	}
	_ = h.sendPlain(ctx, locator, "This Zulip account is now connected to the Balda owner.")
}

func (h *zulipInboundHandler) handleOwnerBindToken(ctx context.Context, locator deliverycmd.Locator, senderID int, token string) {
	h.handleStartChannelTokenCommand(ctx, locator, senderID, token)
}

func (h *zulipInboundHandler) handleResetCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, cmd, args string, isDM bool) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, fmt.Sprintf("Usage: /%s", cmd))
		return
	}
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, zulipResetNotReadyText)
		return
	}
	info, infoErr := h.sessionManager.GetSessionInfo(ctx, locator.SessionID)
	if infoErr != nil {
		h.logger.Debug().Err(infoErr).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("zulip: session info unavailable before restart")
	}
	transportUserID := zulipUserID(senderID)
	reason := fmt.Sprintf("session canceled by %s command", cmd)
	if submitErr := submitSessionCancelControl(
		ctx, h.actorDispatcher, locator, transportUserID, reason, false,
	); submitErr != nil {
		h.logger.Warn().Err(submitErr).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("failed to submit cancel control")
	}
	if err := h.sessionManager.ResetSession(ctx, locator); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to reset session")
		_ = h.sendPlain(ctx, locator, "Could not reset this session.")
		return
	}
	label := restartZulipSessionLabel(isDM, info, ownerSessionLabel, autoSessionLabel)
	userID := restartZulipSessionUserID(senderID, info)
	if err := h.sessionManager.CreateSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  userID,
	}, label); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("zulip: failed to recreate session during restart command")
		failMsg := "Could not reset this session."
		if cmd == commandRestart {
			failMsg = "Could not restart this session."
		}
		_ = h.sendPlain(ctx, locator, failMsg)
		return
	}

	providerName := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	welcomeMsg := buildZulipRestartWelcome(h.sessionManager, providerName, isDM, label, locator.SessionID, ownerSessionLabel)
	if err := h.sendMarkdown(ctx, locator, welcomeMsg); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("zulip: failed to send restart welcome")
	}
	h.sendSessionStartupNotice(ctx, locator, locator.SessionID)
}

func (h *zulipInboundHandler) sendSessionStartupNotice(ctx context.Context, locator deliverycmd.Locator, sessionID string) {
	if h.sessionManager == nil {
		return
	}
	notice := h.sessionManager.TakeStartupNotice(sessionID)
	if notice == "" {
		return
	}
	_ = h.sendPlain(ctx, locator, notice)
}

func (h *zulipInboundHandler) handleCancelCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, args string) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, zulipCancelUsageText)
		return
	}
	transportUserID := zulipUserID(senderID)
	if submitErr := SubmitSessionTurnCancelControl(
		ctx, h.actorDispatcher, locator, transportUserID, "zulip: session canceled by /cancel", true,
	); submitErr != nil {
		h.logger.Warn().Err(submitErr).Str("session_id", locator.SessionID).Msg("failed to submit cancel control")
		_ = h.sendPlain(ctx, locator, "Could not request cancel.")
		return
	}
	_ = h.sendPlain(ctx, locator, "Cancel requested.")
}

func (h *zulipInboundHandler) handleLocatorCommand(ctx context.Context, locator deliverycmd.Locator, args string) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, zulipLocatorUsageText)
		return
	}
	_ = h.sendPlain(ctx, locator, buildZulipLocatorMessage(locator))
}

func (h *zulipInboundHandler) handleTopicCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, args string, isDM bool) {
	if isDM {
		_ = h.sendPlain(ctx, locator, "This command is only available in stream messages.")
		return
	}
	topicName := strings.TrimSpace(args)
	if topicName == "" {
		_ = h.sendPlain(ctx, locator, "Usage: /topic <name>")
		return
	}
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, zulipNotReadyText)
		return
	}
	baldaProviderID := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if baldaProviderID == "" {
		_ = h.sendPlain(ctx, locator, zulipNotReadyText)
		return
	}
	streamID, ok := locatorref.ZulipStreamID(locator)
	if !ok {
		_ = h.sendPlain(ctx, locator, "Could not determine stream ID from current context.")
		return
	}
	topicLocator := newZulipStreamLocator(streamID, topicName)
	transportUserID := zulipUserID(senderID)
	if err := h.sessionManager.CreateSession(ctx, baldasession.SessionContext{
		Locator: topicLocator,
		UserID:  transportUserID,
	}, topicName); err != nil {
		h.logger.Error().Err(err).Str("topic_name", topicName).Msg("failed to create zulip topic session")
		_ = h.sendPlain(ctx, locator, "Could not create topic session.")
		return
	}
	welcomeMsg := buildZulipTopicWelcome(h.sessionManager, baldaProviderID, topicName, topicLocator.SessionID)
	if err := SendAgentReply(ctx, h.actorDispatcher, zulipHandlerActorAddress, topicLocator, welcomeMsg); err != nil {
		h.logger.Warn().Err(err).Str("topic_name", topicName).Msg("failed to send welcome to new topic")
		_ = h.sendPlain(ctx, locator, fmt.Sprintf("Session created for topic '%s'.", topicName))
		return
	}
	_ = h.sendPlain(ctx, locator, fmt.Sprintf("Session created. Post in topic '%s' to continue.", topicName))
}

func (h *zulipInboundHandler) handleGoalCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, args string) {
	objective := strings.TrimSpace(args)
	if objective == "" {
		_ = h.sendPlain(ctx, locator, "Usage:\n/goalkeeper <objective>\n/goalkeeper clear")
		return
	}
	if strings.EqualFold(objective, "clear") {
		if h.actorDispatcher == nil {
			_ = h.sendPlain(ctx, locator, "Goal control is unavailable right now. Please try again.")
			return
		}
		if err := SubmitGoalClearControl(
			ctx, h.actorDispatcher, locator, zulipUserID(senderID), "goal cleared by user", true,
		); err != nil {
			h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to submit goal clear control")
			_ = h.sendPlain(ctx, locator, "Could not clear goal run.")
		}
		return
	}
	started, err := h.submitGoalJob(ctx, locator, objective, zulipUserID(senderID))
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to start /goalkeeper run")
		_ = h.sendPlain(ctx, locator, "Could not start goal run.")
		return
	}
	if !started {
		_ = h.sendPlain(ctx, locator, "A goal run is already active for this session.")
	}
}

func (h *zulipInboundHandler) submitGoalJob(
	ctx context.Context,
	locator deliverycmd.Locator,
	objective string,
	transportUserID string,
) (bool, error) {
	if h.goalJobs != nil {
		active, err := h.goalJobs.HasActiveGoalJob(ctx, locator.SessionID)
		if err != nil {
			return false, fmt.Errorf("list active goal jobs: %w", err)
		}
		if active {
			return false, nil
		}
	}
	maxIterations := normalizeGoalMaxIterations(h.goalMaxIterations)
	env, err := goalkeepercmd.JobEnvelopeWithOptions(locator, deliveryfmt.Options{
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
		ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: false, PlanUpdates: true},
	}, objective, transportUserID, maxIterations)
	if err != nil {
		return false, err
	}
	if h.actorDispatcher == nil {
		return false, fmt.Errorf("runtime is unavailable")
	}
	if _, err = h.actorDispatcher.Dispatch(ctx, env); err != nil {
		return false, err
	}
	return true, nil
}

func (h *zulipInboundHandler) handleUsageCommand(ctx context.Context, locator deliverycmd.Locator, args string) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, "Usage: /usage")
		return
	}
	snapshot, ok, err := LoadUsageSnapshot(ctx, h.sessionManager, locator)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to load usage snapshot")
	}
	if err != nil || !ok {
		_ = h.sendPlain(ctx, locator, "No provider usage has been recorded for this session yet.")
		return
	}
	_ = h.sendPlain(ctx, locator, usageview.RenderSnapshot(snapshot))
}

func (h *zulipInboundHandler) handleCloseCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, args string, isDM bool) {
	if !isDM {
		_ = h.sendPlain(ctx, locator, zulipDirectMessageOnlyText)
		return
	}
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, "Usage: /close")
		return
	}
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, zulipResetNotReadyText)
		return
	}
	transportUserID := zulipUserID(senderID)
	if submitErr := submitSessionCancelControl(
		ctx, h.actorDispatcher, locator, transportUserID, "session canceled by close command", false,
	); submitErr != nil {
		h.logger.Warn().Err(submitErr).Str("session_id", locator.SessionID).Msg("failed to submit cancel control for /close")
	}
	if err := h.sessionManager.ResetSession(ctx, locator); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to reset session for /close")
		_ = h.sendPlain(ctx, locator, "Could not close this session.")
		return
	}
	_ = h.sendPlain(ctx, locator, "Session history reset.")
}

func (h *zulipInboundHandler) handleUserCommand(ctx context.Context, locator deliverycmd.Locator, senderID int, args string) {
	ownerID := h.getOwnerID()
	if ownerID == 0 || int64(senderID) != ownerID {
		_ = h.sendPlain(ctx, locator, "This command is only for the owner.")
		return
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		_ = h.sendPlain(ctx, locator, UserUsageMessage())
		return
	}
	switch fields[0] {
	case userActionAdd, userActionInvite:
		token, err := createZulipInviteToken(ctx, h.inviteStore, senderID)
		if err != nil {
			h.logger.Error().Err(err).Msg("failed to create invite token")
			_ = h.sendPlain(ctx, locator, "Failed to create invite token.")
			return
		}
		_ = h.sendPlain(ctx, locator, UserInviteMessage(token))
	case userActionList:
		collaborators, invites, err := loadZulipUserListView(ctx, h.collaboratorStore, h.inviteStore)
		if err != nil {
			h.logger.Error().Err(err).Msg("failed to list users")
			_ = h.sendPlain(ctx, locator, "Failed to load user list.")
			return
		}
		_ = h.sendPlain(ctx, locator, UserListMessage(collaborators, invites))
	case userActionRemove:
		if len(fields) < 2 {
			_ = h.sendPlain(ctx, locator, "Usage: /user remove <user_id>")
			return
		}
		removeTarget := strings.TrimSpace(fields[1])
		if err := removeZulipCollaborator(ctx, h.collaboratorStore, removeTarget); err != nil {
			h.logger.Warn().Err(err).Str("target_user_id", removeTarget).Msg("failed to remove collaborator")
			_ = h.sendPlain(ctx, locator, "Could not remove collaborator.")
			return
		}
		_ = h.sendPlain(ctx, locator, UserRemovedMessage(removeTarget))
	default:
		_ = h.sendPlain(ctx, locator, UserUsageMessage())
	}
}

func (h *zulipInboundHandler) bootstrapOwnerSession(ctx context.Context, ownerID int64) (*baldasession.TopicSession, bool, error) {
	locator := newZulipDMLocator(int(ownerID))
	return h.ensureSession(ctx, locator, zulipUserID(int(ownerID)), true)
}

func (h *zulipInboundHandler) ensureSession(ctx context.Context, locator deliverycmd.Locator, transportUserID string, isDM bool) (*baldasession.TopicSession, bool, error) {
	return ensureZulipSession(ctx, h.sessionManager, locator, transportUserID, h.baldaProviderName, isDM, ownerSessionLabel, autoSessionLabel)
}

func (h *zulipInboundHandler) sendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return SendPlain(ctx, h.actorDispatcher, zulipHandlerActorAddress, locator, text)
}

func (h *zulipInboundHandler) sendMarkdown(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return SendMarkdown(ctx, h.actorDispatcher, zulipHandlerActorAddress, locator, text)
}

// Support helpers:

func initZulipOwnerID(store *auth.OwnerStore) (int64, bool) {
	if isNilZulipInterface(store) || !store.HasOwner() {
		return 0, false
	}
	owner := store.GetOwner()
	if owner == nil {
		return 0, false
	}
	for _, subject := range store.OwnerSubjects() {
		value := strings.TrimPrefix(strings.TrimSpace(subject), auth.ChannelZulip+":")
		if value == subject || value == "" {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(value, "%d", &id); err == nil && id > 0 {
			return int64(id), true
		}
	}
	return owner.UserID, true
}

func consumeZulipOwnerBindToken(ctx context.Context, channelAuth *auth.ChannelAuthService, senderID int, token string) (bool, error) {
	if channelAuth == nil {
		return false, fmt.Errorf("token authentication is unavailable")
	}
	return channelAuth.ConsumeOwnerBind(ctx, auth.ChannelZulip, auth.ZulipSubject(senderID), token)
}

func registerZulipOwner(store *auth.OwnerStore, senderID int, expectedToken string, providedToken string) (bool, error) {
	if isNilZulipInterface(store) {
		return false, fmt.Errorf("owner store is unavailable")
	}
	if strings.TrimSpace(providedToken) != strings.TrimSpace(expectedToken) {
		return false, fmt.Errorf("invalid authentication token")
	}
	return store.RegisterOwnerSubject(auth.ZulipSubject(senderID))
}

func firstFieldToken(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 1 {
		return "", false
	}
	token := strings.TrimSpace(fields[0])
	return token, auth.LooksLikeChannelToken(token)
}

func ownerBindTokenBundleMessage(ctx context.Context, authService *auth.ChannelAuthService, createdBy string) (string, bool) {
	if authService == nil {
		return "", false
	}
	tokens, err := authService.CreateMissingOwnerBindTokens(ctx, createdBy)
	if err != nil || len(tokens) == 0 {
		return "", false
	}
	lines := []string{"Connect your other Balda channels:"}
	for _, token := range tokens {
		switch token.Channel {
		case auth.ChannelTelegram:
			lines = append(lines, "", "Telegram:", fmt.Sprintf("DM Balda this command: /start %s", token.Token))
		case auth.ChannelSlack:
			lines = append(lines, "", "Slack:", "DM Balda this token:", token.Token)
		case auth.ChannelZulip:
			lines = append(lines, "", "Zulip:", "DM Balda this token:", token.Token)
		}
	}
	return strings.Join(lines, "\n"), true
}

func canAccessZulipCollaboratorScope(ctx context.Context, ownerStore *auth.OwnerStore, collaboratorStore *auth.CollaboratorStore, userID int64) (bool, error) {
	if !isNilZulipInterface(ownerStore) && ownerStore.IsOwnerSubject(auth.ZulipSubject(int(userID))) {
		return true, nil
	}
	if isNilZulipInterface(collaboratorStore) {
		return false, nil
	}
	_, found, err := collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	return found, err
}

func consumeZulipInvite(ctx context.Context, ownerStore *auth.OwnerStore, inviteStore *auth.InviteStore, collaboratorStore *auth.CollaboratorStore, senderID int, token string) (string, error) {
	userIDStr := fmt.Sprintf("%d", senderID)
	if isNilZulipInterface(ownerStore) {
		return "", fmt.Errorf("owner store is unavailable")
	}
	if ownerStore.IsOwnerSubject(auth.ZulipSubject(senderID)) {
		return "You are already the bot owner.", nil
	}
	if !isNilZulipInterface(collaboratorStore) {
		if _, ok, err := collaboratorStore.GetCollaborator(ctx, userIDStr); err != nil {
			return "", err
		} else if ok {
			return "You are already a collaborator.", nil
		}
	}
	if isNilZulipInterface(inviteStore) || isNilZulipInterface(collaboratorStore) {
		return "", fmt.Errorf("invite flow is unavailable")
	}
	invite, err := inviteStore.GetInvite(ctx, token)
	if err != nil {
		return "", err
	}
	if invite == nil {
		return "This invite token is invalid or has expired.", nil
	}
	collaborator := auth.Collaborator{UserID: userIDStr, AddedBy: invite.CreatedBy, AddedAt: time.Now()}
	if err := collaboratorStore.AddCollaborator(ctx, collaborator); err != nil {
		return "", err
	}
	return "Welcome! You are now a bot collaborator.", nil
}

func createZulipInviteToken(ctx context.Context, inviteStore *auth.InviteStore, senderID int) (string, error) {
	if isNilZulipInterface(inviteStore) {
		return "", fmt.Errorf("invite store is unavailable")
	}
	token, _, err := inviteStore.CreateInvite(ctx, fmt.Sprintf("%d", senderID))
	return token, err
}

func loadZulipUserListView(ctx context.Context, collaboratorStore *auth.CollaboratorStore, inviteStore *auth.InviteStore) ([]auth.Collaborator, []auth.Invite, error) {
	if isNilZulipInterface(collaboratorStore) {
		return nil, nil, fmt.Errorf("collaborator store is unavailable")
	}
	collaborators, err := collaboratorStore.ListCollaborators(ctx)
	if err != nil {
		return nil, nil, err
	}
	var invites []auth.Invite
	if !isNilZulipInterface(inviteStore) {
		invites, err = inviteStore.ListInvites(ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	return collaborators, invites, nil
}

func removeZulipCollaborator(ctx context.Context, collaboratorStore *auth.CollaboratorStore, userID string) error {
	if isNilZulipInterface(collaboratorStore) {
		return fmt.Errorf("collaborator store is unavailable")
	}
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return fmt.Errorf("user id is required")
	}
	return collaboratorStore.RemoveCollaborator(ctx, trimmed)
}

func isNilZulipInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

type zulipStartArgs struct {
	Mode  string
	Token string
}

func parseZulipStartArgs(args string) (zulipStartArgs, bool) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return zulipStartArgs{}, true
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 1 && auth.LooksLikeChannelToken(strings.TrimSpace(fields[0])) {
		return zulipStartArgs{Mode: "channel_token", Token: strings.TrimSpace(fields[0])}, true
	}
	key, value, ok := strings.Cut(trimmed, "=")
	if !ok || strings.TrimSpace(value) == "" {
		return zulipStartArgs{}, false
	}
	mode := strings.TrimSpace(key)
	token := strings.TrimSpace(value)
	switch mode {
	case "owner", "invite":
		return zulipStartArgs{Mode: mode, Token: token}, true
	default:
		return zulipStartArgs{}, false
	}
}

func restartZulipSessionLabel(isDM bool, info baldasession.TopicSessionInfo, ownerLabel, autoLabel string) string {
	if label := strings.TrimSpace(info.AgentName); label != "" {
		return label
	}
	if isDM {
		return ownerLabel
	}
	return autoLabel
}

func restartZulipSessionUserID(senderID int, info baldasession.TopicSessionInfo) string {
	if userID := strings.TrimSpace(info.UserID); userID != "" {
		return userID
	}
	return zulipUserID(senderID)
}

func restartZulipWelcomeDisplayName(isDM bool, label, ownerLabel string) string {
	if !isDM {
		return ownerLabel
	}
	return label
}

func zulipSessionWelcomeLabel(isDM bool, ownerLabel, autoLabel string) string {
	if isDM {
		return ownerLabel
	}
	return autoLabel
}

func ensureZulipSession(ctx context.Context, manager *baldasession.Manager, locator deliverycmd.Locator, transportUserID, providerName string, isDM bool, ownerLabel, autoLabel string) (*baldasession.TopicSession, bool, error) {
	if manager == nil {
		return nil, false, fmt.Errorf("session manager is unavailable")
	}
	existing, _ := manager.GetSession(locator)
	if existing != nil {
		return existing, false, nil
	}
	if strings.TrimSpace(providerName) == "" {
		return nil, false, fmt.Errorf("no provider configured")
	}
	ts, err := manager.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID})
	if err != nil && !strings.Contains(err.Error(), "no persisted session") {
		return nil, false, err
	}
	if err == nil && ts != nil {
		return ts, true, nil
	}
	label := zulipSessionWelcomeLabel(isDM, ownerLabel, autoLabel)
	ts, err = manager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID}, label)
	if err != nil {
		return nil, false, err
	}
	return ts, true, nil
}

func buildZulipSessionWelcome(manager *baldasession.Manager, providerName string, isDM bool, sessionID string, ownerLabel string, autoLabel string) string {
	if manager == nil {
		return ""
	}
	label := zulipSessionWelcomeLabel(isDM, ownerLabel, autoLabel)
	metadata := manager.GetAgentMetadata(providerName)
	return welcome.BuildAgentWelcomeMessage(label, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

func buildZulipRestartWelcome(manager *baldasession.Manager, providerName string, isDM bool, label string, sessionID string, ownerLabel string) string {
	if manager == nil {
		return ""
	}
	metadata := manager.GetAgentMetadata(providerName)
	welcomeName := restartZulipWelcomeDisplayName(isDM, label, ownerLabel)
	return welcome.BuildAgentWelcomeMessage(welcomeName, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

func buildZulipTopicWelcome(manager *baldasession.Manager, providerName string, topicName string, sessionID string) string {
	if manager == nil {
		return ""
	}
	metadata := manager.GetAgentMetadata(providerName)
	return welcome.BuildAgentWelcomeMessage(topicName, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

func buildZulipLocatorMessage(locator deliverycmd.Locator) string {
	ref := locatorref.Format(locator)
	return fmt.Sprintf("Transport: %s\nLocator: %s\n\nUse in scheduler/webhook config:\ntarget: locator\nkey: %s", locator.ChannelType, ref, ref)
}

func startWelcomeMessage() string {
	return "Welcome to Balda Bot!\n\nTo authenticate:\n" +
		"• /start owner=<your_owner_token>\n" +
		"• /start invite=<your_invite_token>"
}

func startInvalidFormatMessage() string {
	return "Invalid /start format. Use one of:\n" +
		"• /start owner=<your_owner_token>\n" +
		"• /start invite=<your_invite_token>"
}

func startOwnerAlreadyRegisteredSelfMessage(bundle string) string {
	if strings.TrimSpace(bundle) == "" {
		return "You are already registered as the bot owner."
	}
	return "You are already registered as the bot owner.\n\n" + strings.TrimSpace(bundle)
}

func UserUsageMessage() string {
	return "Usage:\n" +
		"• /user add - Generate invite token\n" +
		"• /user list - Show collaborators and active invites\n" +
		"• /user remove <user_id> - Remove collaborator by ID\n"
}

func UserInviteMessage(token string) string {
	return fmt.Sprintf("Invite token created:\n%s\n\nHave the collaborator send:\n/start invite=%s", token, token)
}

func UserRemovedMessage(userID string) string {
	return fmt.Sprintf("Collaborator removed: %s", strings.TrimSpace(userID))
}

func UserListMessage(collaborators []auth.Collaborator, invites []auth.Invite) string {
	var lines []string
	if len(collaborators) > 0 {
		lines = append(lines, "Collaborators:")
		for _, c := range collaborators {
			name := "unknown"
			if strings.TrimSpace(c.Username) != "" {
				name = c.Username
			} else if strings.TrimSpace(c.FirstName) != "" {
				name = c.FirstName
			}
			lines = append(lines, fmt.Sprintf("• %s (%s) - added %s",
				c.UserID, name, c.AddedAt.Format("2006-01-02 15:04")))
		}
	} else {
		lines = append(lines, "No collaborators")
	}
	if len(invites) > 0 {
		lines = append(lines, "", "Active Invites:")
		for _, inv := range invites {
			lines = append(lines, fmt.Sprintf("expires %s", inv.ExpiresAt.Format("2006-01-02 15:04")))
		}
	}
	return strings.Join(lines, "\n")
}

func zulipUserID(userID int) string {
	return fmt.Sprintf("%s-%d", zulipSessionIDPrefix, userID)
}

func parseZulipUserID(value string) (int, error) {
	prefix := zulipSessionIDPrefix + "-"
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, fmt.Errorf("zulip user id %q must start with %q", value, prefix)
	}
	userID, err := strconv.Atoi(strings.TrimPrefix(trimmed, prefix))
	if err != nil {
		return 0, fmt.Errorf("parse zulip user id %q: %w", value, err)
	}
	if userID <= 0 {
		return 0, fmt.Errorf("zulip user id %q must be positive", value)
	}
	return userID, nil
}

func normalizeGoalMaxIterations(v int) int {
	if v <= 0 {
		return defaultGoalMaxIterations
	}
	return v
}

func buildZulipSessionPreparation(ts *baldasession.TopicSession, requesterUserID string) ingressapp.SessionPreparation {
	return ingressapp.SessionPreparation{
		Ready:           true,
		UserID:          ts.GetUserID(),
		RequesterUserID: requesterUserID,
		AgentSessionID:  ts.GetAgentSessionID(),
	}
}

func inboundLocator(inbound ingressapp.InboundContext) deliverycmd.Locator {
	return deliverycmd.Locator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
}

func newZulipStreamLocator(streamID int, topic string) deliverycmd.Locator {
	return zulip.NewStreamLocator(streamID, topic)
}

func newZulipDMLocator(userID int) deliverycmd.Locator {
	return zulip.NewDMLocator(userID)
}

func SendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator deliverycmd.Locator, text string) error {
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	if dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	_, err = dispatcher.Dispatch(ctx, env)
	return err
}

func SendMarkdown(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator deliverycmd.Locator, text string) error {
	env, err := deliverycmd.MarkdownEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	if dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	_, err = dispatcher.Dispatch(ctx, env)
	return err
}

func SendAgentReply(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator deliverycmd.Locator, text string) error {
	env, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement("", from, locator, "", deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	if dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	_, err = dispatcher.Dispatch(ctx, env)
	return err
}

func SubmitSessionTurnCancelControl(ctx context.Context, dispatcher actortransport.Dispatcher, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.CancelTurnEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session turn cancel control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session turn cancel control command: %w", err)
	}
	return nil
}

func SubmitGoalClearControl(ctx context.Context, dispatcher actortransport.Dispatcher, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.ClearGoalEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build goal clear control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish goal clear control command: %w", err)
	}
	return nil
}

type autoStateManager interface {
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
}

func LoadUsageSnapshot(ctx context.Context, sessions autoStateManager, locator baldasession.SessionLocator) (usageview.Snapshot, bool, error) {
	if sessions == nil {
		return usageview.Snapshot{}, false, nil
	}
	value, ok, err := sessions.RuntimeStateValue(ctx, locator, usageview.UsageStateKey)
	if err != nil || !ok {
		return usageview.Snapshot{}, false, err
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return usageview.Snapshot{}, false, nil
	}
	return usageview.SnapshotFromMap(raw)
}

func PlainAutoCommandReply(
	ctx context.Context,
	sessions autoStateManager,
	dispatcher actortransport.Dispatcher,
	locator deliverycmd.Locator,
	args string,
	usage string,
	now time.Time,
	defaultMaxTurns int,
) string {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "":
		status, err := loadAutoStatusWithDefault(ctx, sessions, locator, defaultMaxTurns)
		if err != nil {
			return "Could not read auto mode status."
		}
		return automode.RenderStatus(status)
	case "on":
		if err := dispatchAutoStateUpdate(ctx, dispatcher, locator, automode.EnableStateWithMaxTurns(now, defaultMaxTurns)); err != nil {
			return "Could not enable auto mode."
		}
		return automode.RenderStatus(automode.NormalizeWithDefault(automode.Status{
			Enabled:  true,
			State:    automode.StateIdle,
			MaxTurns: defaultMaxTurns,
		}, defaultMaxTurns))
	case "off":
		if err := dispatchAutoStateUpdate(ctx, dispatcher, locator, automode.DisableState()); err != nil {
			return "Could not disable auto mode."
		}
		return automode.RenderStatus(automode.DefaultStatusWithMaxTurns(defaultMaxTurns))
	default:
		return usage
	}
}

func loadAutoStatusWithDefault(ctx context.Context, sessions autoStateManager, locator baldasession.SessionLocator, defaultMaxTurns int) (automode.Status, error) {
	status := automode.DefaultStatusWithMaxTurns(defaultMaxTurns)
	if sessions == nil {
		return status, nil
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyEnabled); err != nil {
		return status, err
	} else if ok {
		status.Enabled = automode.ParseBool(value)
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMode); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.State = strings.TrimSpace(text)
		}
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyConsecutiveTurns); err != nil {
		return status, err
	} else if ok {
		status.ConsecutiveTurns = automode.ParseInt(value, 0)
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMaxTurns); err != nil {
		return status, err
	} else if ok {
		status.MaxTurns = automode.ParseInt(value, defaultMaxTurns)
	}
	return status, nil
}

func dispatchAutoStateUpdate(
	ctx context.Context,
	dispatcher actortransport.Dispatcher,
	locator baldasession.SessionLocator,
	state map[string]any,
) error {
	if dispatcher == nil || len(state) == 0 {
		return nil
	}
	env, err := automodecmd.Envelope(automodecmd.Payload{
		Locator: locator,
		State:   state,
	})
	if err != nil {
		return err
	}
	_, err = dispatcher.Dispatch(ctx, env)
	return err
}
