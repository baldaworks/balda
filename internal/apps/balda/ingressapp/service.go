// Package ingressapp owns provider-neutral conversational intake settlement.
package ingressapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

const (
	ReasonAccepted        = "accepted"
	ReasonInvalidInbound  = "invalid_inbound"
	ReasonEmptyInbound    = "empty_inbound"
	ReasonUnauthorized    = "unauthorized"
	ReasonSessionRejected = "session_rejected"
	ReasonDispatchFailed  = "dispatch_failed"
)

// Authorization is the provider-neutral result of an access check.
type Authorization struct {
	Allowed bool
	Reason  string
}

// InboundContext is the minimum safe identity exposed to access and session
// precondition ports. Message text and attachments remain inside the use case.
type InboundContext struct {
	InboundID   turncmd.InboundID
	SessionID   string
	ChannelType string
	AddressKey  string
	AddressJSON string
	UserID      string
	TopicID     int
	Direct      bool
	Source      string
}

// Authorizer checks whether one normalized inbound item may enter a session.
type Authorizer interface {
	Authorize(ctx context.Context, inbound InboundContext) (Authorization, error)
}

// AuthorizerFunc adapts a consumer-owned authorization function to Authorizer.
type AuthorizerFunc func(ctx context.Context, inbound InboundContext) (Authorization, error)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, inbound InboundContext) (Authorization, error) {
	if f == nil {
		return Authorization{}, fmt.Errorf("conversational ingress authorization function is required")
	}
	return f(ctx, inbound)
}

// SessionPreparation contains runtime session identity established before
// durable acceptance. Locator ownership remains with the normalized inbound.
type SessionPreparation struct {
	Ready           bool
	Reason          string
	UserID          string
	RequesterUserID string
	AgentSessionID  string
	TopicID         int
}

// SessionPreparer enforces create/restore/session preconditions without
// exposing a concrete session implementation to conversational ingress.
type SessionPreparer interface {
	Prepare(ctx context.Context, inbound InboundContext) (SessionPreparation, error)
}

// SessionPreparerFunc adapts a consumer-owned session preparation function to SessionPreparer.
type SessionPreparerFunc func(ctx context.Context, inbound InboundContext) (SessionPreparation, error)

// Prepare implements SessionPreparer.
func (f SessionPreparerFunc) Prepare(ctx context.Context, inbound InboundContext) (SessionPreparation, error) {
	if f == nil {
		return SessionPreparation{}, fmt.Errorf("conversational ingress session preparation function is required")
	}
	return f(ctx, inbound)
}

// Dispatcher durably publishes one SessionActor envelope.
type Dispatcher interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)
}

// DispatcherFunc adapts a consumer-owned durable dispatch function to Dispatcher.
type DispatcherFunc func(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)

// Dispatch implements Dispatcher.
func (f DispatcherFunc) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if f == nil {
		return nil, fmt.Errorf("conversational ingress dispatch function is required")
	}
	return f(ctx, env)
}

// Result describes provider-neutral intake settlement and durable acceptance.
type Result struct {
	Settlement turncmd.InboundSettlement
	InboundID  turncmd.InboundID
	DedupeKey  string
	Receipt    *actortransport.DispatchReceipt
}

// Service validates, gates, and durably accepts normalized conversational input.
type Service struct {
	authorizer Authorizer
	sessions   SessionPreparer
	dispatcher Dispatcher
	logger     zerolog.Logger
}

// New constructs a provider-neutral conversational ingress service.
func New(authorizer Authorizer, sessions SessionPreparer, dispatcher Dispatcher) (*Service, error) {
	return NewWithLogger(authorizer, sessions, dispatcher, zerolog.Nop())
}

// NewWithLogger constructs conversational ingress with safe settlement diagnostics.
func NewWithLogger(authorizer Authorizer, sessions SessionPreparer, dispatcher Dispatcher, logger zerolog.Logger) (*Service, error) {
	if authorizer == nil {
		return nil, fmt.Errorf("conversational ingress authorizer is required")
	}
	if sessions == nil {
		return nil, fmt.Errorf("conversational ingress session preparer is required")
	}
	if dispatcher == nil {
		return nil, fmt.Errorf("conversational ingress dispatcher is required")
	}
	return &Service{authorizer: authorizer, sessions: sessions, dispatcher: dispatcher, logger: logger}, nil
}

