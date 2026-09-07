package chatapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

// ServiceParams contains dependencies for constructing a conversational ingress Service.
type ServiceParams struct {
	Sessions   SessionPreparer
	Dispatcher Dispatcher
	Questions  QuestionResolver // optional
	Authorizer Authorizer       // optional
	Logger     zerolog.Logger   // optional
}

// Service owns provider-neutral conversational ingress orchestration:
// question settlement, session preparation, and durable SessionActor publication.
type Service struct {
	sessions   SessionPreparer
	dispatcher Dispatcher
	questions  QuestionResolver
	authorizer Authorizer
	logger     zerolog.Logger
}

// NewService constructs a provider-neutral conversational ingress service.
func NewService(params ServiceParams) (*Service, error) {
	if params.Sessions == nil {
		return nil, fmt.Errorf("session preparer is required")
	}
	if params.Dispatcher == nil {
		return nil, fmt.Errorf("dispatcher is required")
	}
	authorizer := params.Authorizer
	if authorizer == nil {
		authorizer = AuthorizerFunc(func(context.Context, InboundContext) (Authorization, error) {
			return Authorization{Allowed: true}, nil
		})
	}
	return &Service{
		sessions:   params.Sessions,
		dispatcher: params.Dispatcher,
		questions:  params.Questions,
		authorizer: authorizer,
		logger:     params.Logger.With().Str("component", "balda.chatapp").Logger(),
	}, nil
}

// HandleChat handles a conversational ingress request, attempting question resolution first
// when applicable, then session preparation and durable turn dispatch.
func (s *Service) HandleChat(ctx context.Context, request Request) (Result, error) {
	if request.QuestionReply != nil && s.questions != nil {
		handled, activated, err := s.handleQuestionReply(ctx, request)
		if err != nil {
			return Result{Settlement: retryChat(ReasonDispatchFailed)}, err
		}
		if handled {
			return Result{Settlement: terminalChat(ReasonAccepted), Activated: activated}, nil
		}
	}

	inbound := request.NormalizedInbound()
	logContext := inboundContextFromInbound(inbound)

	payload, err := inbound.SessionTurn()
	if err != nil {
		return s.finish(logContext, terminalResult(ReasonInvalidInbound), ReasonInvalidInbound, actorlayer.DecodeError(err))
	}
	if strings.TrimSpace(payload.Text) == "" && len(payload.Attachments) == 0 {
		return s.finish(logContext, terminalResult(ReasonEmptyInbound), ReasonEmptyInbound, nil)
	}

	auth, err := s.authorizer.Authorize(ctx, logContext)
	if err != nil {
		settled, resultErr := errorResult(ReasonUnauthorized, err)
		return s.finish(logContext, settled, ReasonUnauthorized, resultErr)
	}
	if !auth.Allowed {
		return s.finish(logContext, terminalResult(firstNonEmpty(auth.Reason, ReasonUnauthorized)), ReasonUnauthorized, nil)
	}

	prep, err := s.sessions.Prepare(ctx, logContext)
	if err != nil {
		settled, resultErr := errorResult(ReasonSessionRejected, err)
		return s.finish(logContext, settled, ReasonSessionRejected, resultErr)
	}
	if !prep.Ready {
		return s.finish(logContext, terminalResult(firstNonEmpty(prep.Reason, ReasonSessionRejected)), ReasonSessionRejected, nil)
	}

	payload.UserID = firstNonEmpty(prep.UserID, payload.UserID)
	payload.RequesterUserID = firstNonEmpty(prep.RequesterUserID, inbound.UserID)
	payload.AgentSessionID = strings.TrimSpace(prep.AgentSessionID)
	payload.TopicID = prep.TopicID

	envelope, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		return s.finish(logContext, terminalResult(ReasonInvalidInbound), ReasonInvalidInbound, actorlayer.DecodeError(err))
	}

	receipt, err := s.dispatcher.Dispatch(ctx, envelope)
	if err != nil {
		event := s.logger.Warn().Err(err).Str("session_id", request.Locator.SessionID)
		if actorcmd.IsCommandQueueFull(err) {
			event.Msg("session command queue full")
		} else {
			event.Msg("failed to dispatch session turn")
		}
		settled, resultErr := errorResult(ReasonDispatchFailed, err)
		return s.finish(logContext, settled, ReasonDispatchFailed, resultErr)
	}
	if receipt == nil {
		err = actorlayer.TransientError(fmt.Errorf("session dispatch returned no receipt"))
		return s.finish(logContext, retryResult(ReasonDispatchFailed), ReasonDispatchFailed, err)
	}

	res := Result{
		Settlement: turncmd.InboundSettlement{
			Outcome: turncmd.InboundAccepted,
			Reason:  ReasonAccepted,
		},
		Activated: true,
	}
	return s.finish(logContext, res, ReasonAccepted, nil)
}

