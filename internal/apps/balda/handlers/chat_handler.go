package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const autoSessionLabel = "auto"

type ChatHandlerParams struct {
	fx.In

	SessionManager *baldasession.Manager
	Dispatcher     actortransport.Dispatcher
	Questions      *questions.Service `optional:"true"`
	Logger         zerolog.Logger
}

// ChatHandler is a compatibility adapter wrapping chatapp.Service until handlers is removed.
type ChatHandler struct {
	service *chatapp.Service
}

func NewChatHandler(params ChatHandlerParams) (*ChatHandler, error) {
	var qResolver chatapp.QuestionResolver
	if params.Questions != nil {
		qResolver = chatapp.QuestionResolverFunc(func(ctx context.Context, reply questioncmd.InboundReply) (chatapp.QuestionResolution, error) {
			res, err := params.Questions.ResolveReplyDetailed(ctx, reply)
			if err != nil {
				return chatapp.QuestionResolution{Matched: res.Matched}, err
			}
			return chatapp.QuestionResolution{
				Matched:      res.Matched,
				Settled:      res.Settled,
				Continuation: res.Continuation,
			}, nil
		})
	}

	sessionPreparer := chatapp.SessionPreparerFunc(func(ctx context.Context, inbound chatapp.InboundContext) (chatapp.SessionPreparation, error) {
		if params.SessionManager == nil {
			return chatapp.SessionPreparation{}, actorlayer.TransientError(fmt.Errorf("session manager is unavailable"))
		}
		locator := baldasession.SessionLocator{
			ChannelType: inbound.ChannelType,
			AddressKey:  inbound.AddressKey,
			AddressJSON: inbound.AddressJSON,
			SessionID:   inbound.SessionID,
		}
		topicSession, err := getOrCreateSession(ctx, params.SessionManager, locator, inbound.UserID)
		if err != nil {
			return chatapp.SessionPreparation{}, err
		}
		return chatapp.SessionPreparation{
			Ready:           true,
			UserID:          topicSession.GetUserID(),
			RequesterUserID: inbound.UserID,
			AgentSessionID:  topicSession.GetAgentSessionID(),
			TopicID:         inbound.TopicID,
		}, nil
	})

	dispatcher := chatapp.DispatcherFunc(func(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
		if params.Dispatcher == nil {
			return nil, actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
		}
		return params.Dispatcher.Dispatch(ctx, env)
	})

	svc, err := chatapp.NewService(chatapp.ServiceParams{
		Sessions:   sessionPreparer,
		Dispatcher: dispatcher,
		Questions:  qResolver,
		Logger:     params.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &ChatHandler{service: svc}, nil
}

func (h *ChatHandler) HandleChat(ctx context.Context, request chatapp.Request) (chatapp.Result, error) {
	if h == nil || h.service == nil {
		return chatapp.Result{}, fmt.Errorf("chat handler is not initialized")
	}
	return h.service.HandleChat(ctx, request)
}

func getOrCreateSession(ctx context.Context, mgr *baldasession.Manager, locator baldasession.SessionLocator, subject string) (*baldasession.TopicSession, error) {
	if existing, _ := mgr.GetSession(locator); existing != nil {
		return existing, nil
	}
	topicSession, err := mgr.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject})
	if err == nil && topicSession != nil {
		return topicSession, nil
	}
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, err
	}
	return mgr.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject}, autoSessionLabel)
}