// Process performs at most one durable SessionActor publish attempt.
func (s *Service) Process(ctx context.Context, inbound turncmd.NormalizedInbound) (Result, error) {
	logContext := inboundContext(inbound)
	stableID := strings.TrimSpace(string(inbound.ID))
	result := Result{
		InboundID: turncmd.InboundID(stableID),
		DedupeKey: stableID,
	}
	payload, err := inbound.SessionTurn()
	if err != nil {
		return s.finish(logContext, terminalResult(result, ReasonInvalidInbound), ReasonInvalidInbound, actorlayer.DecodeError(err))
	}
	if strings.TrimSpace(payload.Text) == "" && len(payload.Attachments) == 0 {
		return s.finish(logContext, terminalResult(result, ReasonEmptyInbound), ReasonEmptyInbound, nil)
	}
	portContext := logContext

	authorization, err := s.authorizer.Authorize(ctx, portContext)
	if err != nil {
		settled, resultErr := errorResult(result, ReasonUnauthorized, err)
		return s.finish(logContext, settled, ReasonUnauthorized, resultErr)
	}
	if !authorization.Allowed {
		return s.finish(logContext, terminalResult(result, firstNonEmpty(authorization.Reason, ReasonUnauthorized)), ReasonUnauthorized, nil)
	}

	preparation, err := s.sessions.Prepare(ctx, portContext)
	if err != nil {
		settled, resultErr := errorResult(result, ReasonSessionRejected, err)
		return s.finish(logContext, settled, ReasonSessionRejected, resultErr)
	}
	if !preparation.Ready {
		return s.finish(logContext, terminalResult(result, firstNonEmpty(preparation.Reason, ReasonSessionRejected)), ReasonSessionRejected, nil)
	}

	payload.UserID = firstNonEmpty(preparation.UserID, payload.UserID)
	payload.RequesterUserID = firstNonEmpty(preparation.RequesterUserID, inbound.UserID)
	payload.AgentSessionID = strings.TrimSpace(preparation.AgentSessionID)
	payload.TopicID = preparation.TopicID
	envelope, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		return s.finish(logContext, terminalResult(result, ReasonInvalidInbound), ReasonInvalidInbound, actorlayer.DecodeError(err))
	}
	receipt, err := s.dispatcher.Dispatch(ctx, envelope)
	if err != nil {
		settled, resultErr := errorResult(result, ReasonDispatchFailed, err)
		return s.finish(logContext, settled, ReasonDispatchFailed, resultErr)
	}
	if receipt == nil {
		err = actorlayer.TransientError(fmt.Errorf("session dispatch returned no receipt"))
		return s.finish(logContext, retryResult(result, ReasonDispatchFailed), ReasonDispatchFailed, err)
	}

	result.Receipt = receipt
	result.Settlement = turncmd.InboundSettlement{
		Outcome: turncmd.InboundAccepted,
		Reason:  ReasonAccepted,
	}
	return s.finish(logContext, result, ReasonAccepted, nil)
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
		Str("inbound_id", strings.TrimSpace(string(result.InboundID))).
		Str("settlement_outcome", string(result.Settlement.Outcome)).
		Str("settlement_stage", strings.TrimSpace(stage)).
		Str("error_class", errorClass).
		Msg("conversational ingress settled")
	return result, resultErr
}

func inboundContext(inbound turncmd.NormalizedInbound) InboundContext {
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

func errorResult(result Result, reason string, err error) (Result, error) {
	if actorlayer.IsRetryableError(err) {
		return retryResult(result, reason), err
	}
	return terminalResult(result, reason), err
}

func retryResult(result Result, reason string) Result {
	result.Settlement = turncmd.InboundSettlement{
		Outcome: turncmd.InboundRetry,
		Reason:  strings.TrimSpace(reason),
	}
	return result
}

func terminalResult(result Result, reason string) Result {
	result.Settlement = turncmd.InboundSettlement{
		Outcome: turncmd.InboundTerminal,
		Reason:  strings.TrimSpace(reason),
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
