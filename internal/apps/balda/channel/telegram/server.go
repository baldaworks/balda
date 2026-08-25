package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"go.uber.org/fx"
)

type jobEventAppender interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

// Server is the transport-owned Telegram ingress/runtime carrier.
// The implementation is introduced incrementally while the old handlers-owned
// carrier is being dismantled.
type Server struct {
	ownerStore         *auth.OwnerStore
	collaboratorStore  *auth.CollaboratorStore
	channel            Channel
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
	attachmentStore    AttachmentStore

	mu          sync.RWMutex
	ownerID     int64
	chatID      int64
	botUsername string
	botUserID   int64
	now         func() time.Time
}

type ServerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	CollaboratorStore *auth.CollaboratorStore
	Channel           Channel
	SessionManager    *baldasession.Manager
	Dispatcher        actortransport.Dispatcher
	JobEvents         *baldajobs.JobEventsService `optional:"true"`
	TGClient          client.ClientWithResponsesInterface
	AuthToken         string `name:"balda_auth_token"`
	BaldaProviderID   string `name:"balda_provider"`
	TelegramEnabled   bool   `name:"balda_telegram_enabled"`
	Logger            zerolog.Logger
	QuestionService   *questions.Service `optional:"true"`
	AttachmentStore   AttachmentStore    `optional:"true"`
}

func NewServer(params ServerParams) *Server {
	return &Server{
		ownerStore:         params.OwnerStore,
		collaboratorStore:  params.CollaboratorStore,
		channel:            params.Channel,
		sessionManager:     params.SessionManager,
		actorDispatcher:    params.Dispatcher,
		jobEvents:          params.JobEvents,
		tgClient:           params.TGClient,
		authToken:          strings.TrimSpace(params.AuthToken),
		baldaProviderName:  strings.TrimSpace(params.BaldaProviderID),
		telegramEnabled:    params.TelegramEnabled,
		telegramConfigured: true,
		logger:             params.Logger.With().Str("component", "balda.channel.telegram.server").Logger(),
		questionService:    params.QuestionService,
		attachmentStore:    params.AttachmentStore,
		now:                time.Now,
	}
}

// Start validates that the carrier has enough configuration to be wired.
// Full startup/bootstrap behavior is migrated here incrementally.
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
	s.logOwnerAuthIfNeeded()
	s.restorePersistedOwner()
	return nil
}

func (s *Server) restorePersistedOwner() {
	if s.ownerStore == nil {
		return
	}
	owner := s.ownerStore.GetOwner()
	if owner == nil || owner.UserID == 0 {
		return
	}
	s.setOwner(owner.UserID, owner.ChatID)
	s.logger.Info().
		Int64("owner_id", owner.UserID).
		Int64("chat_id", owner.ChatID).
		Msg("restored persisted telegram owner")
}

// ActivateOwner binds the owner identity to the server state.
func (s *Server) ActivateOwner(ctx context.Context, ownerID, chatID int64) error {
	_ = ctx
	s.setOwner(ownerID, chatID)
	return nil
}

// SubmitSessionTurn forwards a session turn envelope into the actor runtime.
func (s *Server) SubmitSessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error) {
	if s.actorDispatcher == nil {
		return nil, fmt.Errorf("runtime is unavailable")
	}
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		return nil, err
	}
	return s.actorDispatcher.Dispatch(ctx, env)
}

// SubmitWebhookTask forwards a durable webhook task envelope into the actor runtime.
func (s *Server) SubmitWebhookTask(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error) {
	if s.actorDispatcher == nil {
		return nil, "", fmt.Errorf("runtime is unavailable")
	}
	env, jobID, err := turncmd.WebhookJobEnvelope(payload, routeName, requestID)
	if err != nil {
		return nil, "", err
	}
	result, err := s.actorDispatcher.Dispatch(ctx, env)
	if err != nil {
		return nil, "", err
	}
	return result, jobID, nil
}

func (s *Server) getOwnerID() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerID
}

func (s *Server) getChatID() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chatID
}

func (s *Server) setChatID(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatID = chatID
}

func (s *Server) setOwner(ownerID, chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownerID = ownerID
	if chatID != 0 {
		s.chatID = chatID
	}
}

func (s *Server) getBotIdentity() (int64, string) {
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

	if s.authToken != "" {
		s.logger.Info().Int64("bot_user_id", botUserID).Str("bot_username", username).Bool("owner_auth_available", true).Msg("balda owner auth available")
		return nil
	}
	s.logger.Info().Int64("bot_user_id", botUserID).Str("bot_username", username).Msg("balda bot identity loaded")
	return nil
}

func (s *Server) logOwnerAuthIfNeeded() {
	if s.authToken == "" || s.ownerStore == nil || s.ownerStore.HasOwner() {
		return
	}

	_, username := s.getBotIdentity()
	if strings.TrimSpace(username) == "" {
		return
	}

	s.logger.Info().
		Str("auth_command", auth.BuildOwnerAuthCommand(s.authToken)).
		Str("auth_link", auth.BuildOwnerAuthLink(username, s.authToken)).
		Msg("balda owner authentication required")
}

var _ Handler = (*Server)(nil)
var _ actortransport.Dispatcher = actortransport.Dispatcher(nil)
var _ = actorlayer.ActorAddress{}
