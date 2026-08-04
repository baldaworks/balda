package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/attachment"
	"github.com/normahq/balda/internal/apps/balda/attachmentstore"
	"github.com/normahq/balda/internal/apps/balda/auth"
	baldachannel "github.com/normahq/balda/internal/apps/balda/channel"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	"github.com/normahq/balda/internal/apps/balda/questions"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/sessionturnapp"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
	"go.uber.org/fx"
	"google.golang.org/adk/v2/runner"
)

const (
	ownerSessionLabel = "balda"
	autoSessionLabel  = "auto"
)

type jobEventAppender interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

// BaldaHandler handles bidirectional session messages for the owner and
// collaborators.
type BaldaHandler struct {
	ownerStore         *auth.OwnerStore
	collaboratorStore  *auth.CollaboratorStore
	channel            *baldatelegram.Adapter
	sessionManager     *baldasession.Manager
	actorDispatcher    actortransport.Dispatcher
	jobEvents          jobEventAppender
	messenger          *messenger.Messenger
	tgClient           client.ClientWithResponsesInterface
	authToken          string
	baldaProviderName  string
	telegramEnabled    bool
	telegramConfigured bool
	logger             zerolog.Logger
	outboundFrom       actorlayer.ActorAddress
	progressEmitter    sessionturnapp.SessionProgressEmitter
	turnExecution      *sessionturnapp.TurnExecutionService
	questionService    *questions.Service
	attachmentStore    attachmentstore.Store

	mu          sync.RWMutex
	ownerID     int64
	chatID      int64
	botUsername string
	botUserID   int64
	now         func() time.Time
}

type baldaHandlerDeps struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           *baldatelegram.Adapter
	SessionManager    *baldasession.Manager
	Dispatcher        actortransport.Dispatcher
	JobEvents         *baldajobs.JobEventsService `optional:"true"`
	Messenger         *messenger.Messenger
	TGClient          client.ClientWithResponsesInterface
	AuthToken         string `name:"balda_auth_token"`
	BaldaProviderID   string `name:"balda_provider"`
	TelegramEnabled   bool   `name:"balda_telegram_enabled"`
	Logger            zerolog.Logger
	TurnExecution     *sessionturnapp.TurnExecutionService
	QuestionService   *questions.Service    `optional:"true"`
	AttachmentStore   attachmentstore.Store `optional:"true"`
}

// Start validates the Telegram identity and bootstraps owner state.
func (h *BaldaHandler) Start(ctx context.Context) error {
	return h.onStart(ctx)
}

// Register registers the handler with the registry.
func (h *BaldaHandler) Register(registry tgbotkit.Registry) {
	registry.OnMessage(h.onMessage)
	registry.OnCallbackDataPrefix(baldatelegram.QuestionCallbackPrefix, h.onQuestionCallback)
	registry.OnMessageType(messagetype.ForumTopicCreated, h.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicEdited, h.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicClosed, h.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicReopened, h.onForumTopicLifecycle)
}

func (h *BaldaHandler) onMessage(ctx context.Context, event *events.MessageEvent) error {
	messageCtx, ok := h.channel.MessageContextFromEvent(event)
	if !ok {
		return nil
	}
	h.logger.Info().
		Str("message_type", string(event.Type)).
		Str("media_group_id", messageCtx.MediaGroupID).
		Int("attachments_count", len(messageCtx.Attachments)).
		Interface("attachments", attachment.LogFields(messageCtx.Attachments)).
		Interface("raw_transport_message", event.Message).
		Msg("received inbound telegram transport message")

	if h.getOwnerID() == 0 {
		return nil
	}
	allowed, err := h.accessCollaboratorScope(ctx, messageCtx.UserID)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	if h.getChatID() == 0 {
		h.setChatID(messageCtx.ChatID)
		log.Info().Int64("chat_id", messageCtx.ChatID).Msg("Chat ID set from message")
	}

	if messageCtx.HasCommand {
		return nil
	}
	if h.channel.CollectMediaGroup(messageCtx, func(groupCtx context.Context, grouped baldatelegram.MessageContext) {
		if err := h.handleAcceptedMessage(groupCtx, grouped); err != nil {
			h.logger.Error().Err(err).
				Str("session_id", grouped.Locator.SessionID).
				Str("media_group_id", grouped.MediaGroupID).
				Msg("failed to handle inbound telegram media group")
		}
	}) {
		return nil
	}
	return h.handleAcceptedMessage(ctx, messageCtx)
}