func (s *Service) handleQuestionReply(ctx context.Context, request Request) (bool, bool, error) {
	if s.questions == nil || request.QuestionReply == nil {
		return false, false, nil
	}
	resolution, err := s.questions.ResolveQuestionReply(ctx, *request.QuestionReply)
	if err != nil || !resolution.Matched {
		return resolution.Matched, false, err
	}
	if !resolution.Settled {
		return true, false, nil
	}
	if s.dispatcher == nil {
		return true, false, actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	continuation := resolution.Continuation
	if inboundID := strings.TrimSpace(string(request.ID)); inboundID != "" {
		continuation.DedupeKey = inboundID
	}
	receipt, err := s.dispatcher.Dispatch(ctx, continuation)
	if err != nil {
		return true, false, err
	}
	if receipt == nil {
		return true, false, actorlayer.TransientError(fmt.Errorf("question continuation dispatch returned no receipt"))
	}
	return true, true, nil
}

func (s *Service) finish(inbound InboundContext, result Result, stage string, resultErr error) (Result, error) {
	event := s.logger.Debug()
	switch result.Settlement.Outcome {
	case turncmd.InboundRetry:
		event = s.logger.Warn()
	case turncmd.InboundAccepted:
		event = s.logger.Info()
	}
	errorClass := "none"
	if resultErr != nil {
		errorClass = string(actorlayer.ClassifyError(resultErr))
		if errorClass == "" {
			errorClass = "unclassified"
		}
	}
	event.
		Str("transport", strings.TrimSpace(inbound.ChannelType)).
		Str("source", strings.TrimSpace(inbound.Source)).
		Str("session_id", strings.TrimSpace(inbound.SessionID)).
		Str("inbound_id", strings.TrimSpace(string(inbound.InboundID))).
		Str("settlement_outcome", string(result.Settlement.Outcome)).
		Str("settlement_stage", strings.TrimSpace(stage)).
		Str("error_class", errorClass).
		Msg("conversational ingress settled")
	return result, resultErr
}

func inboundContextFromInbound(inbound turncmd.NormalizedInbound) InboundContext {
	return InboundContext{
		InboundID:   inbound.ID,
		SessionID:   strings.TrimSpace(inbound.Locator.SessionID),
		ChannelType: strings.TrimSpace(inbound.Locator.ChannelType),
		AddressKey:  strings.TrimSpace(inbound.Locator.AddressKey),
		AddressJSON: strings.TrimSpace(inbound.Locator.AddressJSON),
		UserID:      strings.TrimSpace(inbound.UserID),
		TopicID:     inbound.TopicID,
		Direct:      inbound.Direct,
		Source:      strings.TrimSpace(inbound.Source),
	}
}

func errorResult(reason string, err error) (Result, error) {
	if actorlayer.IsRetryableError(err) {
		return retryResult(reason), err
	}
	return terminalResult(reason), err
}

func retryResult(reason string) Result {
	return Result{
		Settlement: turncmd.InboundSettlement{
			Outcome: turncmd.InboundRetry,
			Reason:  strings.TrimSpace(reason),
		},
		Activated: false,
	}
}

func terminalResult(reason string) Result {
	return Result{
		Settlement: turncmd.InboundSettlement{
			Outcome: turncmd.InboundTerminal,
			Reason:  strings.TrimSpace(reason),
		},
		Activated: false,
	}
}

func terminalChat(reason string) turncmd.InboundSettlement {
	return turncmd.InboundSettlement{
		Outcome: turncmd.InboundTerminal,
		Reason:  strings.TrimSpace(reason),
	}
}

func retryChat(reason string) turncmd.InboundSettlement {
	return turncmd.InboundSettlement{
		Outcome: turncmd.InboundRetry,
		Reason:  strings.TrimSpace(reason),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
