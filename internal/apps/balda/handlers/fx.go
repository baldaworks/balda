package handlers

import (
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandapp"
	"go.uber.org/fx"
)

// Module provides handlers for the balda bot.
var Module = fx.Module("balda_handlers",
	fx.Provide(
		fx.Annotate(
			NewChatHandler,
			fx.As(new(chatapp.Handler)),
		),
		func(params inboundWebhookParams) (*InboundWebhookReceiver, error) {
			normalized, err := normalizeInboundWebhookConfig(params.Config)
			if err != nil {
				return nil, err
			}

			receiver := &InboundWebhookReceiver{
				enabled:    normalized.Enabled,
				listenAddr: normalized.ListenAddr,
				routes:     normalized.Routes,
				balda:      params.Executor,
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
				baldaHandler:      params.OwnerActivator,
			}
		},
		func(params commandHandlerParams) *CommandHandler {
			return &CommandHandler{
				ownerStore:        params.OwnerStore,
				collaboratorStore: params.CollaboratorStore,
				channel:           params.Channel,
				sessionManager:    params.SessionManager,
				workCanceller:     params.WorkCanceller,
				actorDispatcher:   params.Dispatcher,
				locatorRenderer:   params.LocatorRenderer,
				goalJobs:          params.GoalJobs,
				goalMaxIterations: normalizeGoalMaxIterations(params.MaxIterations),
				autoMaxTurns:      automode.NormalizeMaxTurns(params.AutoMaxTurns),
				userHandler:       params.UserHandler,
				plugins:           params.Plugins,
			}
		},
		func(handler *CommandHandler) commandapp.Handler { return handler },
		func(params userHandlerParams) *userHandler {
			return &userHandler{
				ownerStore:        params.OwnerStore,
				inviteStore:       params.InviteStore,
				collaboratorStore: params.CollaboratorStore,
				actorDispatcher:   params.Dispatcher,
				tgClient:          params.TGClient,
			}
		},
	),
	fx.Invoke(
		func(*InboundWebhookReceiver) {},
	),
)
