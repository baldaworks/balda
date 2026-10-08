package webhookapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// These aliases let the current policy owner consume the neutral intake
// contract. TASK-007 removes the old execution-mode branch.
const (
	ModeJob     = "job"
	ModeSession = "session"
)

type Request = webhookcmd.Request
type Result = webhookcmd.Result
type TargetNotFoundError = webhookcmd.TargetNotFoundError
type QueueFullError = webhookcmd.QueueFullError
type DispatchFailedError = webhookcmd.DispatchFailedError
type InvalidRequestError = webhookcmd.InvalidRequestError

var (
	IsTargetNotFound = webhookcmd.IsTargetNotFound
	IsQueueFull      = webhookcmd.IsQueueFull
	IsDispatchFailed = webhookcmd.IsDispatchFailed
	IsInvalidRequest = webhookcmd.IsInvalidRequest
)

// TargetResolver resolves a destination reference for application policy.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error)
}

type TargetResolverFunc func(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error)

func (f TargetResolverFunc) ResolveTarget(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
	return f(ctx, target)
}

func NewDestinationTargetResolver(resolver envelopetarget.DestinationResolver) TargetResolver {
	return TargetResolverFunc(func(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
		return envelopetarget.Resolve(ctx, resolver, target)
	})
}

// SessionPublisher and JobPublisher are the current runtime publication ports.
type SessionPublisher interface {
	PublishSessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error)
}

type SessionPublisherFunc func(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error)

func (f SessionPublisherFunc) PublishSessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error) {
	return f(ctx, payload)
}

type JobPublisher interface {
	PublishWebhookJob(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error)
}

type JobPublisherFunc func(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error)

func (f JobPublisherFunc) PublishWebhookJob(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error) {
	return f(ctx, payload, routeName, requestID)
}
