package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/automode"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"go.uber.org/fx"
)

// Module provides handlers for the balda bot.
var Module = fx.Module("balda_handlers",
	fx.Provide(
		NewZulipBaldaHandler,
		NewSlackChatHandler,
		NewSlackAgentHandler,
		func(params inboundWebhookParams) (*InboundWebhookReceiver, error) {
			normalized, err := normalizeInboundWebhookConfig(params.Config)
			if err != nil {
				return nil, err
			}

			receiver := &InboundWebhookReceiver{
				enabled:    normalized.Enabled,
				listenAddr: normalized.ListenAddr,
				routes:     normalized.Routes,
				balda:      params.Balda,
				owner:      params.OwnerStore,
				logger:     params.Logger.With().Str("component", "balda.inbound_webhook").Logger(),
			}

			if !receiver.enabled {
				return receiver, nil
			}
			if receiver.balda == nil {
				return nil, fmt.Errorf("balda handler is required for inbound webhooks")
			}
			if receiver.owner == nil {
				return nil, fmt.Errorf("balda owner store is required for inbound webhooks")
			}

			return receiver, nil
		},
		func(params startHandlerParams) *StartHandler {
			return &StartHandler{
				ownerStore:        params.OwnerStore,
				inviteStore:       params.InviteStore,
				collaboratorStore: params.CollaboratorStore,
				channelAuth:       params.ChannelAuth,
				actorDispatcher:   params.Dispatcher,
				authToken:         params.AuthToken,
			}
		},
		newBaldaHandler,
		func(params commandHandlerParams) *CommandHandler {
			return &CommandHandler{
				ownerStore:        params.OwnerStore,
				collaboratorStore: params.CollaboratorStore,
				channel:           params.Channel,
				sessionManager:    params.SessionManager,
				workCanceller:     params.WorkCanceller,
				actorDispatcher:   params.Dispatcher,
				jobService:        params.GoalJobs,
				goalMaxIterations: normalizeGoalMaxIterations(params.MaxIterations),
				autoMaxTurns:      automode.NormalizeMaxTurns(params.AutoMaxTurns),
				userHandler:       params.UserHandler,
			}
		},
		func(params userHandlerParams) *userHandler {
			return &userHandler{
				ownerStore:        params.OwnerStore,
				inviteStore:       params.InviteStore,
				collaboratorStore: params.CollaboratorStore,
				channel:           params.Channel,
				actorDispatcher:   params.Dispatcher,
				tgClient:          params.TGClient,
			}
		},
		fx.Annotate(
			func(h *StartHandler) tgbotkit.Handler { return h },
			fx.As(new(tgbotkit.Handler)),
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			func(h *BaldaHandler) tgbotkit.Handler { return h },
			fx.As(new(tgbotkit.Handler)),
			fx.ResultTags(`group:"bot_handlers"`),
		),
		fx.Annotate(
			func(h *CommandHandler) tgbotkit.Handler { return h },
			fx.As(new(tgbotkit.Handler)),
			fx.ResultTags(`group:"bot_handlers"`),
		),
	),
	fx.Invoke(
		func(start *StartHandler, balda *BaldaHandler) {
			start.baldaHandler = balda
		},
		func(*InboundWebhookReceiver) {},
		func(*ZulipBaldaHandler) {},
		func(*SlackChatHandler) {},
		func(*SlackAgentHandler) {},
	),
)

func newBaldaHandler(deps baldaHandlerDeps) (*BaldaHandler, error) {
	return &BaldaHandler{
		ownerStore:         deps.OwnerStore,
		collaboratorStore:  deps.CollaboratorStore,
		channel:            deps.Channel,
		sessionManager:     deps.SessionManager,
		turnDispatcher:     deps.TurnDispatcher,
		actorDispatcher:    deps.Dispatcher,
		jobEvents:          deps.JobEvents,
		messenger:          deps.Messenger,
		tgClient:           deps.TGClient,
		authToken:          strings.TrimSpace(deps.AuthToken),
		baldaProviderName:  strings.TrimSpace(deps.BaldaProviderID),
		telegramEnabled:    deps.TelegramEnabled,
		telegramConfigured: true,
		logger:             deps.Logger.With().Str("component", "balda.handler").Logger(),
		turnExecution:      deps.TurnExecution,
		questionService:    deps.QuestionService,
		now:                time.Now,
	}, nil
}