func (h *BaldaHandler) handleAcceptedMessage(ctx context.Context, messageCtx baldatelegram.MessageContext) error {
	if h.attachmentStore != nil && len(messageCtx.Attachments) > 0 {
		persisted, err := h.attachmentStore.PersistTelegram(ctx, messageCtx.Attachments)
		if err != nil {
			h.logger.Warn().Err(err).Msg("failed to persist inbound telegram attachments")
		} else {
			messageCtx.Attachments = persisted
		}
	}
	if handled, err := h.handleQuestionReply(ctx, messageCtx); err != nil {
		h.logger.Warn().Err(err).Str("session_id", messageCtx.Locator.SessionID).Msg("failed to handle question reply")
		_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, messageCtx.Locator, "Could not process this reply right now. Please try again.")
		return nil
	} else if handled {
		return nil
	}

	var text string
	if messageCtx.IsDM {
		text = h.normalizeDMText(messageCtx)
	} else {
		normalized, ok := h.normalizePublicText(messageCtx)
		if !ok {
			return nil
		}
		text = normalized
	}
	if strings.TrimSpace(text) == "" && len(messageCtx.Attachments) == 0 {
		return nil
	}

	receivedAtNow := time.Now
	if h.now != nil {
		receivedAtNow = h.now
	}
	inbound := h.channel.NormalizeInbound(
		messageCtx,
		appendAttachmentSummary(text, messageCtx.Attachments),
		receivedAtNow(),
	)
	service, err := h.telegramIngressService()
	if err != nil {
		return err
	}
	result, err := service.Process(ctx, inbound)
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		if baldaexecution.IsCommandQueueFull(err) {
			_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, messageCtx.Locator, "Session command queue is full. Please wait or use /cancel.")
		} else {
			h.logger.Error().Err(err).
				Str("session_id", messageCtx.Locator.SessionID).
				Str("inbound_id", string(result.InboundID)).
				Msg("failed to durably accept inbound telegram message")
			_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, messageCtx.Locator, "Failed to publish your message for processing. Please try again.")
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

func (h *BaldaHandler) outboundActorAddress(sessionID string) actorlayer.ActorAddress {
	if h != nil && strings.TrimSpace(h.outboundFrom.Target) != "" {
		return h.outboundFrom
	}
	return baldaHandlerActorAddress
}

func (h *BaldaHandler) runTurnJobWithDelivery(
	ctx context.Context,
	text string,
	r *runner.Runner,
	userID string,
	sessionID string,
	jobID string,
	agentSessionID string,
	locator baldasession.SessionLocator,
	messageID int,
	topicID int,
	progressPolicy baldachannel.ProgressPolicy,
	deliver bool,
) error {
	return h.runTurnJobWithDeliveryOptions(ctx, text, r, userID, sessionID, jobID, agentSessionID, locator, messageID, topicID, deliveryfmt.Options{ProgressPolicy: progressPolicy}, deliver)
}

