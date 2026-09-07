package chatapp

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
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

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, inbound InboundContext) (Authorization, error)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, inbound InboundContext) (Authorization, error) {
	if f == nil {
		return Authorization{Allowed: true}, nil
	}
	return f(ctx, inbound)
}

// SessionPreparation contains runtime session identity established before
// durable acceptance.
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

// SessionPreparerFunc adapts a function to SessionPreparer.
type SessionPreparerFunc func(ctx context.Context, inbound InboundContext) (SessionPreparation, error)

// Prepare implements SessionPreparer.
func (f SessionPreparerFunc) Prepare(ctx context.Context, inbound InboundContext) (SessionPreparation, error) {
	if f == nil {
		return SessionPreparation{}, fmt.Errorf("conversational ingress session preparation function is required")
	}
	return f(ctx, inbound)
}

// QuestionResolution represents the outcome of resolving a question reply.
type QuestionResolution struct {
	Matched      bool
	Settled      bool
	Continuation actorlayer.Envelope
}

// QuestionResolver resolves question replies to determine if a question was answered.
type QuestionResolver interface {
	ResolveQuestionReply(ctx context.Context, reply questioncmd.InboundReply) (QuestionResolution, error)
}

// QuestionResolverFunc adapts a function to QuestionResolver.
type QuestionResolverFunc func(ctx context.Context, reply questioncmd.InboundReply) (QuestionResolution, error)

// ResolveQuestionReply implements QuestionResolver.
func (f QuestionResolverFunc) ResolveQuestionReply(ctx context.Context, reply questioncmd.InboundReply) (QuestionResolution, error) {
	if f == nil {
		return QuestionResolution{}, nil
	}
	return f(ctx, reply)
}

// Dispatcher durably publishes one SessionActor envelope.
type Dispatcher interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)
}

// DispatcherFunc adapts a durable dispatch function to Dispatcher.
type DispatcherFunc func(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)

// Dispatch implements Dispatcher.
func (f DispatcherFunc) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if f == nil {
		return nil, fmt.Errorf("conversational ingress dispatch function is required")
	}
	return f(ctx, env)
}
