// Package ingressapp owns provider-neutral conversational intake settlement.
package ingressapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
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
	Source      string
}

// Authorizer checks whether one normalized inbound item may enter a session.
type Authorizer interface {
	Authorize(ctx context.Context, inbound InboundContext) (Authorization, error)
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

// Dispatcher durably publishes one SessionActor envelope.
type Dispatcher interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)
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
}

// New constructs a provider-neutral conversational ingress service.
func New(authorizer Authorizer, sessions SessionPreparer, dispatcher Dispatcher) (*Service, error) {
	if authorizer == nil {
		return nil, fmt.Errorf("conversational ingress authorizer is required")
	}
	if sessions == nil {
		return nil, fmt.Errorf("conversational ingress session preparer is required")
	}
	if dispatcher == nil {
		return nil, fmt.Errorf("conversational ingress dispatcher is required")
	}
	return &Service{authorizer: authorizer, sessions: sessions, dispatcher: dispatcher}, nil
}

// Process performs at most one durable SessionActor publish attempt.
func (s *Service) Process(ctx context.Context, inbound turncmd.NormalizedInbound) (Result, error) {
	stableID := strings.TrimSpace(string(inbound.ID))
	result := Result{
		InboundID: turncmd.InboundID(stableID),
		DedupeKey: stableID,
	}
	payload, err := inbound.SessionTurn()
	if err != nil {
		return terminalResult(result, ReasonInvalidInbound), actorlayer.DecodeError(err)
	}
	if strings.TrimSpace(payload.Text) == "" && len(payload.Attachments) == 0 {
		return terminalResult(result, ReasonEmptyInbound), nil
	}
	portContext := inboundContext(inbound)

	authorization, err := s.authorizer.Authorize(ctx, portContext)
	if err != nil {
		return errorResult(result, ReasonUnauthorized, err)
	}
	if !authorization.Allowed {
		return terminalResult(result, firstNonEmpty(authorization.Reason, ReasonUnauthorized)), nil
	}

	preparation, err := s.sessions.Prepare(ctx, portContext)
	if err != nil {
		return errorResult(result, ReasonSessionRejected, err)
	}
	if !preparation.Ready {
		return terminalResult(result, firstNonEmpty(preparation.Reason, ReasonSessionRejected)), nil
	}

	payload.UserID = firstNonEmpty(preparation.UserID, payload.UserID)
	payload.RequesterUserID = firstNonEmpty(preparation.RequesterUserID, inbound.UserID)
	payload.AgentSessionID = strings.TrimSpace(preparation.AgentSessionID)
	payload.TopicID = preparation.TopicID
	envelope, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		return terminalResult(result, ReasonInvalidInbound), actorlayer.DecodeError(err)
	}
	receipt, err := s.dispatcher.Dispatch(ctx, envelope)
	if err != nil {
		return errorResult(result, ReasonDispatchFailed, err)
	}
	if receipt == nil {
		err = actorlayer.TransientError(fmt.Errorf("session dispatch returned no receipt"))
		return retryResult(result, ReasonDispatchFailed), err
	}

	result.Receipt = receipt
	result.Settlement = turncmd.InboundSettlement{
		Outcome: turncmd.InboundAccepted,
		Reason:  ReasonAccepted,
	}
	return result, nil
}

func inboundContext(inbound turncmd.NormalizedInbound) InboundContext {
	return InboundContext{
		InboundID:   inbound.ID,
		SessionID:   strings.TrimSpace(inbound.Locator.SessionID),
		ChannelType: strings.TrimSpace(inbound.Locator.ChannelType),
		AddressKey:  strings.TrimSpace(inbound.Locator.AddressKey),
		AddressJSON: strings.TrimSpace(inbound.Locator.AddressJSON),
		UserID:      strings.TrimSpace(inbound.UserID),
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