func (h *BaldaHandler) runTurnJobWithDeliveryOptions(
	ctx context.Context,
	text string,
	r *runner.Runner,
	userID string,
	sessionID string,
	jobID string,
	agentSessionID string,
	locator baldasession.SessionLocator,
	messageID int,
	topicID int,
	deliveryOptions deliveryfmt.Options,
	deliver bool,
	runOpts ...runner.RunOption,
) error {
	if !deliver {
		deliveryOptions.ProgressPolicy = deliveryfmt.ProgressPolicy{}
	}
	err := h.runTurnWithDeliveryOptions(ctx, text, r, userID, sessionID, jobID, agentSessionID, locator, messageID, deliveryOptions, deliver, runOpts...)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		h.logger.Info().
			Str("session_id", sessionID).
			Int("topic_id", topicID).
			Msg("balda turn canceled")
		return err
	}
	if _, getErr := h.sessionManager.GetSession(locator); getErr != nil {
		h.logger.Debug().
			Str("session_id", sessionID).
			Int("topic_id", topicID).
			Msg("suppressing balda turn error for inactive session")
		return nil
	}
	if !deliver {
		h.logger.Warn().
			Err(err).
			Str("session_id", sessionID).
			Int("topic_id", topicID).
			Msg("fire-and-forget balda turn failed")
		return err
	}

	log.Error().Err(err).Int("topic_id", topicID).Msg("agent execution failed")
	errText := "Agent execution failed. Use /reset or /restart to restart this session."
	if sendErr := sendPlain(context.Background(), h.actorDispatcher, baldaHandlerActorAddress, locator, errText); sendErr != nil {
		log.Warn().Err(sendErr).Int("topic_id", topicID).Msg("failed to send balda error message")
	}
	return err
}

func (h *BaldaHandler) onForumTopicLifecycle(ctx context.Context, event *events.MessageEvent) error {
	lifecycle, ok := h.channel.TopicLifecycleFromEvent(event)
	if !ok {
		return nil
	}

	chatID := lifecycle.ChatID
	boundChatID := h.getChatID()
	if boundChatID != 0 && chatID != boundChatID {
		return nil
	}

	topicID := lifecycle.TopicID
	if topicID <= 0 {
		h.logger.Debug().
			Int64("chat_id", chatID).
			Str("event_type", string(lifecycle.Type)).
			Msg("ignoring forum topic lifecycle event without topic id")
		return nil
	}

	evt := h.logger.Info().
		Int64("chat_id", chatID).
		Int("topic_id", topicID).
		Int("message_id", lifecycle.MessageID).
		Str("event_type", string(lifecycle.Type))
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

func (h *BaldaHandler) runTurnWithDelivery(
	ctx context.Context,
	text string,
	r *runner.Runner,
	userID string,
	sessionID string,
	jobID string,
	agentSessionID string,
	locator baldasession.SessionLocator,
	messageID int,
	progressPolicy baldachannel.ProgressPolicy,
	deliver bool,
) error {
	return h.runTurnWithDeliveryOptions(ctx, text, r, userID, sessionID, jobID, agentSessionID, locator, messageID, deliveryfmt.Options{ProgressPolicy: progressPolicy}, deliver)
}

func (h *BaldaHandler) runTurnWithDeliveryOptions(
	ctx context.Context,
	text string,
	r *runner.Runner,
	userID string,
	sessionID string,
	jobID string,
	agentSessionID string,
	locator baldasession.SessionLocator,
	messageID int,
	deliveryOptions deliveryfmt.Options,
	deliver bool,
	runOpts ...runner.RunOption,
) error {
	execution := h.turnExecution
	if execution == nil {
		return fmt.Errorf("turn execution service is not configured")
	}
	return execution.Execute(ctx, sessionturnapp.ExecutionRequest{
		Text:            text,
		Runner:          r,
		UserID:          userID,
		SessionID:       sessionID,
		JobID:           jobID,
		AgentSessionID:  agentSessionID,
		Locator:         locator,
		MessageID:       messageID,
		DeliveryOptions: deliveryOptions,
		Deliver:         deliver,
		ProgressEmitter: h.progressEmitter,
		OutboundFrom:    h.outboundActorAddress(sessionID),
		RunOptions:      runOpts,
	})
}
