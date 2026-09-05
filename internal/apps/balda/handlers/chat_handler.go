package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type ChatHandlerParams struct {
	fx.In

	SessionManager *baldasession.Manager
	Dispatcher     actortransport.Dispatcher
	Questions      *questions.Service `optional:"true"`
	Logger         zerolog.Logger
}

// ChatHandler owns provider-neutral session preparation, question settlement,
// and durable SessionActor publication for conversational ingress.
type ChatHandler struct {
	sessions   *baldasession.Manager
	dispatcher actortransport.Dispatcher
	questions  *questions.Service
	ingress    *ingressapp.Service
	logger     zerolog.Logger
}

func NewChatHandler(params ChatHandlerParams) (*ChatHandler, error) {
	handler := &ChatHandler{
		sessions:   params.SessionManager,
		dispatcher: params.Dispatcher,
		questions:  params.Questions,
		logger:     params.Logger.With().Str("component", "balda.handlers.chat").Logger(),
	}
	service, err := ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(func(context.Context, ingressapp.InboundContext) (ingressapp.Authorization, error) {
			return ingressapp.Authorization{Allowed: true}, nil
		}),
		ingressapp.SessionPreparerFunc(handler.prepareSession),
		params.Dispatcher,
		handler.logger,
	)
	if err != nil {
		return nil, err
	}
	handler.ingress = service
	return handler, nil
}

func (h *ChatHandler) HandleChat(ctx context.Context, request chatapp.Request) (chatapp.Result, error) {
	if request.QuestionReply != nil {
		handled, activated, err := h.handleQuestionReply(ctx, request)
		if err != nil {
			return chatapp.Result{Settlement: retryChat()}, err
		}
		if handled {
			return chatapp.Result{Settlement: terminalChat(), Activated: activated}, nil
		}
	}
	result, err := h.ingress.Process(ctx, request.NormalizedInbound())
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		event := h.logger.Warn().Err(err).Str("session_id", request.Locator.SessionID)
		if actorcmd.IsCommandQueueFull(err) {
			event.Msg("session command queue full")
		} else {
			event.Msg("failed to dispatch session turn")
		}
	}
	return chatapp.Result{
		Settlement: result.Settlement,
		Activated:  err == nil && result.Settlement.Outcome == turncmd.InboundAccepted,
	}, err
}

func (h *ChatHandler) prepareSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	if h.sessions == nil {
		return ingressapp.SessionPreparation{}, actorlayer.TransientError(fmt.Errorf("session manager is unavailable"))
	}
	locator := baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
	topicSession, err := h.getOrCreateSession(ctx, locator, inbound.UserID)
	if err != nil {
		return ingressapp.SessionPreparation{}, err
	}
	return ingressapp.SessionPreparation{
		Ready:           true,
		UserID:          topicSession.GetUserID(),
		RequesterUserID: inbound.UserID,
		AgentSessionID:  topicSession.GetAgentSessionID(),
	}, nil
}

func (h *ChatHandler) getOrCreateSession(ctx context.Context, locator baldasession.SessionLocator, subject string) (*baldasession.TopicSession, error) {
	if existing, _ := h.sessions.GetSession(locator); existing != nil {
		return existing, nil
	}
	topicSession, err := h.sessions.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject})
	if err == nil && topicSession != nil {
		return topicSession, nil
	}
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, err
	}
	return h.sessions.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject}, autoSessionLabel)
}

func (h *ChatHandler) handleQuestionReply(ctx context.Context, request chatapp.Request) (bool, bool, error) {
	if h.questions == nil || request.QuestionReply == nil {
		return false, false, nil
	}
	result, err := h.questions.ResolveReplyDetailed(ctx, *request.QuestionReply)
	if err != nil || !result.Matched {
		return result.Matched, false, err
	}
	if !result.Settled {
		return true, false, nil
	}
	if h.dispatcher == nil {
		return true, false, actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	continuation := result.Continuation
	if inboundID := strings.TrimSpace(string(request.ID)); inboundID != "" {
		continuation.DedupeKey = inboundID
	}
	receipt, err := h.dispatcher.Dispatch(ctx, continuation)
	if err != nil {
		return true, false, err
	}
	if receipt == nil {
		return true, false, actorlayer.TransientError(fmt.Errorf("question continuation dispatch returned no receipt"))
	}
	return true, true, nil
}

func terminalChat() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryChat() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}
