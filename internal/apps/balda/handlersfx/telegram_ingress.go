package handlersfx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/handlers"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/runtime/messagetype"
	"go.uber.org/fx"
)

const (
	ownerSessionLabel = "balda"
	autoSessionLabel  = "auto"

	startupReadyMessage = "Boss, I'm online and ready to work."

	telegramIngressReasonOwnerUnavailable    = "owner_unavailable"
	telegramIngressReasonProviderUnavailable = "provider_unavailable"
	telegramIngressReasonSessionUnavailable  = "session_unavailable"

	questionCallbackSelectedMessage    = "Selected."
	questionCallbackUnavailableMessage = "This choice is not available to you."
)

var serverActorAddress = actorlayer.ActorAddress{Target: "channel", Key: "telegram"}

type telegramInboundHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	sessionManager    *baldasession.Manager
	actorDispatcher   actortransport.Dispatcher
	questionService   *questions.Service
	channel           baldatelegram.Channel
	authToken         string
	baldaProviderName string
	logger            zerolog.Logger
	now               func() time.Time

	mu          sync.RWMutex
	bootstrapMu sync.Mutex
	ownerID     int64
	chatID      int64
	botUsername string
	botUserID   int64
}

type telegramInboundHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore            `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore     `optional:"true"`
	SessionManager    *baldasession.Manager       `optional:"true"`
	Dispatcher        actortransport.Dispatcher   `optional:"true"`
	QuestionService   *questions.Service          `optional:"true"`
	Channel           *baldatelegram.Adapter      `optional:"true"`
	AuthToken         string                      `name:"balda_auth_token" optional:"true"`
	BaldaProviderID   string                      `name:"balda_provider" optional:"true"`
	Logger            zerolog.Logger
}

func newTelegramInboundHandler(params telegramInboundHandlerParams) *telegramInboundHandler {
	var ch baldatelegram.Channel
	if params.Channel != nil {
		ch = params.Channel
	}
	return &telegramInboundHandler{
		ownerStore:        params.OwnerStore,
		collaboratorStore: params.CollaboratorStore,
		sessionManager:    params.SessionManager,
		actorDispatcher:   params.Dispatcher,
		questionService:   params.QuestionService,
		channel:           ch,
		authToken:         strings.TrimSpace(params.AuthToken),
		baldaProviderName: strings.TrimSpace(params.BaldaProviderID),
		logger:            params.Logger.With().Str("component", "balda.handlersfx.telegram_ingress").Logger(),
		now:               time.Now,
	}
}

func (h *telegramInboundHandler) OnBotStarted(ctx context.Context, botUserID int64, botUsername string) error {
	h.mu.Lock()
	h.botUserID = botUserID
	h.botUsername = botUsername
	h.mu.Unlock()

	h.logOwnerAuthIfNeeded()
	ownerID, chatID, bound := h.restorePersistedOwner()
	if bound {
		if _, err := h.bootstrapOwnerSession(ctx, ownerID, chatID); err != nil {
			h.logger.Error().Err(err).Int64("owner_id", ownerID).Msg("failed to bootstrap owner session during startup")
			return fmt.Errorf("bootstrap owner session during startup: %w", err)
		}
	}
	h.sendStartupReadyMessage(ctx)
	return nil
}

func (h *telegramInboundHandler) ActivateOwner(ctx context.Context, ownerID, chatID int64) error {
	if ownerID == 0 {
		return fmt.Errorf("telegram owner id is required")
	}
	if chatID == 0 {
		return fmt.Errorf("telegram owner chat id is required")
	}
	h.setOwner(ownerID, chatID)
	_, err := h.bootstrapOwnerSession(ctx, ownerID, chatID)
	return err
}

func (h *telegramInboundHandler) HandleMessage(ctx context.Context, messageCtx baldatelegram.MessageContext) error {
	ownerID, ownerChatID := h.getOwnerBinding()
	if ownerID == 0 || ownerChatID == 0 {
		h.logger.Warn().
			Str("reason", telegramIngressReasonOwnerUnavailable).
			Msg("ignored inbound telegram message")
		return nil
	}
	allowed, err := h.accessCollaboratorScope(ctx, messageCtx.UserID)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}

	if handled, err := h.handleQuestionReply(ctx, messageCtx); err != nil {
		h.logger.Warn().Err(err).Str("session_id", messageCtx.Locator.SessionID).Msg("failed to handle question reply")
		_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, messageCtx.Locator, "Could not process this reply right now. Please try again.")
		return nil
	} else if handled {
		return nil
	}

	var text string
	if messageCtx.IsDM {
		text = baldatelegram.NormalizeDMText(messageCtx)
	} else {
		botUserID, botUsername := h.getBotIdentity()
		normalized, ok := baldatelegram.NormalizePublicText(messageCtx, botUserID, botUsername)
		if !ok {
			return nil
		}
		text = normalized
	}
	if strings.TrimSpace(text) == "" && len(messageCtx.Attachments) == 0 {
		return nil
	}
	nowFn := time.Now
	if h.now != nil {
		nowFn = h.now
	}
	inbound := baldatelegram.NormalizeInbound(messageCtx, baldatelegram.AppendAttachmentSummary(text, messageCtx.Attachments), nowFn())
	service, err := h.telegramIngressService()
	if err != nil {
		return err
	}
	result, err := service.Process(ctx, inbound)
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		if baldaexecution.IsCommandQueueFull(err) {
			_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, messageCtx.Locator, "Session command queue is full. Please wait or use /cancel.")
		} else {
			h.logger.Error().Err(err).
				Str("session_id", messageCtx.Locator.SessionID).
				Str("inbound_id", string(result.InboundID)).
				Msg("failed to durably accept inbound telegram message")
			_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, messageCtx.Locator, "Failed to publish your message for processing. Please try again.")
		}
		return err
	}
	if err != nil {
		h.logger.Warn().Err(err).
			Str("session_id", messageCtx.Locator.SessionID).
			Str("inbound_id", string(result.InboundID)).
			Str("settlement", string(result.Settlement.Outcome)).
			Msg("terminal inbound telegram message failure")
	}
	return nil
}

func (h *telegramInboundHandler) HandleCallback(ctx context.Context, callback baldatelegram.CallbackContext) error {
	if h.channel == nil {
		return nil
	}
	if h.questionService == nil || h.ownerStore == nil || h.collaboratorStore == nil {
		return h.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "This request is unavailable.", true)
	}
	if !h.canAccessCollaboratorScope(ctx, callback.UserID) {
		return h.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "You cannot answer this request.", true)
	}
	receivedAt := time.Now()
	if h.now != nil {
		receivedAt = h.now()
	}
	result, err := h.questionService.ResolveSelectionDetailed(ctx, questioncmd.InboundSelection{
		Provider:          string(deliverycmd.ChannelTypeTelegram),
		SessionID:         callback.Locator.SessionID,
		ConversationKey:   callback.Locator.AddressKey,
		QuestionID:        callback.QuestionID,
		ProviderMessageID: callback.ProviderMessageID,
		User:              questioncmd.UserRef{UserID: telegramref.UserID(callback.UserID)},
		OptionIndex:       callback.OptionIndex,
		ReceivedAt:        receivedAt,
	})
	if err != nil {
		_ = h.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "Could not process this choice.", true)
		return err
	}
	message, alert := questionCallbackSelectedMessage, false
	switch {
	case !result.Matched || result.Inactive:
		message = "This request has expired."
	case result.Invalid:
		message, alert = questionCallbackUnavailableMessage, true
	case !result.Settled:
		message = "This request has already been answered."
	}
	ackErr := h.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, message, alert)
	if !result.Settled {
		return ackErr
	}
	if dispatchErr := dispatchQuestionContinuation(ctx, h.actorDispatcher, result.Continuation); dispatchErr != nil {
		return dispatchErr
	}
	return ackErr
}

func (h *telegramInboundHandler) HandleForumTopic(ctx context.Context, lifecycle baldatelegram.TopicLifecycleContext) error {
	chatID := lifecycle.ChatID
	boundChatID := h.getChatID()
	if boundChatID != 0 && chatID != boundChatID {
		return nil
	}
	topicID := lifecycle.TopicID
	if topicID <= 0 {
		h.logger.Debug().Int64("chat_id", chatID).Str("event_type", string(lifecycle.Type)).Msg("ignoring forum topic lifecycle event without topic id")
		return nil
	}
	evt := h.logger.Info().Int64("chat_id", chatID).Int("topic_id", topicID).Int("message_id", lifecycle.MessageID).Str("event_type", string(lifecycle.Type))
	if lifecycle.UserID != 0 {
		evt = evt.Int64("user_id", lifecycle.UserID)
	}
	switch lifecycle.Type {
	case messagetype.ForumTopicCreated:
		evt.Msg("forum topic created")
	case messagetype.ForumTopicEdited:
		evt.Msg("forum topic edited")
	case messagetype.ForumTopicClosed:
		evt.Msg("forum topic closed")
		if err := submitSessionCancelControl(ctx, h.actorDispatcher, lifecycle.Locator, "system", "session canceled because forum topic was closed", false); err != nil {
			h.logger.Warn().Err(err).Int64("chat_id", chatID).Int("topic_id", topicID).Msg("failed to publish forum-topic-close cancel control command")
		}
		if h.sessionManager != nil {
			h.sessionManager.StopSession(lifecycle.Locator)
		}
	case messagetype.ForumTopicReopened:
		evt.Msg("forum topic reopened")
	default:
		evt.Msg("forum topic lifecycle event")
	}
	return nil
}

func (h *telegramInboundHandler) handleQuestionReply(ctx context.Context, messageCtx baldatelegram.MessageContext) (bool, error) {
	text := messageCtx.Text
	if h == nil || h.questionService == nil || messageCtx.ReplyToMessageID <= 0 || strings.TrimSpace(text) == "" {
		return false, nil
	}
	receivedAt := time.Now()
	if h.now != nil {
		receivedAt = h.now()
	}
	result, err := h.questionService.ResolveReplyDetailed(ctx, questioncmd.InboundReply{
		Provider:         "telegram",
		SessionID:        messageCtx.Locator.SessionID,
		ConversationKey:  messageCtx.Locator.AddressKey,
		ReplyToMessageID: strconv.Itoa(messageCtx.ReplyToMessageID),
		MessageID:        strconv.Itoa(messageCtx.MessageID),
		User:             questioncmd.UserRef{UserID: telegramref.UserID(messageCtx.UserID)},
		Text:             text,
		ReceivedAt:       receivedAt,
	})
	if err != nil || !result.Matched {
		return result.Matched, err
	}
	if !result.Settled {
		return true, nil
	}
	if err := dispatchQuestionContinuation(ctx, h.actorDispatcher, result.Continuation); err != nil {
		return true, err
	}
	return true, nil
}

func (h *telegramInboundHandler) canAccessCollaboratorScope(ctx context.Context, userID int64) bool {
	allowed, err := h.accessCollaboratorScope(ctx, userID)
	return err == nil && allowed
}

func (h *telegramInboundHandler) accessCollaboratorScope(ctx context.Context, userID int64) (bool, error) {
	if h.ownerStore != nil && h.ownerStore.IsOwner(userID) {
		return true, nil
	}
	if h.collaboratorStore == nil {
		return false, nil
	}
	collaborator, found, err := h.collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	if err != nil {
		return false, fmt.Errorf("look up telegram collaborator: %w", err)
	}
	return found && collaborator != nil, nil
}

func (h *telegramInboundHandler) telegramIngressService() (*ingressapp.Service, error) {
	if h == nil {
		return nil, fmt.Errorf("telegram inbound handler is required")
	}
	return ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(h.authorizeTelegramInbound),
		ingressapp.SessionPreparerFunc(h.prepareTelegramSession),
		ingressapp.DispatcherFunc(h.dispatchTelegramInbound),
		h.logger,
	)
}

func (h *telegramInboundHandler) authorizeTelegramInbound(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.Authorization, error) {
	userID, err := telegramref.ParseUserID(inbound.UserID)
	if err != nil {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	allowed, err := h.accessCollaboratorScope(ctx, userID)
	if err != nil {
		return ingressapp.Authorization{}, err
	}
	return ingressapp.Authorization{Allowed: allowed, Reason: ingressapp.ReasonUnauthorized}, nil
}

func (h *telegramInboundHandler) prepareTelegramSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	locator := baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
	transportUserID := strings.TrimSpace(inbound.UserID)
	var ts *baldasession.TopicSession
	var err error
	if inbound.Direct && inbound.TopicID == 0 {
		ownerID, ownerChatID := h.getOwnerBinding()
		if transportUserID == baldatelegram.UserID(ownerID) && locator.SessionID == baldatelegram.NewLocator(ownerChatID, 0).SessionID {
			ts, err = h.bootstrapOwnerSession(ctx, ownerID, ownerChatID)
			if err != nil {
				h.logger.Error().Err(err).Msg("failed to bootstrap owner dm session")
				_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Could not start this session. Please close this chat and try again.")
				return ingressapp.SessionPreparation{Reason: telegramIngressReasonSessionUnavailable}, nil
			}
		} else {
			existingSession, _ := h.sessionManager.GetSession(locator)
			sendWelcome := existingSession == nil
			baldaProviderName := h.getProviderName()
			if baldaProviderName == "" {
				_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Balda is not ready right now. Please close this chat and try again.")
				return ingressapp.SessionPreparation{Reason: telegramIngressReasonProviderUnavailable}, nil
			}
			ts, err = h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID}, ownerSessionLabel)
			if err != nil {
				h.logger.Error().Err(err).Str("agent", baldaProviderName).Msg("failed to ensure main dm session")
				_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Could not start this session. Please close this chat and try again.")
				return ingressapp.SessionPreparation{Reason: telegramIngressReasonSessionUnavailable}, nil
			}
			if sendWelcome {
				metadata := h.sessionManager.GetAgentMetadata(baldaProviderName)
				welcomeMsg := welcome.BuildAgentWelcomeMessage(ownerSessionLabel, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
				_ = sendMarkdown(ctx, h.actorDispatcher, serverActorAddress, locator, welcomeMsg)
				h.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())
			}
		}
	} else {
		ts, err = h.sessionManager.GetSession(locator)
		if err != nil {
			_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Restoring agent session...")
			ts, err = h.sessionManager.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID, AllowBaldaProviderFallback: false})
			if err != nil {
				if errors.Is(err, baldasession.ErrNoPersistedSession) {
					baldaProviderName := h.getProviderName()
					if baldaProviderName == "" {
						_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Balda is not ready right now. Please close this chat topic and try again.")
						return ingressapp.SessionPreparation{Reason: telegramIngressReasonProviderUnavailable}, nil
					}
					ts, err = h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID}, autoSessionLabel)
					if err != nil {
						h.logger.Error().Err(err).Str("agent", baldaProviderName).Int("topic_id", inbound.TopicID).Msg("failed to create session")
						_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Could not start this session. Please close this chat topic and create a new one.")
						return ingressapp.SessionPreparation{Reason: telegramIngressReasonSessionUnavailable}, nil
					}
				} else {
					h.logger.Warn().Err(err).Int("topic_id", inbound.TopicID).Msg("failed to restore session")
					_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, "Could not restore this session. Please close this chat topic and create a new one.")
					return ingressapp.SessionPreparation{Reason: telegramIngressReasonSessionUnavailable}, nil
				}
			}
			if ts != nil {
				baldaProviderID := h.getProviderName()
				metadata := h.sessionManager.GetAgentMetadata(baldaProviderID)
				welcomeName := ownerSessionLabel
				if inbound.Direct {
					welcomeName = ts.GetAgentName()
				}
				welcomeMsg := welcome.BuildAgentWelcomeMessage(welcomeName, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
				_ = sendMarkdown(ctx, h.actorDispatcher, serverActorAddress, locator, welcomeMsg)
				h.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())
			}
		}
	}
	if ts == nil {
		return ingressapp.SessionPreparation{Reason: telegramIngressReasonSessionUnavailable}, nil
	}
	return ingressapp.SessionPreparation{Ready: true, UserID: ts.GetUserID(), RequesterUserID: transportUserID, AgentSessionID: ts.GetAgentSessionID(), TopicID: inbound.TopicID}, nil
}

func (h *telegramInboundHandler) dispatchTelegramInbound(ctx context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if h.actorDispatcher == nil {
		return nil, actorlayer.TransientError(errors.New("telegram ingress dispatcher is unavailable"))
	}
	receipt, err := h.actorDispatcher.Dispatch(ctx, envelope)
	if err == nil {
		return receipt, nil
	}
	if baldaexecution.IsCommandQueueFull(err) {
		return nil, actorlayer.TransientError(err)
	}
	return receipt, err
}

func (h *telegramInboundHandler) bootstrapOwnerSession(ctx context.Context, ownerID, chatID int64) (*baldasession.TopicSession, error) {
	if ownerID == 0 {
		return nil, fmt.Errorf("telegram owner id is required")
	}
	if chatID == 0 {
		return nil, fmt.Errorf("telegram owner chat id is required")
	}
	if h.sessionManager == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	h.bootstrapMu.Lock()
	defer h.bootstrapMu.Unlock()

	providerName := h.getProviderName()
	if providerName == "" {
		return nil, fmt.Errorf("balda provider is not configured")
	}

	locator := baldatelegram.NewLocator(chatID, 0)
	ts, err := h.sessionManager.GetSession(locator)
	if err == nil {
		return ts, nil
	}

	ts, err = h.sessionManager.RestoreSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  baldatelegram.UserID(ownerID),
	})
	if err != nil {
		if !errors.Is(err, baldasession.ErrNoPersistedSession) {
			return nil, fmt.Errorf("restore owner session: %w", err)
		}
		ts, err = h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{
			Locator: locator,
			UserID:  baldatelegram.UserID(ownerID),
		}, ownerSessionLabel)
		if err != nil {
			return nil, fmt.Errorf("create owner session: %w", err)
		}
	}

	metadata := h.sessionManager.GetAgentMetadata(providerName)
	welcomeMessage := welcome.BuildAgentWelcomeMessage(ownerSessionLabel, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
	if err := sendMarkdown(ctx, h.actorDispatcher, serverActorAddress, locator, welcomeMessage); err != nil {
		h.logger.Warn().Err(err).Str("session_id", ts.GetSessionID()).Msg("failed to send owner session welcome")
	}
	h.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())

	h.logger.Info().
		Int64("owner_id", ownerID).
		Int64("chat_id", chatID).
		Str("agent", providerName).
		Msg("owner session bootstrapped")
	return ts, nil
}

func (h *telegramInboundHandler) restorePersistedOwner() (ownerID, chatID int64, bound bool) {
	if h.ownerStore == nil {
		return 0, 0, false
	}
	owner := h.ownerStore.GetOwner()
	if owner == nil || owner.UserID == 0 {
		return 0, 0, false
	}
	h.setOwner(owner.UserID, owner.ChatID)
	h.logger.Info().
		Int64("owner_id", owner.UserID).
		Int64("chat_id", owner.ChatID).
		Msg("restored persisted telegram owner")
	return owner.UserID, owner.ChatID, owner.ChatID != 0
}

func (h *telegramInboundHandler) sendStartupReadyMessage(ctx context.Context) {
	ownerID, chatID := h.getOwnerBinding()
	if ownerID == 0 || chatID == 0 {
		return
	}
	if err := sendPlain(ctx, h.actorDispatcher, serverActorAddress, baldatelegram.NewLocator(chatID, 0), startupReadyMessage); err != nil {
		h.logger.Warn().Err(err).Int64("owner_id", ownerID).Msg("failed to send startup ready message to owner")
		return
	}
	h.logger.Info().Int64("owner_id", ownerID).Msg("startup ready message sent to owner")
}

func (h *telegramInboundHandler) logOwnerAuthIfNeeded() {
	if h.authToken == "" || h.ownerStore == nil || h.ownerStore.HasOwner() {
		return
	}

	_, username := h.getBotIdentity()
	if strings.TrimSpace(username) == "" {
		return
	}

	h.logger.Info().
		Str("auth_command", auth.BuildOwnerAuthCommand(h.authToken)).
		Str("auth_link", auth.BuildOwnerAuthLink(username, h.authToken)).
		Msg("balda owner authentication required")
}

func (h *telegramInboundHandler) getProviderName() string {
	if h.sessionManager == nil {
		return ""
	}
	providerName := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if providerName == "" {
		h.mu.RLock()
		defer h.mu.RUnlock()
		providerName = strings.TrimSpace(h.baldaProviderName)
	}
	return providerName
}

func (h *telegramInboundHandler) sendSessionStartupNotice(ctx context.Context, locator baldasession.SessionLocator, sessionID string) {
	if h.sessionManager == nil {
		return
	}
	notice := strings.TrimSpace(h.sessionManager.TakeStartupNotice(sessionID))
	if notice == "" {
		return
	}
	if err := sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, notice); err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID).Msg("failed to send session startup notice")
	}
}

func (h *telegramInboundHandler) getChatID() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.chatID
}

func (h *telegramInboundHandler) getOwnerBinding() (ownerID, chatID int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ownerID, h.chatID
}


func (h *telegramInboundHandler) setOwner(ownerID, chatID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ownerID = ownerID
	h.chatID = chatID
}

func (h *telegramInboundHandler) getBotIdentity() (int64, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.botUserID, h.botUsername
}

func dispatchQuestionContinuation(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope) error {
	if dispatcher == nil {
		return actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}

func sendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator deliverycmd.Locator, text string) error {
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

func sendMarkdown(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator deliverycmd.Locator, text string) error {
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

func submitSessionCancelControl(ctx context.Context, dispatcher actortransport.Dispatcher, locator deliverycmd.Locator, requestedBy, reason string, notify bool) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.CancelEnvelopeWithNotify(locator, "", requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session cancel control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session cancel control command: %w", err)
	}
	return nil
}

type inboundTurnExecutor struct {
	dispatcher actortransport.Dispatcher
}

func newInboundTurnExecutor(dispatcher actortransport.Dispatcher) handlers.InboundTurnExecutor {
	return &inboundTurnExecutor{dispatcher: dispatcher}
}

func (e *inboundTurnExecutor) SubmitSessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error) {
	if e == nil || e.dispatcher == nil {
		return nil, fmt.Errorf("runtime is unavailable")
	}
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		return nil, err
	}
	return e.dispatcher.Dispatch(ctx, env)
}

func (e *inboundTurnExecutor) SubmitWebhookTask(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error) {
	if e == nil || e.dispatcher == nil {
		return nil, "", fmt.Errorf("runtime is unavailable")
	}
	env, jobID, err := turncmd.WebhookJobEnvelope(payload, routeName, requestID)
	if err != nil {
		return nil, "", err
	}
	result, err := e.dispatcher.Dispatch(ctx, env)
	if err != nil {
		return nil, "", err
	}
	return result, jobID, nil
}
