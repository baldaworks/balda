package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
	"go.uber.org/fx"
)

// Server is the transport-owned Telegram ingress/runtime carrier.
type Server struct {
	channel            Channel
	inboundHandler     InboundHandler
	lifecycleHandler   BotLifecycleHandler
	attachmentStore    AttachmentStore
	tgClient           client.ClientWithResponsesInterface
	telegramEnabled    bool
	telegramConfigured bool
	logger             zerolog.Logger

	mu          sync.RWMutex
	botUsername string
	botUserID   int64
}

type ServerParams struct {
	fx.In

	Channel          Channel
	InboundHandler   InboundHandler      `optional:"true"`
	LifecycleHandler BotLifecycleHandler `optional:"true"`
	AttachmentStore  AttachmentStore     `optional:"true"`
	TGClient         client.ClientWithResponsesInterface
	TelegramEnabled  bool                `name:"balda_telegram_enabled"`
	Logger           zerolog.Logger
}

func NewServer(params ServerParams) *Server {
	return &Server{
		channel:            params.Channel,
		inboundHandler:     params.InboundHandler,
		lifecycleHandler:   params.LifecycleHandler,
		attachmentStore:    params.AttachmentStore,
		tgClient:           params.TGClient,
		telegramEnabled:    params.TelegramEnabled,
		telegramConfigured: true,
		logger:             params.Logger.With().Str("component", "balda.channel.telegram.server").Logger(),
	}
}

// Start validates that the carrier has enough configuration to be wired and loads bot identity.
func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("telegram server is required")
	}
	if !s.telegramConfigured || !s.telegramEnabled {
		return nil
	}
	if s.channel == nil {
		return fmt.Errorf("telegram channel is required")
	}
	if err := s.initializeBotUsername(ctx); err != nil {
		return fmt.Errorf("resolve balda telegram bot identity: %w", err)
	}
	if s.lifecycleHandler != nil {
		botUserID, botUsername := s.GetBotIdentity()
		if err := s.lifecycleHandler.OnBotStarted(ctx, botUserID, botUsername); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Register(registry Registry) {
	registry.OnMessage(s.HandleMessage)
	registry.OnCallbackDataPrefix(QuestionCallbackPrefix, s.HandleQuestionCallback)
	registry.OnMessageType(messagetype.ForumTopicCreated, s.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicEdited, s.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicClosed, s.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicReopened, s.onForumTopicLifecycle)
}

func (s *Server) HandleMessage(ctx context.Context, event *events.MessageEvent) error {
	if s == nil || s.channel == nil {
		return nil
	}
	messageCtx, ok := s.channel.MessageContextFromEvent(event)
	if !ok {
		return nil
	}
	s.logger.Info().
		Str("transport", "telegram").
		Str("message_type", string(event.Type)).
		Bool("media_group", strings.TrimSpace(messageCtx.MediaGroupID) != "").
		Int("attachments_count", len(messageCtx.Attachments)).
		Msg("received inbound telegram transport message")

	if messageCtx.HasCommand {
		return nil
	}
	if s.channel.CollectMediaGroup(messageCtx, func(groupCtx context.Context, grouped MessageContext) {
		if err := s.handleAcceptedMessage(groupCtx, grouped); err != nil {
			s.logger.Error().Err(err).
				Str("session_id", grouped.Locator.SessionID).
				Str("media_group_id", grouped.MediaGroupID).
				Msg("failed to handle inbound telegram media group")
		}
	}) {
		return nil
	}
	return s.handleAcceptedMessage(ctx, messageCtx)
}

func (s *Server) handleAcceptedMessage(ctx context.Context, messageCtx MessageContext) error {
	if s.attachmentStore != nil && len(messageCtx.Attachments) > 0 {
		persisted, err := s.attachmentStore.PersistTelegram(ctx, messageCtx.Attachments)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to persist inbound telegram attachments")
		} else {
			messageCtx.Attachments = persisted
		}
	}
	if s.inboundHandler == nil {
		return nil
	}
	return s.inboundHandler.HandleMessage(ctx, messageCtx)
}

func (s *Server) HandleQuestionCallback(ctx context.Context, event *events.CallbackQueryEvent) error {
	if s == nil || s.channel == nil {
		return nil
	}
	callback, ok := s.channel.CallbackContextFromEvent(event)
	if !ok {
		if event != nil && event.CallbackQuery != nil {
			_ = s.channel.AnswerQuestionCallback(ctx, event.CallbackQuery.Id, "This choice is no longer available.", true)
		}
		return nil
	}
	if s.inboundHandler == nil {
		return s.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "This request is unavailable.", true)
	}
	return s.inboundHandler.HandleCallback(ctx, callback)
}

func (s *Server) onForumTopicLifecycle(ctx context.Context, event *events.MessageEvent) error {
	if s == nil || s.channel == nil {
		return nil
	}
	lifecycle, ok := s.channel.TopicLifecycleFromEvent(event)
	if !ok || lifecycle.TopicID <= 0 {
		return nil
	}
	if s.inboundHandler == nil {
		return nil
	}
	return s.inboundHandler.HandleForumTopic(ctx, lifecycle)
}

func (s *Server) GetBotIdentity() (int64, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.botUserID, s.botUsername
}

func (s *Server) initializeBotUsername(ctx context.Context) error {
	if s.tgClient == nil {
		return fmt.Errorf("telegram client is required")
	}

	meResp, err := s.tgClient.GetMeWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	if meResp == nil {
		return fmt.Errorf("getMe response is nil")
	}
	if meResp.JSON200 == nil {
		if meResp.JSON401 != nil {
			return fmt.Errorf("getMe unauthorized: %s", strings.TrimSpace(meResp.JSON401.Description))
		}
		if meResp.JSON400 != nil {
			return fmt.Errorf("getMe bad request: %s", strings.TrimSpace(meResp.JSON400.Description))
		}
		return fmt.Errorf("getMe response missing result (status %s)", strings.TrimSpace(meResp.Status()))
	}

	botUserID := meResp.JSON200.Result.Id
	if botUserID == 0 {
		return fmt.Errorf("getMe returned empty bot id")
	}

	username := ""
	if meResp.JSON200.Result.Username != nil {
		username = strings.TrimSpace(*meResp.JSON200.Result.Username)
	}
	if username == "" {
		return fmt.Errorf("getMe returned empty username for bot id %d", botUserID)
	}

	s.mu.Lock()
	s.botUserID = botUserID
	s.botUsername = username
	s.mu.Unlock()

	s.logger.Info().Int64("bot_user_id", botUserID).Str("bot_username", username).Msg("balda bot identity loaded")
	return nil
}

var _ Handler = (*Server)(nil)
