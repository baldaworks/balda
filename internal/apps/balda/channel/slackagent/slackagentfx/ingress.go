package slackagentfx

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	Lifecycle      slackagent.SessionLifecycle
	History        threadHistoryReader
	Logger         zerolog.Logger
}

type threadHistoryReader interface {
	ReadThreadBefore(ctx context.Context, channelID, rootTS, beforeTS string) (slackagent.ThreadSnapshot, error)
}

type inboundProcessor struct {
	sessions   *baldasession.Manager
	dispatcher actortransport.Dispatcher
	questions  *questions.Service
	lifecycle  slackagent.SessionLifecycle
	history    threadHistoryReader
	logger     zerolog.Logger
}

func newInboundProcessor(params inboundProcessorParams) *inboundProcessor {
	return &inboundProcessor{
		sessions:   params.SessionManager,
		dispatcher: params.Dispatcher,
		questions:  params.Question,
		lifecycle:  params.Lifecycle,
		history:    params.History,
		logger:     params.Logger.With().Str("component", "balda.channel.slackagent.ingress").Logger(),
	}
}

func (p *inboundProcessor) ProcessInbound(ctx context.Context, env slackagent.IngressEnvelope) (turncmd.InboundSettlement, error) {
	if p.lifecycle == nil {
		return retryInbound(), actorlayer.TransientError(fmt.Errorf("slackagent session lifecycle is unavailable"))
	}
	originalPrompt := env.Inbound.Text
	if handled, activated, err := p.handleQuestionReply(ctx, env); err != nil {
		return retryInbound(), err
	} else if handled {
		if activated {
			if err := p.lifecycle.BeginTurn(ctx, env.Locator, env.InitiatorUserID, originalPrompt); err != nil {
				return retryInbound(), err
			}
		}
		return terminalInbound(), nil
	}
	if err := p.hydrateThreadContext(ctx, &env); err != nil {
		return retryInbound(), err
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
	if err != nil {
		return result.Settlement, err
	}
	if result.Settlement.Outcome == turncmd.InboundAccepted {
		if err := p.lifecycle.BeginTurn(ctx, env.Locator, env.InitiatorUserID, originalPrompt); err != nil {
			return retryInbound(), err
		}
	}
	return result.Settlement, nil
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

func (p *inboundProcessor) handleQuestionReply(ctx context.Context, env slackagent.IngressEnvelope) (bool, bool, error) {
	if p.questions == nil || !env.HasReply {
		return false, false, nil
	}
	result, err := p.questions.ResolveReplyDetailed(ctx, env.Reply)
	if err != nil || !result.Matched {
		return result.Matched, false, err
	}
	if !result.Settled {
		return true, false, nil
	}
	if p.dispatcher == nil {
		return true, false, actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	continuation := result.Continuation
	if inboundID := strings.TrimSpace(string(env.Inbound.ID)); inboundID != "" {
		// A lifecycle failure after this receipt makes Slack redeliver the same
		// callback. Reuse its provider-stable identity so that a reply already
		// settled as a question cannot later become a second generic turn.
		continuation.DedupeKey = inboundID
	}
	receipt, err := p.dispatcher.Dispatch(ctx, continuation)
	if err != nil {
		return true, false, err
	}
	if receipt == nil {
		return true, false, actorlayer.TransientError(fmt.Errorf("question continuation dispatch returned no receipt"))
	}
	return true, true, nil
}

func (p *inboundProcessor) hydrateThreadContext(ctx context.Context, env *slackagent.IngressEnvelope) error {
	if env == nil || env.ThreadContext == nil {
		return nil
	}
	if p.history == nil {
		return actorlayer.TransientError(fmt.Errorf("slackagent thread history reader is unavailable"))
	}
	request := *env.ThreadContext
	snapshot, err := p.history.ReadThreadBefore(ctx, request.ConversationID, request.RootTS, request.BeforeTS)
	if err != nil {
		if slackagent.IsRetryableSlackError(err) {
			return actorlayer.TransientError(fmt.Errorf("read Slack thread context: %w", err))
		}
		var apiErr *slackagent.APIError
		if !errors.As(err, &apiErr) {
			return actorlayer.TransientError(fmt.Errorf("read Slack thread context: %w", err))
		}
		snapshot = slackagent.UnavailableThreadSnapshot(request, apiErr.Code)
	}
	prompt, err := slackagent.FormatThreadContext(snapshot, env.Inbound.Text)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	env.Inbound.Text = prompt
	return nil
}

type turnCanceller struct {
	control   *controlapp.Service
	lifecycle slackagent.SessionLifecycle
}

func newTurnCanceller(control *controlapp.Service, lifecycle slackagent.SessionLifecycle) *turnCanceller {
	return &turnCanceller{control: control, lifecycle: lifecycle}
}

func (c *turnCanceller) CancelTurn(ctx context.Context, stopped slackagent.SessionStopped) error {
	if c == nil || c.control == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is unavailable"))
	}
	if err := c.control.CancelSessionTurn(ctx, controlcmd.Payload{
		Action:      controlcmd.ActionCancelTurn,
		SessionID:   stopped.Locator.SessionID,
		Locator:     stopped.Locator,
		Reason:      "Slack agent session stopped by user",
		RequestedBy: stopped.RequestedBy,
		Notify:      false,
	}); err != nil {
		return err
	}
	if c.lifecycle == nil {
		return actorlayer.TransientError(fmt.Errorf("slackagent session lifecycle is unavailable"))
	}
	return c.lifecycle.HandleSessionStopped(ctx, stopped.Locator)
}

type boundaryObserver struct {
	lifecycle slackagent.SessionLifecycle
}

func newBoundaryObserver(lifecycle slackagent.SessionLifecycle) *boundaryObserver {
	return &boundaryObserver{lifecycle: lifecycle}
}

func (o *boundaryObserver) BeforeSessionBoundary(ctx context.Context, boundary baldasession.SessionBoundary) error {
	if boundary.Reason != baldasession.BoundaryReasonClose || strings.TrimSpace(boundary.Locator.ChannelType) != slackagent.ChannelType {
		return nil
	}
	if o == nil || o.lifecycle == nil {
		return actorlayer.TransientError(fmt.Errorf("slackagent session lifecycle is unavailable"))
	}
	return o.lifecycle.CloseSession(ctx, boundary.Locator)
}

func terminalInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}
