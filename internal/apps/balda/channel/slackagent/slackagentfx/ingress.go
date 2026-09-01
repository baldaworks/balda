package slackagentfx

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const autoSessionLabel = "auto"

type inboundProcessorParams struct {
	fx.In

	SessionManager *baldasession.Manager
	Dispatcher     actortransport.Dispatcher
	Question       *questions.Service `optional:"true"`
	Logger         zerolog.Logger
}

type inboundProcessor struct {
	sessions   *baldasession.Manager
	dispatcher actortransport.Dispatcher
	questions  *questions.Service
	logger     zerolog.Logger
}

func newInboundProcessor(params inboundProcessorParams) *inboundProcessor {
	return &inboundProcessor{
		sessions:   params.SessionManager,
		dispatcher: params.Dispatcher,
		questions:  params.Question,
		logger:     params.Logger.With().Str("component", "balda.channel.slackagent.ingress").Logger(),
	}
}

func (p *inboundProcessor) ProcessInbound(ctx context.Context, env slackagent.IngressEnvelope) (turncmd.InboundSettlement, error) {
	if handled, err := p.handleQuestionReply(ctx, env); err != nil {
		return retryInbound(), err
	} else if handled {
		return terminalInbound(), nil
	}
	service, err := ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(func(context.Context, ingressapp.InboundContext) (ingressapp.Authorization, error) {
			return ingressapp.Authorization{Allowed: true}, nil
		}),
		ingressapp.SessionPreparerFunc(p.prepareSession),
		p.dispatcher,
		p.logger,
	)
	if err != nil {
		return retryInbound(), err
	}
	result, err := service.Process(ctx, env.Inbound)
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		event := p.logger.Warn().Err(err).Str("session_id", env.Locator.SessionID)
		if actorcmd.IsCommandQueueFull(err) {
			event.Msg("slackagent session command queue full")
		} else {
			event.Msg("failed to dispatch slackagent session turn")
		}
	}
	return result.Settlement, err
}

func (p *inboundProcessor) prepareSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	locator := baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
	ts, err := p.getOrCreateSession(ctx, locator, inbound.UserID)
	if err != nil {
		return ingressapp.SessionPreparation{}, err
	}
	return ingressapp.SessionPreparation{
		Ready:           true,
		UserID:          ts.GetUserID(),
		RequesterUserID: inbound.UserID,
		AgentSessionID:  ts.GetAgentSessionID(),
	}, nil
}

func (p *inboundProcessor) getOrCreateSession(ctx context.Context, locator baldasession.SessionLocator, subject string) (*baldasession.TopicSession, error) {
	if existing, _ := p.sessions.GetSession(locator); existing != nil {
		return existing, nil
	}
	ts, err := p.sessions.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject})
	if err == nil && ts != nil {
		return ts, nil
	}
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, err
	}
	return p.sessions.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject}, autoSessionLabel)
}

func (p *inboundProcessor) handleQuestionReply(ctx context.Context, env slackagent.IngressEnvelope) (bool, error) {
	if p.questions == nil || !env.HasReply {
		return false, nil
	}
	result, err := p.questions.ResolveReplyDetailed(ctx, env.Reply)
	if err != nil || !result.Matched {
		return result.Matched, err
	}
	if !result.Settled {
		return true, nil
	}
	if p.dispatcher == nil {
		return true, actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	_, err = p.dispatcher.Dispatch(ctx, result.Continuation)
	return true, err
}

type turnCanceller struct {
	control *controlapp.Service
}

func newTurnCanceller(control *controlapp.Service) *turnCanceller {
	return &turnCanceller{control: control}
}

func (c *turnCanceller) CancelTurn(ctx context.Context, stopped slackagent.SessionStopped) error {
	if c == nil || c.control == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is unavailable"))
	}
	return c.control.CancelSessionTurn(ctx, controlcmd.Payload{
		Action:      controlcmd.ActionCancelTurn,
		SessionID:   stopped.Locator.SessionID,
		Locator:     stopped.Locator,
		Reason:      "Slack agent session stopped by user",
		RequestedBy: stopped.RequestedBy,
		Notify:      false,
	})
}

func terminalInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}
