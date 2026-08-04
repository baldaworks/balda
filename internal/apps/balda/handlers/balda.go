package handlers

import (
	"context"
	"strings"
	"sync"
	"time"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/auth"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/questions"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
	"go.uber.org/fx"
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
	channel            TelegramChannel
	sessionManager     *baldasession.Manager
	actorDispatcher    actortransport.Dispatcher
	jobEvents          jobEventAppender
	tgClient           client.ClientWithResponsesInterface
	authToken          string
	baldaProviderName  string
	telegramEnabled    bool
	telegramConfigured bool
	logger             zerolog.Logger
	questionService    *questions.Service
	attachmentStore    TelegramAttachmentStore

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
	Channel           TelegramChannel
	SessionManager    *baldasession.Manager
	Dispatcher        actortransport.Dispatcher
	JobEvents         *baldajobs.JobEventsService `optional:"true"`
	TGClient          client.ClientWithResponsesInterface
	AuthToken         string `name:"balda_auth_token"`
	BaldaProviderID   string `name:"balda_provider"`
	TelegramEnabled   bool   `name:"balda_telegram_enabled"`
	Logger            zerolog.Logger
	QuestionService   *questions.Service      `optional:"true"`
	AttachmentStore   TelegramAttachmentStore `optional:"true"`
}

// Start validates the Telegram identity and bootstraps owner state.
func (h *BaldaHandler) Start(ctx context.Context) error {
	return h.onStart(ctx)
}

// Register registers the handler with the registry.
func (h *BaldaHandler) Register(registry TelegramRegistry) {
	registry.OnMessage(h.onMessage)
	registry.OnCallbackDataPrefix(telegramQuestionCallbackPrefix, h.onQuestionCallback)
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
		Str("transport", "telegram").
		Str("message_type", string(event.Type)).
		Bool("media_group", strings.TrimSpace(messageCtx.MediaGroupID) != "").
		Int("attachments_count", len(messageCtx.Attachments)).
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
	if h.channel.CollectMediaGroup(messageCtx, func(groupCtx context.Context, grouped TelegramMessageContext) {
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

func (h *BaldaHandler) handleAcceptedMessage(ctx context.Context, messageCtx TelegramMessageContext) error {
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
	inbound := normalizeTelegramInbound(
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
